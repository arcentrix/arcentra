package identitystore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

const (
	recordName     = "identity.json"
	stageName      = ".pending"
	guardName      = "enrollment.pending"
	lockName       = ".lock"
	recordVersion  = 1
	maxRecordBytes = 16 * 1024
)

var (
	// ErrPending 表示注册结果不确定，必须由运维显式恢复而不能再次注册
	ErrPending = errors.New("agent identity enrollment is pending or ambiguous; operator recovery required")
	// ErrLocked 表示身份目录已经被另一个进程占用
	ErrLocked = errors.New("agent identity directory is already locked")
	// ErrClosed 表示身份存储已经关闭
	ErrClosed  = errors.New("agent identity store is closed")
	errUnsafe  = errors.New("agent identity storage has unsafe ownership, permissions, links, or file type")
	errInvalid = errors.New("agent identity record is invalid")
	errChanged = errors.New("agent identity storage changed unexpectedly")
	errIO      = errors.New("agent identity storage operation failed")
)

// State 表示身份存储生命周期状态
type State string

const (
	// StateEmpty 表示该存储从未开始注册
	StateEmpty State = "empty"
	// StatePending 表示注册已经开始但尚未完成本地持久化
	StatePending State = "pending"
	// StateReady 表示身份已经完整持久化
	StateReady State = "ready"
)

// Config 指定显式私有目录、有效构建工作区和实际控制端安全绑定
type Config struct {
	// Directory 是显式指定的绝对私有目录，不提供默认位置
	Directory string
	// Workspace 是构建进程实际使用的工作区，不能为空
	Workspace string
	// ControllerBinding 绑定实际加载的控制端地址、验证名称和信任策略
	ControllerBinding string
}

// Identity 仅包含代理 ID 和单代理凭据，不包含注册令牌或注册响应
type Identity struct {
	// AgentID 是控制端分配的稳定代理标识
	AgentID string
	// PerAgentToken 是仅属于该代理的认证令牌
	PerAgentToken string
}

type record struct {
	Version           int    `json:"version"`
	ControllerBinding string `json:"controllerBinding"`
	State             State  `json:"state"`
	AgentID           string `json:"agentID,omitempty"`
	PerAgentToken     string `json:"perAgentToken,omitempty"`
}

type faultHook func(string) error

// Store 持有身份目录的独占进程锁，必须在代理全部停止后关闭
type Store struct {
	mu            sync.Mutex
	root          *os.Root
	dir           *os.File
	lock          *os.File
	directoryInfo os.FileInfo
	guardInfo     os.FileInfo
	binding       string
	state         State
	identity      Identity
	closed        bool
	fatal         bool
	hook          faultHook
}

// Open 验证安全目录并获取独占锁，拒绝未完成注册、模糊状态和不匹配的绑定
func Open(ctx context.Context, cfg Config) (*Store, error) {
	return openStore(ctx, cfg, nil)
}

func openStore(ctx context.Context, cfg Config, hook faultHook) (_ *Store, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := supportedPlatform(); err != nil {
		return nil, err
	}
	if !validBinding(cfg.ControllerBinding) {
		return nil, errors.New("agent identity controller binding is required and must be bounded")
	}
	directory, err := validateLocations(ctx, cfg.Directory, cfg.Workspace)
	if err != nil {
		return nil, err
	}
	root, err := openPrivateDirectory(ctx, directory, hook)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, binding: cfg.ControllerBinding, state: StateEmpty, hook: hook}
	defer func() {
		if retErr != nil {
			_ = s.Close()
		}
	}()
	s.dir, err = root.Open(".")
	if err != nil {
		return nil, errIO
	}
	s.directoryInfo, err = s.dir.Stat()
	if err != nil || validatePrivateDirectory(s.directoryInfo) != nil {
		return nil, errUnsafe
	}
	if err := s.inspectEntries(ctx); err != nil {
		return nil, err
	}
	s.lock, err = s.openPrivateFile(ctx, lockName, true)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(s.lock); err != nil {
		return nil, err
	}
	if err := s.validateFile(lockName, s.lock); err != nil {
		return nil, err
	}
	if err := s.syncFile(ctx, s.lock, "lock"); err != nil {
		return nil, err
	}
	if err := s.syncDirectory(ctx, "lock"); err != nil {
		return nil, err
	}
	if err := s.inspectEntries(ctx); err != nil {
		return nil, err
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// State 返回内存中的生命周期状态，关闭后的状态不表示可以继续执行
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Identity 返回已经完整持久化的身份，不返回未提交的响应内容
func (s *Store) Identity() (Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fatal || s.state != StateReady {
		return Identity{}, false
	}
	return s.identity, true
}

// ValidateIdentity 验证单代理令牌的有限长度和 ID 绑定，密码学验证由控制端执行
func ValidateIdentity(agentID, perAgentToken string) error {
	if len(agentID) == 0 || len(agentID) > 256 || len(perAgentToken) > 4096 {
		return errInvalid
	}
	for _, c := range agentID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errInvalid
		}
	}
	prefix := agentID + ":"
	if !strings.HasPrefix(perAgentToken, prefix) || len(perAgentToken) == len(prefix) {
		return errInvalid
	}
	for _, c := range perAgentToken[len(prefix):] {
		if c < 33 || c > 126 || c == ':' {
			return errInvalid
		}
	}
	return nil
}

// BeginEnrollment 在任何动态注册 RPC 之前提交不含令牌的待定状态
func (s *Store) BeginEnrollment(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return err
	}
	if s.state != StateEmpty {
		return errors.New("agent identity enrollment cannot replace an existing identity")
	}
	if err := s.verifySnapshot(ctx); err != nil {
		s.fatal = true
		return err
	}
	pending := record{Version: recordVersion, ControllerBinding: s.binding, State: StatePending}
	// This guard survives ready-record rename until ready is known to be durable.
	s.fatal = true
	if err := s.writeNewChecked(ctx, guardName, pending, "guard", &s.guardInfo); err != nil {
		return err
	}
	if err := s.syncDirectory(ctx, "guard"); err != nil {
		return err
	}
	if err := s.atomicRecord(ctx, pending, "pending"); err != nil {
		return err
	}
	s.state, s.fatal = StatePending, false
	return nil
}

// CommitIdentity 仅在本次注册已经开始后原子提交返回的身份，禁止覆盖已有身份
func (s *Store) CommitIdentity(ctx context.Context, agentID, perAgentToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return err
	}
	if s.state != StatePending {
		return errors.New("agent identity commit requires this process's pending enrollment")
	}
	if err := ValidateIdentity(agentID, perAgentToken); err != nil {
		return err
	}
	if err := s.verifySnapshot(ctx); err != nil {
		s.fatal = true
		return err
	}
	s.fatal = true
	ready := record{Version: recordVersion, ControllerBinding: s.binding, State: StateReady, AgentID: agentID, PerAgentToken: perAgentToken}
	if err := s.atomicRecord(ctx, ready, "ready"); err != nil {
		return err
	}
	if err := s.at(ctx, "guard.before_remove"); err != nil {
		return err
	}
	if err := s.validateGuard(); err != nil {
		return err
	}
	if err := s.root.Remove(guardName); err != nil {
		return errIO
	}
	if err := s.at(ctx, "guard.after_remove"); err != nil {
		s.restoreGuard(ctx)
		return err
	}
	if err := s.syncDirectory(ctx, "commit"); err != nil {
		s.restoreGuard(ctx)
		return err
	}
	s.state, s.identity, s.fatal = StateReady, Identity{AgentID: agentID, PerAgentToken: perAgentToken}, false
	return nil
}

func (s *Store) restoreGuard(ctx context.Context) {
	// An in-process failure after unlink restores the fail-closed marker when the
	// filesystem still accepts writes. No local design can promise durability on
	// a disk that refuses every write. The already-synced ready record is safe.
	ctx = context.WithoutCancel(ctx)
	hook := s.hook
	s.hook = nil
	defer func() { s.hook = hook }()
	pending := record{Version: recordVersion, ControllerBinding: s.binding, State: StatePending}
	if err := s.writeNew(ctx, guardName, pending, "restore"); err == nil {
		_ = s.syncDirectory(ctx, "restore")
	}
}

// Close 关闭文件描述符并释放独占锁，保留锁文件和全部恢复证据
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var failed bool
	if s.dir != nil {
		failed = s.dir.Close() != nil || failed
	}
	if s.root != nil {
		failed = s.root.Close() != nil || failed
	}
	if s.lock != nil {
		failed = s.lock.Close() != nil || failed
	}
	s.identity = Identity{}
	if failed {
		return errIO
	}
	return nil
}

func (s *Store) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed {
		return ErrClosed
	}
	if s.fatal {
		return ErrPending
	}
	return nil
}

func (s *Store) at(ctx context.Context, point string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.hook != nil && s.hook(point) != nil {
		return errIO
	}
	return ctx.Err()
}

func (s *Store) syncFile(ctx context.Context, f *os.File, label string) error {
	if err := s.at(ctx, label+".before_file_sync"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return errIO
	}
	return s.at(ctx, label+".after_file_sync")
}

func (s *Store) syncDirectory(ctx context.Context, label string) error {
	if err := s.at(ctx, label+".before_dir_sync"); err != nil {
		return err
	}
	if err := s.dir.Sync(); err != nil {
		return errIO
	}
	return s.at(ctx, label+".after_dir_sync")
}

func (s *Store) writeNew(ctx context.Context, name string, rec record, label string) error {
	return s.writeNewChecked(ctx, name, rec, label, nil)
}

func (s *Store) writeNewChecked(ctx context.Context, name string, rec record, label string, written *os.FileInfo) (retErr error) {
	data, err := sonic.Marshal(rec)
	if err != nil || len(data) > maxRecordBytes {
		return errInvalid
	}
	if err := s.at(ctx, label+".before_create"); err != nil {
		return err
	}
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errIO
	}
	defer func() {
		if f.Close() != nil && retErr == nil {
			retErr = errIO
		}
	}()
	if err := s.validateFile(name, f); err != nil {
		return err
	}
	if err := s.at(ctx, label+".after_create"); err != nil {
		return err
	}
	if err := s.at(ctx, label+".before_write"); err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil || n != len(data) {
		return errIO
	}
	if err := s.at(ctx, label+".after_write"); err != nil {
		return err
	}
	if err := s.syncFile(ctx, f, label); err != nil {
		return err
	}
	if err := s.validateFile(name, f); err != nil {
		return err
	}
	if written != nil {
		info, err := f.Stat()
		if err != nil {
			return errIO
		}
		*written = info
	}
	return nil
}

func (s *Store) atomicRecord(ctx context.Context, rec record, label string) error {
	var written os.FileInfo
	if err := s.writeNewChecked(ctx, stageName, rec, label, &written); err != nil {
		return err
	}
	if err := s.at(ctx, label+".before_rename"); err != nil {
		return err
	}
	current, err := s.root.Lstat(stageName)
	if err != nil || validatePrivateFile(current) != nil || !os.SameFile(written, current) {
		return errChanged
	}
	if err := s.validateFile(lockName, s.lock); err != nil {
		return err
	}
	previous, exists, err := s.readRecord(ctx)
	if err != nil {
		return err
	}
	if rec.State == StatePending && exists {
		return errChanged
	}
	if rec.State == StateReady && (!exists || previous.State != StatePending || previous.ControllerBinding != s.binding) {
		return errChanged
	}
	if err := s.root.Rename(stageName, recordName); err != nil {
		return errIO
	}
	if err := s.at(ctx, label+".after_rename"); err != nil {
		return err
	}
	if err := s.syncDirectory(ctx, label); err != nil {
		return err
	}
	persisted, exists, err := s.readRecord(ctx)
	if err != nil {
		return err
	}
	if !exists || persisted != rec {
		return errChanged
	}
	if _, err := s.root.Lstat(stageName); !errors.Is(err, os.ErrNotExist) {
		return errChanged
	}
	return nil
}

func (s *Store) load(ctx context.Context) error {
	for _, name := range []string{stageName, guardName} {
		if _, err := s.root.Lstat(name); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return errIO
		}
	}
	rec, exists, err := s.readRecord(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if rec.ControllerBinding != s.binding {
		return errors.New("agent identity controller binding does not match")
	}
	if rec.State == StatePending {
		return ErrPending
	}
	s.state, s.identity = StateReady, Identity{AgentID: rec.AgentID, PerAgentToken: rec.PerAgentToken}
	return nil
}

func (s *Store) readRecord(ctx context.Context) (record, bool, error) {
	var rec record
	info, err := s.root.Lstat(recordName)
	if errors.Is(err, os.ErrNotExist) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, errIO
	}
	if err := validatePrivateFile(info); err != nil {
		return rec, false, err
	}
	f, err := s.openPrivateFile(ctx, recordName, false)
	if err != nil {
		return rec, false, err
	}
	defer f.Close()
	if info.Size() > maxRecordBytes || info.Size() <= 0 {
		return rec, false, errInvalid
	}
	if err := ctx.Err(); err != nil {
		return rec, false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return rec, false, errIO
	}
	if len(data) > maxRecordBytes {
		return rec, false, errInvalid
	}
	if err := s.validateFile(recordName, f); err != nil {
		return rec, false, err
	}
	decoder := sonic.ConfigDefault.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rec); err != nil {
		return rec, false, errInvalid
	}
	// Canonical encoding rejects duplicate keys, trailing JSON, alternate casing,
	// unknown fields and ambiguous encodings, without echoing any input.
	canonical, err := sonic.Marshal(rec)
	if err != nil || !bytes.Equal(data, canonical) || rec.Version != recordVersion || !validBinding(rec.ControllerBinding) {
		return rec, false, errInvalid
	}
	switch rec.State {
	case StatePending:
		if rec.AgentID != "" || rec.PerAgentToken != "" {
			return rec, false, errInvalid
		}
	case StateReady:
		if err := ValidateIdentity(rec.AgentID, rec.PerAgentToken); err != nil {
			return rec, false, err
		}
	default:
		return rec, false, errInvalid
	}
	return rec, true, nil
}

func validBinding(binding string) bool {
	if len(binding) == 0 || len(binding) > 8192 || !utf8.ValidString(binding) || strings.TrimSpace(binding) != binding {
		return false
	}
	for _, c := range binding {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func validateLocations(ctx context.Context, directory, workspace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if directory == "" || !filepath.IsAbs(directory) || workspace == "" {
		return "", errors.New("agent identity requires an explicit absolute directory and effective workspace")
	}
	if filepath.Clean(directory) != directory {
		return "", errors.New("agent identity directory must be a canonical absolute path")
	}
	if directory == filepath.VolumeName(directory)+string(os.PathSeparator) {
		return "", errUnsafe
	}
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", errIO
	}
	if pathsOverlap(directory, absoluteWorkspace) {
		return "", errors.New("agent identity directory must be disjoint from the build workspace")
	}
	// Resolve any existing workspace ancestor, so a symlink alias cannot bypass
	// the separation check even when the final workspace has not been created.
	resolved, suffix := absoluteWorkspace, []string{}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		target, err := filepath.EvalSymlinks(resolved)
		if err == nil {
			resolved = target
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errUnsafe
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return "", errUnsafe
		}
		suffix = append(suffix, filepath.Base(resolved))
		resolved = parent
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	if pathsOverlap(directory, resolved) {
		return "", errors.New("agent identity directory must be disjoint from the build workspace")
	}
	return directory, nil
}

func pathsOverlap(a, b string) bool {
	beneath := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
	}
	return beneath(a, b) || beneath(b, a)
}
