// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const (
	terminalOutboxMaxBytes       int64 = 64 << 20
	terminalOutboxMaxRecords           = 10000
	terminalOutboxMaxRecordBytes       = 4 << 20
)

var (
	errTerminalOutboxFull   = errors.New("terminal report outbox is full")
	errTerminalOutboxClosed = errors.New("terminal report outbox is closed")
)

// terminalRecord 只保存终态请求和非认证认领标识，不保存任务、命令、环境或连接凭据
// Payload 是确定性 protobuf 编码，发送时不得重新计算终态或时间戳
// Dependencies 只引用步骤；缺失记录表示步骤已确认，作业记录写入前必须持久化全部待交付步骤
// 本地持久磁盘与控制面终态幂等性共同提供至少一次报告，不提供执行恢复或自动重跑
type terminalRecord struct {
	Version      int      `json:"version"`
	Binding      string   `json:"binding"`
	Kind         string   `json:"kind"`
	RunID        string   `json:"runId"`
	ClaimID      string   `json:"claimId"`
	Dependencies []string `json:"dependencies,omitempty"`
	Payload      []byte   `json:"payload"`
	Checksum     string   `json:"checksum"`
}

type terminalEntry struct {
	record terminalRecord
	size   int64
}

type terminalOutbox struct {
	mu               sync.Mutex
	root             *os.Root
	dir, lock        *os.File
	binding, agentID string
	maxBytes, bytes  int64
	entries          map[string]terminalEntry
	closed           bool
	fatalErr         error
	// 仅供文件同步故障测试注入
	syncFile func(*os.File) error
}

// terminalIOError 防止文件路径、序列化内容或 RPC 错误中的凭据进入日志
type terminalIOError struct {
	operation string
	cause     error
}

func (e *terminalIOError) Error() string { return "terminal report outbox: " + e.operation + " failed" }
func (e *terminalIOError) Unwrap() error { return e.cause }
func terminalIO(operation string, err error) error {
	return &terminalIOError{operation: operation, cause: err}
}

func terminalBinding(agentID, controller string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + strings.TrimSpace(controller)))
	return hex.EncodeToString(sum[:])
}

func terminalRecordName(kind, runID string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + runID))
	return hex.EncodeToString(sum[:]) + ".record"
}

func newTerminalOutbox(ctx context.Context, directory, agentID, controller string) (*terminalOutbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(controller) == "" || directory == "" {
		return nil, errors.New("terminal report outbox requires agent, controller and directory")
	}
	root, err := openPrivateTerminalDirectory(ctx, directory)
	if err != nil {
		return nil, err
	}
	o := &terminalOutbox{root: root, agentID: agentID, binding: terminalBinding(agentID, controller), maxBytes: terminalOutboxMaxBytes, entries: make(map[string]terminalEntry), syncFile: (*os.File).Sync}
	fail := func(err error) (*terminalOutbox, error) { _ = o.close(); return nil, err }
	o.dir, err = root.Open(".")
	if err != nil {
		return fail(terminalIO("open directory", err))
	}
	info, err := o.dir.Stat()
	if err != nil {
		return fail(terminalIO("inspect directory", err))
	}
	if err := validateTerminalOwner(info); err != nil {
		return fail(err)
	}
	if err := o.dir.Chmod(0o700); err != nil {
		return fail(terminalIO("protect directory", err))
	}
	if err := checkTerminalRegular(root, ".lock", true); err != nil {
		return fail(err)
	}
	o.lock, err = root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fail(terminalIO("open lock", err))
	}
	if err := o.lock.Chmod(0o600); err != nil {
		return fail(terminalIO("protect lock", err))
	}
	if err := lockTerminalOutbox(o.lock); err != nil {
		return fail(terminalIO("acquire exclusive lock", err))
	}
	if err := o.recover(ctx); err != nil {
		return fail(err)
	}
	return o, nil
}

func (o *terminalOutbox) recover(ctx context.Context) error {
	if err := checkTerminalRegular(o.root, "identity", true); err != nil {
		return err
	}
	binding, err := o.readFile(ctx, "identity", 65)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	missingIdentity := errors.Is(err, os.ErrNotExist)
	if !missingIdentity && string(binding) != o.binding+"\n" {
		return errors.New("terminal report outbox identity does not match this agent and controller")
	}
	scanned, temporary := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := o.dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return terminalIO("scan directory", err)
		}
		for _, entry := range entries {
			scanned++
			if scanned > terminalOutboxMaxRecords+3 {
				return errors.New("terminal report outbox contains too many entries")
			}
			name := entry.Name()
			if name == ".lock" || name == "identity" {
				continue
			}
			if err := checkTerminalRegular(o.root, name, false); err != nil {
				return err
			}
			if name == ".pending" {
				temporary++
				if temporary > 1 {
					return errors.New("terminal report outbox contains multiple incomplete writes")
				}
				// 原子替换前的临时文件从未允许上报或队列确认
				if err := o.root.Remove(name); err != nil {
					return terminalIO("remove incomplete write", err)
				}
				continue
			}
			if missingIdentity {
				return errors.New("terminal report outbox records have no identity binding")
			}
			if len(name) != 71 || !strings.HasSuffix(name, ".record") {
				return errors.New("unexpected terminal report outbox entry")
			}
			info, err := entry.Info()
			if err != nil {
				return terminalIO("inspect record", err)
			}
			if info.Size() <= 0 || info.Size() > terminalOutboxMaxRecordBytes {
				return errors.New("invalid terminal report record size")
			}
			if len(o.entries) >= terminalOutboxMaxRecords || info.Size() > o.maxBytes-o.bytes {
				return errTerminalOutboxFull
			}
			data, err := o.readFile(ctx, name, info.Size())
			if err != nil {
				return err
			}
			var record terminalRecord
			if err := sonic.Unmarshal(data, &record); err != nil {
				return errors.New("malformed terminal report record")
			}
			if err := o.validateRecord(record); err != nil {
				return err
			}
			if name != terminalRecordName(record.Kind, record.RunID) {
				return errors.New("terminal report record name does not match payload")
			}
			if _, duplicate := o.entries[name]; duplicate {
				return errors.New("duplicate terminal report record")
			}
			o.entries[name] = terminalEntry{record: record, size: info.Size()}
			o.bytes += info.Size()
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if missingIdentity {
		if err := o.writeAtomic(ctx, "identity", []byte(o.binding+"\n")); err != nil {
			return err
		}
	}
	if err := o.syncFile(o.dir); err != nil {
		return terminalIO("sync directory", err)
	}
	return nil
}

func (o *terminalOutbox) validateRecord(r terminalRecord) error {
	if r.Version != 1 || r.Binding != o.binding || r.RunID == "" || len(r.RunID) > 512 || r.ClaimID == "" || len(r.ClaimID) > 64 {
		return errors.New("invalid terminal report identity")
	}
	if len(r.Dependencies) > terminalOutboxMaxRecords {
		return errors.New("too many terminal report dependencies")
	}
	for _, runID := range r.Dependencies {
		if runID == "" || len(runID) > 512 {
			return errors.New("invalid terminal report dependency")
		}
	}
	var agentID, runID string
	var state int32
	switch r.Kind {
	case "step":
		if len(r.Dependencies) != 0 {
			return errors.New("step terminal report cannot have dependencies")
		}
		req := &agentv1.ReportStepRunStatusRequest{}
		if err := proto.Unmarshal(r.Payload, req); err != nil {
			return errors.New("invalid terminal step payload")
		}
		agentID, runID, state = req.AgentId, req.StepRunId, int32(req.Status)
	case "job":
		req := &agentv1.ReportJobRunStatusRequest{}
		if err := proto.Unmarshal(r.Payload, req); err != nil {
			return errors.New("invalid terminal job payload")
		}
		agentID, runID, state = req.AgentId, req.JobRunId, req.Status
	default:
		return errors.New("invalid terminal report kind")
	}
	maxState := int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT)
	if r.Kind == "step" {
		maxState = int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED)
	}
	if agentID != o.agentID || runID != r.RunID || state < 4 || state > maxState {
		return errors.New("terminal report payload identity or status mismatch")
	}
	expected := r.Checksum
	r.Checksum = ""
	data, err := sonic.Marshal(r)
	if err != nil {
		return errors.New("invalid terminal report envelope")
	}
	sum := sha256.Sum256(data)
	if expected != hex.EncodeToString(sum[:]) {
		return errors.New("terminal report checksum mismatch")
	}
	return nil
}

func (o *terminalOutbox) put(ctx context.Context, r terminalRecord) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.closed {
		return errTerminalOutboxClosed
	}
	if o.fatalErr != nil {
		return o.fatalErr
	}
	r.Version = 1
	r.Binding = o.binding
	r.Checksum = ""
	data, err := sonic.Marshal(r)
	if err != nil {
		return errors.New("cannot encode terminal report")
	}
	sum := sha256.Sum256(data)
	r.Checksum = hex.EncodeToString(sum[:])
	if err := o.validateRecord(r); err != nil {
		return err
	}
	data, err = sonic.Marshal(r)
	if err != nil {
		return errors.New("cannot encode terminal report")
	}
	if len(data) > terminalOutboxMaxRecordBytes {
		return errors.New("terminal report exceeds record size limit")
	}
	name := terminalRecordName(r.Kind, r.RunID)
	if previous, exists := o.entries[name]; exists {
		if previous.record.Checksum != r.Checksum {
			return errors.New("terminal report changed after persistence")
		}
		return nil
	}
	if len(o.entries) >= terminalOutboxMaxRecords || int64(len(data)) > o.maxBytes-o.bytes {
		return errTerminalOutboxFull
	}
	if err := o.writeAtomic(ctx, name, data); err != nil {
		return err
	}
	// 与调用方隔离，后续修改切片不能改变已持久化的请求
	r.Payload = bytes.Clone(r.Payload)
	r.Dependencies = append([]string(nil), r.Dependencies...)
	o.entries[name] = terminalEntry{record: r, size: int64(len(data))}
	o.bytes += int64(len(data))
	return nil
}

func (o *terminalOutbox) writeAtomic(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := o.root.OpenFile(".pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return terminalIO("create pending record", err)
	}
	fail := func(operation string, err error) error {
		_ = file.Close()
		_ = o.root.Remove(".pending")
		return terminalIO(operation, err)
	}
	if _, err := file.Write(data); err != nil {
		return fail("write record", err)
	}
	if err := o.syncFile(file); err != nil {
		return fail("sync record", err)
	}
	if err := file.Close(); err != nil {
		return fail("close record", err)
	}
	if err := ctx.Err(); err != nil {
		_ = o.root.Remove(".pending")
		return err
	}
	if err := o.root.Rename(".pending", name); err != nil {
		return fail("commit record", err)
	}
	if err := o.syncFile(o.dir); err != nil {
		// 重命名后同步失败的持久性不确定，停止实例而不是允许后续确认
		o.fatalErr = terminalIO("sync committed record", err)
		return o.fatalErr
	}
	return nil
}

func (o *terminalOutbox) readFile(ctx context.Context, name string, size int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkTerminalRegular(o.root, name, false); err != nil {
		return nil, err
	}
	file, err := o.root.Open(name)
	if err != nil {
		return nil, terminalIO("open record", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return nil, terminalIO("protect record", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return nil, terminalIO("read record", err)
	}
	if int64(len(data)) != size {
		return nil, errors.New("terminal report record size changed")
	}
	return data, nil
}

func (o *terminalOutbox) has(kind, runID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.entries[terminalRecordName(kind, runID)]
	return ok
}

func (o *terminalOutbox) deliver(ctx context.Context, client *agentgrpc.ClientWrapper, kind, runID string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	name := terminalRecordName(kind, runID)
	if _, ok := o.entries[name]; !ok {
		return false, nil
	}
	return true, o.deliverLocked(ctx, client, name)
}

func (o *terminalOutbox) deliverLocked(ctx context.Context, client *agentgrpc.ClientWrapper, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.closed {
		return errTerminalOutboxClosed
	}
	if o.fatalErr != nil {
		return o.fatalErr
	}
	entry, ok := o.entries[name]
	if !ok {
		return nil
	}
	for _, dependency := range entry.record.Dependencies {
		if err := o.deliverLocked(ctx, client, terminalRecordName("step", dependency)); err != nil {
			return err
		}
	}
	if err := sendTerminalRecord(ctx, client, entry.record); err != nil {
		return err
	}
	if err := o.root.Remove(name); err != nil {
		o.fatalErr = terminalIO("remove acknowledged record", err)
		return o.fatalErr
	}
	if err := o.syncFile(o.dir); err != nil {
		o.fatalErr = terminalIO("sync acknowledgement", err)
		return o.fatalErr
	}
	o.bytes -= entry.size
	delete(o.entries, name)
	return nil
}

func (o *terminalOutbox) replay(ctx context.Context, client *agentgrpc.ClientWrapper) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errTerminalOutboxClosed
	}
	if o.fatalErr != nil {
		return o.fatalErr
	}
	names := make([]string, 0, len(o.entries))
	for name := range o.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := o.deliverLocked(ctx, client, name); err != nil {
			return err
		}
	}
	return nil
}

func sendTerminalRecord(ctx context.Context, client *agentgrpc.ClientWrapper, r terminalRecord) error {
	if client == nil || client.AgentClient == nil {
		return errors.New("terminal report client is unavailable")
	}
	ctx, cancel := client.WithTimeoutAndAuth(ctx)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, executionClaimMetadataKey, r.ClaimID)
	switch r.Kind {
	case "step":
		req := &agentv1.ReportStepRunStatusRequest{}
		if err := proto.Unmarshal(r.Payload, req); err != nil {
			return errors.New("invalid terminal step payload")
		}
		response, err := client.AgentClient.ReportStepRunStatus(ctx, req)
		if err != nil {
			return err
		}
		if response == nil || !response.Success {
			return errors.New("terminal step report was not acknowledged")
		}
	case "job":
		req := &agentv1.ReportJobRunStatusRequest{}
		if err := proto.Unmarshal(r.Payload, req); err != nil {
			return errors.New("invalid terminal job payload")
		}
		response, err := client.AgentClient.ReportJobRunStatus(ctx, req)
		if err != nil {
			return err
		}
		if response == nil || !response.Success {
			return errors.New("terminal job report was not acknowledged")
		}
	default:
		return errors.New("invalid terminal report kind")
	}
	return nil
}

func (o *terminalOutbox) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	var err error
	if o.dir != nil {
		err = errors.Join(err, o.dir.Close())
	}
	if o.root != nil {
		err = errors.Join(err, o.root.Close())
	}
	if o.lock != nil {
		err = errors.Join(err, o.lock.Close())
	}
	if err != nil {
		return terminalIO("close", err)
	}
	return nil
}

func checkTerminalRegular(root *os.Root, name string, allowMissing bool) error {
	info, err := root.Lstat(name)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return terminalIO("inspect file", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("terminal report outbox contains a non-regular file or symbolic link")
	}
	return validateTerminalOwner(info)
}

func openPrivateTerminalDirectory(ctx context.Context, path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, terminalIO("resolve directory", err)
	}
	volume := filepath.VolumeName(absolute) + string(os.PathSeparator)
	if absolute == volume {
		return nil, errors.New("filesystem root cannot be a terminal report outbox")
	}
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, terminalIO("open filesystem root", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(absolute, volume), string(os.PathSeparator)) {
		if err := ctx.Err(); err != nil {
			_ = root.Close()
			return nil, err
		}
		info, err := root.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(component, 0o700); err != nil {
				_ = root.Close()
				return nil, terminalIO("create directory", err)
			}
			parent, openErr := root.Open(".")
			if openErr != nil {
				_ = root.Close()
				return nil, terminalIO("open parent", openErr)
			}
			syncErr := parent.Sync()
			_ = parent.Close()
			if syncErr != nil {
				_ = root.Close()
				return nil, terminalIO("sync parent", syncErr)
			}
			info, err = root.Lstat(component)
		}
		if err != nil {
			_ = root.Close()
			return nil, terminalIO("inspect directory", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, errors.New("terminal report outbox path contains a non-directory or symbolic link")
		}
		next, err := root.OpenRoot(component)
		if err != nil {
			_ = root.Close()
			return nil, terminalIO("open directory component", err)
		}
		actual, statErr := next.Stat(".")
		after, afterErr := root.Lstat(component)
		_ = root.Close()
		if statErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, actual) || !os.SameFile(info, after) {
			_ = next.Close()
			return nil, errors.New("terminal report outbox directory changed while opening")
		}
		root = next
	}
	return root, nil
}
