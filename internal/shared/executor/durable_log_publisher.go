// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// ErrBuildLogSpoolFull 表示日志队列达到字节或记录数上限，调用方必须处理背压
var ErrBuildLogSpoolFull = errors.New("build log spool is full")

// ErrBuildLogSpoolClosed 表示日志队列已经关闭
var ErrBuildLogSpoolClosed = errors.New("build log spool is closed")

const (
	buildLogSpoolVersion    = 1
	maxBuildLogSpoolRecords = 10000
	maxBuildLogRecordBytes  = 4 << 20
	buildLogRetryInterval   = time.Second
)

type durableLogRecord struct {
	Version  int                       `json:"version"`
	Checksum string                    `json:"checksum"`
	Message  logstream.BuildLogMessage `json:"message"`
}

type durableLogEntry struct {
	name string
	size int64
}

// DurableLogPublisher 将已脱敏日志同步持久化，并在传输确认后删除本地记录
// 目录不能包含符号链接；MaxBytes 计量序列化记录字节，另限制记录数以约束元数据开销
// 调用方必须在 Publish 前脱敏；本类型不接收或保存凭据配置，也不关闭传输对象
// Publish 成功仅表示本机持久化成功，不表示服务端已接收，目录必须位于持久磁盘
// 不支持本地磁盘丢失或多主机共享队列，文件系统必须支持文件与目录 fsync
type DurableLogPublisher struct {
	mu        sync.Mutex
	root      *os.Root
	dir       *os.File
	lock      *os.File
	transport LogPublisher
	maxBytes  int64
	bytes     int64
	next      uint64
	entries   []durableLogEntry
	lastErr   error
	fatalErr  error
	started   bool
	closing   bool
	released  bool
	closeErr  error
	cancel    context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	// 注入同步函数仅供故障测试，生产环境始终调用 File.Sync
	syncFile func(*os.File) error
}

// NewDurableLogPublisher 打开独占日志目录，校验并恢复所有待确认记录
// maxBytes 必须为正；现存记录超限或损坏时保留文件并返回错误
func NewDurableLogPublisher(ctx context.Context, transport LogPublisher, directory string, maxBytes int64) (*DurableLogPublisher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("build log transport is required")
	}
	if directory == "" || maxBytes <= 0 {
		return nil, errors.New("build log spool directory and positive maxBytes are required")
	}
	root, err := openPrivateLogSpool(ctx, directory)
	if err != nil {
		return nil, err
	}
	p := &DurableLogPublisher{root: root, transport: transport, maxBytes: maxBytes, done: make(chan struct{}), wake: make(chan struct{}, 1), syncFile: (*os.File).Sync}
	fail := func(err error) (*DurableLogPublisher, error) { p.release(); return nil, err }
	p.dir, err = root.Open(".")
	if err != nil {
		return fail(fmt.Errorf("open build log spool directory: %w", err))
	}
	info, err := p.dir.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect build log spool directory: %w", err))
	}
	if err := validateLogSpoolOwner(info); err != nil {
		return fail(err)
	}
	if err := p.dir.Chmod(0o700); err != nil {
		return fail(fmt.Errorf("protect build log spool directory: %w", err))
	}
	if err := checkLogSpoolRegular(root, ".lock", true); err != nil {
		return fail(err)
	}
	p.lock, err = root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fail(fmt.Errorf("open build log spool lock: %w", err))
	}
	if err := p.lock.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("protect build log spool lock: %w", err))
	}
	if err := lockBuildLogSpool(p.lock); err != nil {
		return fail(fmt.Errorf("lock build log spool: %w", err))
	}
	if err := p.recover(ctx); err != nil {
		return fail(err)
	}
	if err := p.dir.Sync(); err != nil {
		return fail(fmt.Errorf("sync build log spool directory: %w", err))
	}
	return p, nil
}

// Start 启动按顺序重放日志的后台工作，传入的上下文控制发送生命周期
func (p *DurableLogPublisher) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return ErrBuildLogSpoolClosed
	}
	if p.started {
		return errors.New("build log spool is already started")
	}
	if p.fatalErr != nil {
		return p.fatalErr
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.started = true
	safe.Go(func() {
		defer func() {
			p.mu.Lock()
			if p.closing {
				p.release()
			}
			close(p.done)
			p.mu.Unlock()
		}()
		p.run(runCtx)
	})
	return nil
}

// Publish 复制已脱敏消息，原子落盘并同步文件与目录后才返回成功
// 没有 EventID 时生成并持久化 UUID；不会修改输入消息；满队列立即返回可判定错误
func (p *DurableLogPublisher) Publish(ctx context.Context, msg *logstream.BuildLogMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if msg == nil {
		return errors.New("build log message is required")
	}
	message := *msg
	if strings.TrimSpace(message.StepRunID) == "" {
		return errors.New("build log step run ID is required")
	}
	if message.EventID == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return fmt.Errorf("generate build log event ID: %w", err)
		}
		message.EventID = id.String()
	}
	if err := logstream.ValidateEventID(message.EventID); err != nil {
		return err
	}
	payload, err := sonic.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode build log message: %w", err)
	}
	sum := sha256.Sum256(payload)
	data, err := sonic.Marshal(durableLogRecord{Version: buildLogSpoolVersion, Checksum: hex.EncodeToString(sum[:]), Message: message})
	if err != nil {
		return fmt.Errorf("encode build log record: %w", err)
	}
	if len(data) > maxBuildLogRecordBytes {
		return fmt.Errorf("build log record exceeds %d bytes", maxBuildLogRecordBytes)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.closing {
		return ErrBuildLogSpoolClosed
	}
	if p.fatalErr != nil {
		return p.fatalErr
	}
	if int64(len(data)) > p.maxBytes-p.bytes || len(p.entries) >= maxBuildLogSpoolRecords {
		return ErrBuildLogSpoolFull
	}
	if p.next == ^uint64(0) {
		return errors.New("build log spool sequence exhausted")
	}
	p.next++
	// 序号只需覆盖当前待发送记录；每条记录独立的 UUID 不会随重启复用
	name := fmt.Sprintf("%020d.record", p.next)
	tmp := name + ".tmp"
	f, err := p.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return p.failDisk("create build log record", err)
	}
	fileClosed := false
	cleanup := func(cause error) error {
		var closeErr error
		if !fileClosed {
			closeErr = f.Close()
			fileClosed = true
		}
		removeErr := p.root.Remove(tmp)
		if removeErr == nil {
			removeErr = p.syncFile(p.dir)
		}
		if closeErr == nil && removeErr == nil && (errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
			return cause
		}
		return p.failDisk("persist build log record", errors.Join(cause, closeErr, removeErr))
	}
	if err := f.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := ctx.Err(); err != nil {
		return cleanup(err)
	}
	if err := p.syncFile(f); err != nil {
		return cleanup(err)
	}
	err = f.Close()
	fileClosed = true
	if err != nil {
		return cleanup(err)
	}
	if err := p.root.Rename(tmp, name); err != nil {
		removeErr := p.root.Remove(tmp)
		if removeErr == nil {
			removeErr = p.syncFile(p.dir)
		}
		return p.failDisk("commit build log record", errors.Join(err, removeErr))
	}
	p.entries = append(p.entries, durableLogEntry{name: name, size: int64(len(data))})
	p.bytes += int64(len(data))
	if err := p.syncFile(p.dir); err != nil {
		return p.failDisk("sync committed build log record", err)
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return nil
}

// LastError 返回最近一次发送或存储错误，不包含日志正文
func (p *DurableLogPublisher) LastError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

// Close 停止接收与发送，保留未确认记录；上下文超时不会提前释放仍在使用的锁
// 调用方应先停止日志生产；底层传输必须响应上下文取消才能及时完成关闭
func (p *DurableLogPublisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closing {
		p.closing = true
		if p.cancel != nil {
			p.cancel()
		}
		if !p.started {
			p.release()
			close(p.done)
		}
	}
	done := p.done
	p.mu.Unlock()
	select {
	case <-done:
		p.mu.Lock()
		defer p.mu.Unlock()
		// 发送协程也可能先因 Start 上下文结束而退出
		p.release()
		return p.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *DurableLogPublisher) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		if p.fatalErr != nil {
			p.mu.Unlock()
			return
		}
		if len(p.entries) == 0 {
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		entry := p.entries[0]
		message, err := p.readRecord(ctx, entry.name, entry.size)
		if err != nil {
			if ctx.Err() != nil {
				p.mu.Unlock()
				return
			}
			p.failDisk("read queued build log record", err)
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		err = p.transport.Publish(ctx, &message)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.mu.Lock()
			if p.fatalErr != nil {
				p.mu.Unlock()
				return
			}
			// 传输错误可能携带外部数据，因此诊断仅公开错误类别
			if p.lastErr == nil {
				log.Warnw("build log delivery interrupted; durable records retained", "pendingRecords", len(p.entries), "pendingBytes", p.bytes)
			}
			p.lastErr = errors.New("build log transport did not acknowledge delivery; queued records retained")
			p.mu.Unlock()
			timer := time.NewTimer(buildLogRetryInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		p.mu.Lock()
		if p.fatalErr != nil {
			p.mu.Unlock()
			return
		}
		// 仅成功的 broker ACK 可以触发删除，取消不会丢弃排队数据
		if err := p.root.Remove(entry.name); err != nil {
			p.failDisk("remove acknowledged build log record", err)
			p.mu.Unlock()
			return
		}
		if err := p.syncFile(p.dir); err != nil {
			p.failDisk("sync acknowledged build log removal", err)
			p.mu.Unlock()
			return
		}
		p.entries[0] = durableLogEntry{}
		p.entries = p.entries[1:]
		p.bytes -= entry.size
		if p.lastErr != nil {
			log.Infow("build log delivery resumed after broker acknowledgment", "pendingRecords", len(p.entries), "pendingBytes", p.bytes)
		}
		p.lastErr = nil
		p.mu.Unlock()
	}
}

func (p *DurableLogPublisher) failDisk(operation string, err error) error {
	if p.fatalErr == nil {
		log.Errorw("build log durable storage failed; further appends stopped", "operation", operation, "pendingRecords", len(p.entries), "pendingBytes", p.bytes)
	}
	p.fatalErr = fmt.Errorf("%s: %w", operation, err)
	p.lastErr = p.fatalErr
	return p.fatalErr
}

func (p *DurableLogPublisher) release() {
	if p.released {
		return
	}
	p.released = true
	if p.dir != nil {
		p.closeErr = errors.Join(p.closeErr, p.dir.Close())
	}
	if p.root != nil {
		p.closeErr = errors.Join(p.closeErr, p.root.Close())
	}
	// 最后关闭锁，避免同时存活的目录操作实例
	if p.lock != nil {
		p.closeErr = errors.Join(p.closeErr, p.lock.Close())
	}
}

func (p *DurableLogPublisher) recover(ctx context.Context) error {
	// 分批读取并限制条目数，避免异常目录造成无界内存或清理工作
	scanned, temporary := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := p.dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("scan build log spool: %w", err)
		}
		for _, entry := range entries {
			scanned++
			if scanned > maxBuildLogSpoolRecords+2 {
				return errors.New("build log spool contains too many directory entries")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if name == ".lock" {
				continue
			}
			if err := checkLogSpoolRegular(p.root, name, false); err != nil {
				return err
			}
			if strings.HasSuffix(name, ".tmp") {
				temporary++
				if temporary > 1 {
					return errors.New("build log spool contains multiple incomplete writes; manual inspection required")
				}
				if _, err := parseLogSpoolSequence(strings.TrimSuffix(name, ".tmp")); err != nil {
					return err
				}
				// 临时文件从未被成功接受；一个写入者最多留下一个
				if err := p.root.Remove(name); err != nil {
					return fmt.Errorf("remove incomplete build log record: %w", err)
				}
				continue
			}
			sequence, parseErr := parseLogSpoolSequence(name)
			if parseErr != nil {
				return parseErr
			}
			info, statErr := entry.Info()
			if statErr != nil {
				return fmt.Errorf("inspect build log record: %w", statErr)
			}
			if info.Size() <= 0 || info.Size() > maxBuildLogRecordBytes {
				return errors.New("build log spool contains empty or oversized record")
			}
			if len(p.entries) >= maxBuildLogSpoolRecords || info.Size() > p.maxBytes-p.bytes {
				return fmt.Errorf("recover build log spool: %w", ErrBuildLogSpoolFull)
			}
			if _, readErr := p.readRecord(ctx, name, info.Size()); readErr != nil {
				return readErr
			}
			p.entries = append(p.entries, durableLogEntry{name: name, size: info.Size()})
			p.bytes += info.Size()
			p.next = max(p.next, sequence)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].name < p.entries[j].name })
	return nil
}

func (p *DurableLogPublisher) readRecord(ctx context.Context, name string, size int64) (logstream.BuildLogMessage, error) {
	var record durableLogRecord
	if err := ctx.Err(); err != nil {
		return record.Message, err
	}
	if err := checkLogSpoolRegular(p.root, name, false); err != nil {
		return record.Message, err
	}
	f, err := p.root.Open(name)
	if err != nil {
		return record.Message, fmt.Errorf("open build log record: %w", err)
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return record.Message, fmt.Errorf("protect build log record: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return record.Message, fmt.Errorf("read build log record: %w", err)
	}
	if int64(len(data)) != size {
		return record.Message, errors.New("build log record size changed")
	}
	if err := sonic.Unmarshal(data, &record); err != nil {
		return record.Message, errors.New("build log spool contains malformed JSON")
	}
	if record.Version != buildLogSpoolVersion || record.Message.EventID == "" {
		return record.Message, errors.New("build log spool record version or event ID is invalid")
	}
	if strings.TrimSpace(record.Message.StepRunID) == "" {
		return record.Message, errors.New("build log spool step run ID is missing")
	}
	if err := logstream.ValidateEventID(record.Message.EventID); err != nil {
		return record.Message, errors.New("build log spool event ID is invalid")
	}
	payload, err := sonic.Marshal(record.Message)
	if err != nil {
		return record.Message, errors.New("build log spool payload is invalid")
	}
	sum := sha256.Sum256(payload)
	if record.Checksum != hex.EncodeToString(sum[:]) {
		return record.Message, errors.New("build log spool checksum mismatch")
	}
	return record.Message, nil
}

func parseLogSpoolSequence(name string) (uint64, error) {
	if len(name) != 27 || !strings.HasSuffix(name, ".record") {
		return 0, errors.New("unexpected file in build log spool")
	}
	sequence, err := strconv.ParseUint(name[:20], 10, 64)
	if err != nil || sequence == 0 || fmt.Sprintf("%020d.record", sequence) != name {
		return 0, errors.New("invalid build log spool record name")
	}
	return sequence, nil
}

func checkLogSpoolRegular(root *os.Root, name string, allowMissing bool) error {
	info, err := root.Lstat(name)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect build log spool file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("build log spool contains non-regular file or symbolic link")
	}
	return validateLogSpoolOwner(info)
}

func openPrivateLogSpool(ctx context.Context, path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve build log spool: %w", err)
	}
	volume := filepath.VolumeName(absolute) + string(os.PathSeparator)
	if absolute == volume {
		return nil, errors.New("filesystem root cannot be a build log spool")
	}
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, fmt.Errorf("open build log spool root: %w", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(absolute, volume), string(os.PathSeparator)) {
		if err := ctx.Err(); err != nil {
			root.Close()
			return nil, err
		}
		info, err := root.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(component, 0o700); err != nil {
				root.Close()
				return nil, fmt.Errorf("create build log spool directory: %w", err)
			}
			parent, openErr := root.Open(".")
			if openErr != nil {
				root.Close()
				return nil, openErr
			}
			syncErr := parent.Sync()
			parent.Close()
			if syncErr != nil {
				root.Close()
				return nil, fmt.Errorf("sync build log spool parent: %w", syncErr)
			}
			info, err = root.Lstat(component)
		}
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("inspect build log spool directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errors.New("build log spool path contains a non-directory or symbolic link")
		}
		next, err := root.OpenRoot(component)
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("open build log spool component: %w", err)
		}
		actual, statErr := next.Stat(".")
		after, afterErr := root.Lstat(component)
		root.Close()
		if statErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, actual) || !os.SameFile(info, after) {
			next.Close()
			return nil, errors.New("build log spool directory changed while opening")
		}
		root = next
	}
	return root, nil
}
