// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// artifactExecutor 为当前作业提供对象存储制品操作，不共享可变运行状态
// uploaded 中的值始终是相对于存储 BasePath 的对象键，而不是下载 URL
type artifactExecutor struct {
	job       *taskqueue.JobRunTaskPayload
	storage   storage.IStorage
	uploaded  map[string]string
	reference taskqueue.ArtifactReference
	results   []*agentv1.ArtifactResult
}

type artifactArguments struct {
	Paths     []string `json:"paths"`
	Path      string   `json:"path"`
	Job       string   `json:"job"`
	ExpireIn  string   `json:"expireIn"`
	When      string   `json:"when"`
	Name      string   `json:"name"`
	Public    bool     `json:"public"`
	Untracked bool     `json:"untracked"`
}

func (e *artifactExecutor) Name() string { return "artifacts" }

func (e *artifactExecutor) CanExecute(req *executor.ExecutionRequest) bool {
	return req != nil && req.Step != nil && strings.EqualFold(strings.TrimSpace(req.Step.Uses), "builtin:artifacts")
}

func (e *artifactExecutor) Execute(ctx context.Context, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	result := executor.NewExecutionResult(e.Name())
	var paths []string
	err := func() error {
		if !e.CanExecute(req) || e.job == nil {
			return fmt.Errorf("artifact execution requires a job context")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := sonic.Marshal(req.Step.Args)
		if err != nil {
			return fmt.Errorf("encode artifact arguments: %w", err)
		}
		var args artifactArguments
		if err := sonic.Unmarshal(data, &args); err != nil {
			return fmt.Errorf("decode artifact arguments: %w", err)
		}
		if len(args.Paths) == 0 && args.Path != "" {
			args.Paths = []string{args.Path}
		}
		stream, err := artifactStorage(e.storage)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(req.Step.Action)) {
		case "upload":
			if (args.When != "" && args.When != "always") || args.Name != "" || args.Public || args.Untracked {
				return fmt.Errorf("agent artifact upload supports paths/path and expireIn only; named bundles, upload policies, public access and untracked filtering are not supported")
			}
			if len(args.Paths) == 0 {
				return fmt.Errorf("artifact upload paths are required")
			}
			expiry, parseErr := artifactutil.ParseExpiry(args.ExpireIn, time.Now())
			if parseErr != nil {
				return parseErr
			}
			paths, err = e.upload(ctx, req.Workspace, req.StepRunID, args.Paths, expiry, stream)
		case "download":
			if args.Job == "" {
				args.Job = e.job.JobName
			}
			paths, err = e.download(ctx, req.Workspace, args, stream)
		default:
			err = fmt.Errorf("unsupported artifact action %q", req.Step.Action)
		}
		return err
	}()
	if err != nil {
		result.Complete(false, 1, err)
		return result, err
	}
	result.Metadata["artifact"] = map[string]any{"action": req.Step.Action, "files": paths}
	result.Output = fmt.Sprintf("%s: transferred %d artifact files\n", req.Step.Action, len(paths))
	result.Complete(true, 0, nil)
	return result, nil
}

func artifactStorage(st storage.IStorage) (storage.IStreamingStorage, error) {
	if st == nil {
		return nil, fmt.Errorf("artifact storage is not configured")
	}
	stream, ok := st.(storage.IStreamingStorage)
	if !ok {
		return nil, fmt.Errorf("artifact storage does not support streaming transfers")
	}
	return stream, nil
}

func (e *artifactExecutor) upload(ctx context.Context, workspace, stepRunID string, patterns []string, expiry *time.Time, st storage.IStreamingStorage) ([]string, error) {
	for label, id := range map[string]string{"pipeline run": e.job.PipelineRunID, "job run": e.job.JobRunID, "step run": stepRunID} {
		if err := artifactIdentifier(id); err != nil {
			return nil, fmt.Errorf("invalid %s ID: %w", label, err)
		}
	}
	root, err := openArtifactWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files, err := expandArtifactFiles(ctx, root, patterns)
	if err != nil {
		return nil, err
	}
	if len(files)+len(e.results) > 1000 {
		return nil, fmt.Errorf("artifact upload exceeds 1000 files per job")
	}
	if e.reference.StorageID == "" || e.reference.Fingerprint == "" {
		return nil, fmt.Errorf("artifact storage identity is not configured; re-register the agent")
	}
	if e.uploaded == nil {
		e.uploaded = make(map[string]string)
	}
	for _, name := range files {
		if len(name) > 512 {
			return nil, fmt.Errorf("artifact relative path exceeds 512 bytes")
		}
		// 上传成功后立即记录，后续步骤失败也能报告已存在的对象
		metadata, err := uploadArtifactFile(ctx, root, name, path.Join("artifacts", e.job.PipelineRunID, e.job.JobRunID, stepRunID), st)
		if err != nil {
			return nil, fmt.Errorf("upload artifact %s: %w", name, err)
		}
		metadata.StepRunId = stepRunID
		if expiry != nil {
			metadata.ExpiresAt = expiry.Unix()
		}
		e.results = append(e.results, metadata)
		e.uploaded[name] = metadata.StorageKey
	}
	return files, nil
}

func (e *artifactExecutor) download(ctx context.Context, workspace string, args artifactArguments, st storage.IStreamingStorage) ([]string, error) {
	if err := artifactIdentifier(args.Job); err != nil {
		return nil, fmt.Errorf("invalid source artifact job: %w", err)
	}
	available := make(map[string]string)
	references := make(map[string]taskqueue.ArtifactReference)
	for name, key := range e.job.ArtifactURIs {
		if strings.HasPrefix(name, args.Job+"/") {
			relative := strings.TrimPrefix(name, args.Job+"/")
			available[relative] = key
			references[relative] = e.job.ArtifactReferences[name]
		}
	}
	if args.Job == e.job.JobName {
		for name, key := range e.uploaded {
			available[name] = key
			references[name] = e.reference
		}
		for _, file := range e.results {
			ref := e.reference
			ref.ExpiresAt = file.ExpiresAt
			ref.SHA256 = file.Sha256
			size := file.Size
			ref.Size = &size
			references[file.Name] = ref
		}
	}
	if len(args.Paths) == 0 {
		args.Paths = []string{"*"}
	}
	files, err := selectArtifactFiles(ctx, available, args.Paths)
	if err != nil {
		return nil, fmt.Errorf("download from job %s: %w", args.Job, err)
	}
	selected := make(map[string]string, len(files))
	for _, name := range files {
		selected[name] = available[name]
	}
	if err := validateArtifactReferences(selected, references, e.reference, time.Now()); err != nil {
		return nil, err
	}
	root, err := openArtifactWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, name := range files {
		if err := downloadArtifactFile(ctx, root, name, available[name], references[name], st); err != nil {
			return nil, fmt.Errorf("download artifact %s: %w", name, err)
		}
	}
	return files, nil
}

// handleArtifactDownload 预取上游制品，按作业和相对路径保存在工作目录的 .artifacts 中
func handleArtifactDownload(ctx context.Context, uris map[string]string, references map[string]taskqueue.ArtifactReference, workspace string, st storage.IStorage) error {
	if len(uris) == 0 {
		return nil
	}
	for name := range uris {
		if err := references[name].ValidateIntegrity(); err != nil {
			return fmt.Errorf("artifact %s: %w", name, err)
		}
	}
	stream, err := artifactStorage(st)
	if err != nil {
		return err
	}
	files, err := selectArtifactFiles(ctx, uris, []string{"*"})
	if err != nil {
		return err
	}
	root, err := openArtifactWorkspace(ctx, workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range files {
		if err := downloadArtifactFile(ctx, root, path.Join(".artifacts", name), uris[name], references[name], stream); err != nil {
			return fmt.Errorf("prefetch artifact %s: %w", name, err)
		}
	}
	return nil
}

func artifactIdentifier(value string) error {
	if len(value) > 64 || value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\:\x00\r\n") {
		return fmt.Errorf("invalid path component %q", value)
	}
	return nil
}

func artifactName(value string) (string, error) {
	if len(value) > 1024 || value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00\r\n") {
		return "", fmt.Errorf("invalid relative artifact path %q", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", fmt.Errorf("artifact path must not traverse parent directories: %q", value)
		}
	}
	name := path.Clean(value)
	if !fs.ValidPath(name) {
		return "", fmt.Errorf("invalid relative artifact path %q", value)
	}
	return name, nil
}

func openArtifactWorkspace(ctx context.Context, workspace string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("artifact workspace is required")
	}
	info, err := os.Lstat(workspace)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("artifact workspace must be a real directory")
	}
	return os.OpenRoot(workspace)
}

// checkArtifactPath 拒绝所有符号链接，os.Root 同时阻止检查后的目录替换越界
func checkArtifactPath(ctx context.Context, root *os.Root, name string, allowMissing bool) error {
	name, err := artifactName(name)
	if err != nil {
		return err
	}
	current := "."
	for _, part := range strings.Split(name, "/") {
		if err := ctx.Err(); err != nil {
			return err
		}
		current = path.Join(current, part)
		info, err := root.Lstat(filepath.FromSlash(current))
		if allowMissing && os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic artifact links are not supported: %s", current)
		}
	}
	return nil
}

type artifactFS struct {
	ctx context.Context
	fs.FS
}

func (f artifactFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.FS.Open(name)
}

func expandArtifactFiles(ctx context.Context, root *os.Root, patterns []string) ([]string, error) {
	selected := make(map[string]string)
	for _, pattern := range patterns {
		name, err := artifactName(pattern)
		if err != nil {
			return nil, err
		}
		matches, err := fs.Glob(artifactFS{ctx: ctx, FS: root.FS()}, name)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("artifact path %q matched no files", pattern)
		}
		for _, match := range matches {
			if err := collectArtifactFiles(ctx, root, match, selected); err != nil {
				return nil, err
			}
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("artifact paths matched no regular files")
	}
	return sortedArtifactNames(selected), nil
}

func collectArtifactFiles(ctx context.Context, root *os.Root, name string, files map[string]string) error {
	if err := checkArtifactPath(ctx, root, name, false); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		files[name] = ""
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("only regular artifact files are supported: %s", name)
	}
	dir, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			if err := collectArtifactFiles(ctx, root, path.Join(name, entry.Name()), files); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

func selectArtifactFiles(ctx context.Context, available map[string]string, patterns []string) ([]string, error) {
	for name, key := range available {
		clean, err := artifactName(name)
		if err != nil || clean != name || clean == "." {
			return nil, fmt.Errorf("invalid artifact entry %q", name)
		}
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("artifact %s has no storage key", name)
		}
	}
	selected := make(map[string]string)
	for _, pattern := range patterns {
		name, err := artifactName(pattern)
		if err != nil {
			return nil, err
		}
		if _, err := path.Match(name, ""); err != nil {
			return nil, err
		}
		matched := false
		for file, key := range available {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// 通配符匹配目录时包含其全部子文件，与本地制品 builtin 的语义一致
			for candidate := file; ; candidate = path.Dir(candidate) {
				if ok, _ := path.Match(name, candidate); ok {
					selected[file], matched = key, true
					break
				}
				if candidate == "." {
					break
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("artifact path %q matched no files", pattern)
		}
	}
	return sortedArtifactNames(selected), nil
}

func sortedArtifactNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func uploadArtifactFile(ctx context.Context, root *os.Root, name, prefix string, st storage.IStreamingStorage) (*agentv1.ArtifactResult, error) {
	if err := checkArtifactPath(ctx, root, name, false); err != nil {
		return nil, err
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact is not a regular file: %s", name)
	}
	file, err := openArtifactRegularFile(root, filepath.FromSlash(name), info)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("artifact changed while opening: %s", name)
	}
	// 制品快照以受限权限写入临时文件，避免上传时源文件变化破坏摘要与对象身份
	snapshot, err := os.CreateTemp("", "arcentra-artifact-snapshot-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close(); _ = os.Remove(snapshot.Name()) }()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(snapshot, digest), &artifactReader{ctx: ctx, Reader: file})
	if err != nil {
		return nil, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	key := path.Join(prefix, hash, name)
	if err := st.UploadStream(ctx, key, &artifactReadSeeker{ctx: ctx, ReadSeeker: snapshot}, size, "application/octet-stream"); err != nil {
		return nil, artifactStorageError(err)
	}
	return &agentv1.ArtifactResult{Name: name, StorageKey: key, Size: size, MediaType: "application/octet-stream", Sha256: hash}, nil
}

// validateArtifactReferences 拒绝过期、身份缺失、位置不匹配或内容未绑定的上游制品
// 历史裸机终态仍可回执和展示，但缺少摘要或长度时不得继续下载或进入不可变重试
func validateArtifactReferences(uris map[string]string, references map[string]taskqueue.ArtifactReference, current taskqueue.ArtifactReference, now time.Time) error {
	for name := range uris {
		ref := references[name]
		if current.StorageID == "" || current.Fingerprint == "" || ref.StorageID != current.StorageID || ref.Fingerprint != current.Fingerprint {
			return fmt.Errorf("artifact %s is unavailable: storage identity is missing or changed", name)
		}
		if ref.ExpiresAt != 0 && ref.ExpiresAt <= now.Unix() {
			return fmt.Errorf("artifact %s has expired", name)
		}
		if err := ref.ValidateIntegrity(); err != nil {
			return fmt.Errorf("artifact %s: %w", name, err)
		}
	}
	return nil
}

// downloadArtifactFile 验证流式下载的摘要及长度后原子提交，失败或取消不截断已有制品
func downloadArtifactFile(ctx context.Context, root *os.Root, name, key string, reference taskqueue.ArtifactReference, st storage.IStreamingStorage) error {
	if err := reference.ValidateIntegrity(); err != nil {
		return err
	}
	expectedSize, expectedSHA256 := *reference.Size, reference.SHA256
	if err := checkArtifactPath(ctx, root, name, true); err != nil {
		return err
	}
	if info, err := root.Lstat(filepath.FromSlash(name)); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("artifact destination is not a regular file: %s", name)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	dir := path.Dir(name)
	if err := root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
		return err
	}
	if err := checkArtifactPath(ctx, root, dir, false); err != nil {
		return err
	}
	input, err := st.DownloadStream(ctx, key)
	if err != nil {
		return artifactStorageError(err)
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			_ = input.Close()
		}
	}()
	// 独立私有目录使其他用户无法替换暂存文件；同 UID 的恶意写入不属于隔离边界
	temporary := path.Join(dir, ".arcentra-artifact-"+rand.Text())
	if err := root.Mkdir(filepath.FromSlash(temporary), 0o700); err != nil {
		return err
	}
	defer func() { _ = root.Remove(filepath.FromSlash(temporary)) }()
	directoryInfo, err := root.Lstat(filepath.FromSlash(temporary))
	if err != nil || !directoryInfo.IsDir() {
		return fmt.Errorf("artifact staging directory changed")
	}
	staging, err := root.OpenRoot(filepath.FromSlash(temporary))
	if err != nil {
		return err
	}
	defer staging.Close()
	actualDirectory, err := staging.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, actualDirectory) {
		return fmt.Errorf("artifact staging directory changed")
	}
	const stagedName = "content"
	output, err := staging.OpenFile(stagedName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close(); _ = staging.Remove(stagedName) }()
	fileInfo, err := output.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("artifact staging file is not regular")
	}
	if err := copyVerifiedArtifactContent(ctx, output, artifactStorageReader{Reader: input}, expectedSize, expectedSHA256); err != nil {
		return err
	}
	inputClosed = true
	if err := input.Close(); err != nil {
		return artifactStorageError(err)
	}
	// 校验实际打开文件，避免供应商 Close 回调或其他暂存修改替换已验证的流字节
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := copyVerifiedArtifactContent(ctx, io.Discard, output, expectedSize, expectedSHA256); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := checkArtifactPath(ctx, root, name, true); err != nil {
		return err
	}
	if info, err := root.Lstat(filepath.FromSlash(name)); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("artifact destination is not a regular file: %s", name)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	// 普通 Rename 不支持 inode 条件交换：这些检查检测替换，但不承诺防御同 UID
	// 在最后检查与重命名间持续竞态写入；工作区必须由可信 Agent 独占暂存
	if err := checkArtifactPath(ctx, root, temporary, false); err != nil {
		return err
	}
	currentDirectory, err := root.Lstat(filepath.FromSlash(temporary))
	if err != nil || !currentDirectory.IsDir() || !os.SameFile(directoryInfo, currentDirectory) {
		return fmt.Errorf("artifact staging directory changed before publication")
	}
	currentFile, err := staging.Lstat(stagedName)
	if err != nil || !currentFile.Mode().IsRegular() || !os.SameFile(fileInfo, currentFile) || currentFile.Size() != expectedSize {
		return fmt.Errorf("artifact staging file changed before publication")
	}
	return root.Rename(filepath.FromSlash(path.Join(temporary, stagedName)), filepath.FromSlash(name))
}

func copyVerifiedArtifactContent(ctx context.Context, destination io.Writer, source io.Reader, expectedSize int64, expectedSHA256 string) error {
	reader := &artifactReader{ctx: ctx, Reader: source}
	digest := sha256.New()
	// 最多读取声明长度再探测一个字节，不把超长对象全部下载或写入磁盘
	size, err := io.Copy(io.MultiWriter(destination, digest), io.LimitReader(reader, expectedSize))
	if err != nil {
		return err
	}
	if size != expectedSize {
		return fmt.Errorf("artifact content size does not match registered metadata")
	}
	var extra [1]byte
	n, err := io.ReadFull(reader, extra[:])
	if n != 0 {
		return fmt.Errorf("artifact content exceeds registered size")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		return fmt.Errorf("artifact content SHA256 does not match registered metadata")
	}
	return nil
}

type artifactReader struct {
	ctx context.Context
	io.Reader
}

func (r *artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type artifactReadSeeker struct {
	ctx context.Context
	io.ReadSeeker
}

func (r *artifactReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}

func (r *artifactReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Seek(offset, whence)
}

// artifactStorageError 不把可能包含访问密钥或签名 URL 的供应商错误写入执行元数据
func artifactStorageError(err error) error {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, io.ErrShortWrite, io.ErrShortBuffer} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return fmt.Errorf("object storage operation failed")
}

type artifactStorageReader struct{ io.Reader }

func (r artifactStorageReader) Read(buffer []byte) (int, error) {
	n, err := r.Reader.Read(buffer)
	if err != nil {
		return n, artifactStorageError(err)
	}
	return n, nil
}
