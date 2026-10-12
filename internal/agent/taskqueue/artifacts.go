// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// artifactExecutor 为当前作业提供对象存储制品操作，不共享可变运行状态
// uploaded 中的值始终是相对于存储 BasePath 的对象键，而不是下载 URL
type artifactExecutor struct {
	job      *taskqueue.JobRunTaskPayload
	storage  storage.IStorage
	uploaded map[string]string
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
			if args.ExpireIn != "" || args.When != "" || args.Name != "" || args.Public || args.Untracked {
				return fmt.Errorf("agent artifact upload supports paths/path only; retention, named bundles, upload policies, public access and untracked filtering are not supported")
			}
			if len(args.Paths) == 0 {
				return fmt.Errorf("artifact upload paths are required")
			}
			paths, err = e.upload(ctx, req.Workspace, args.Paths, stream)
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

func (e *artifactExecutor) upload(ctx context.Context, workspace string, patterns []string, st storage.IStreamingStorage) ([]string, error) {
	for label, id := range map[string]string{"pipeline run": e.job.PipelineRunID, "job run": e.job.JobRunID} {
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
	if e.uploaded == nil {
		e.uploaded = make(map[string]string)
	}
	for _, name := range files {
		// 上传成功后立即记录，后续步骤失败也能报告已存在的对象
		key := path.Join("artifacts", e.job.PipelineRunID, e.job.JobRunID, name)
		if err := uploadArtifactFile(ctx, root, name, key, st); err != nil {
			return nil, fmt.Errorf("upload artifact %s: %w", name, err)
		}
		e.uploaded[name] = key
	}
	return files, nil
}

func (e *artifactExecutor) download(ctx context.Context, workspace string, args artifactArguments, st storage.IStreamingStorage) ([]string, error) {
	if err := artifactIdentifier(args.Job); err != nil {
		return nil, fmt.Errorf("invalid source artifact job: %w", err)
	}
	available := make(map[string]string)
	for name, key := range e.job.ArtifactURIs {
		if strings.HasPrefix(name, args.Job+"/") {
			available[strings.TrimPrefix(name, args.Job+"/")] = key
		}
	}
	if args.Job == e.job.JobName {
		for name, key := range e.uploaded {
			available[name] = key
		}
	}
	if len(args.Paths) == 0 {
		args.Paths = []string{"*"}
	}
	files, err := selectArtifactFiles(ctx, available, args.Paths)
	if err != nil {
		return nil, fmt.Errorf("download from job %s: %w", args.Job, err)
	}
	root, err := openArtifactWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, name := range files {
		if err := downloadArtifactFile(ctx, root, name, available[name], st); err != nil {
			return nil, fmt.Errorf("download artifact %s: %w", name, err)
		}
	}
	return files, nil
}

// handleArtifactDownload 预取上游制品，按作业和相对路径保存在工作目录的 .artifacts 中
func handleArtifactDownload(ctx context.Context, uris map[string]string, workspace string, st storage.IStorage) error {
	if len(uris) == 0 {
		return nil
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
		if err := downloadArtifactFile(ctx, root, path.Join(".artifacts", name), uris[name], stream); err != nil {
			return fmt.Errorf("prefetch artifact %s: %w", name, err)
		}
	}
	return nil
}

func artifactIdentifier(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\:\x00") {
		return fmt.Errorf("invalid path component %q", value)
	}
	return nil
}

func artifactName(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00") {
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

func uploadArtifactFile(ctx context.Context, root *os.Root, name, key string, st storage.IStreamingStorage) error {
	if err := checkArtifactPath(ctx, root, name, false); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact is not a regular file: %s", name)
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact is not a regular file: %s", name)
	}
	return st.UploadStream(ctx, key, &artifactReadSeeker{ctx: ctx, ReadSeeker: file}, info.Size(), "application/octet-stream")
}

// downloadArtifactFile 使用原子重命名提交下载，读取失败或取消不会截断已有制品
func downloadArtifactFile(ctx context.Context, root *os.Root, name, key string, st storage.IStreamingStorage) error {
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
		return err
	}
	defer input.Close()
	temporary := path.Join(dir, ".arcentra-artifact-"+rand.Text())
	output, err := root.OpenFile(filepath.FromSlash(temporary), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close(); _ = root.Remove(filepath.FromSlash(temporary)) }()
	if _, err := io.Copy(output, &artifactReader{ctx: ctx, Reader: input}); err != nil {
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := checkArtifactPath(ctx, root, name, true); err != nil {
		return err
	}
	return root.Rename(filepath.FromSlash(temporary), filepath.FromSlash(name))
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
