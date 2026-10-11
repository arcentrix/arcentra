// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package builtin

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
)

// openTransferRoots 将文件操作限定到当前工作目录和运行内的指定作业目录
func openTransferRoots(ctx context.Context, opts *Options, kind, job string, create bool) (*os.Root, *os.Root, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, "", err
	}
	if opts == nil || opts.ExecutionContext == nil || opts.ExecutionContext.GetPipeline() == nil {
		return nil, nil, "", fmt.Errorf("execution context and pipeline are required")
	}
	runContext, ok := opts.ExecutionContext.(interface{ GetPipelineRunID() string })
	if !ok || runContext.GetPipelineRunID() == "" {
		return nil, nil, "", fmt.Errorf("pipeline run ID is required")
	}
	if job == "" && opts.Job != nil {
		job = opts.Job.Name
	}
	namespace := opts.ExecutionContext.GetPipeline().Namespace
	runID := runContext.GetPipelineRunID()
	for label, value := range map[string]string{"namespace": namespace, "pipeline run ID": runID, "job": job} {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\:\x00") {
			return nil, nil, "", fmt.Errorf("invalid %s %q", label, value)
		}
	}
	rootPath := opts.ExecutionContext.GetWorkspaceRoot()
	if rootPath == "" || opts.Workspace == "" {
		return nil, nil, "", fmt.Errorf("workspace and workspace root are required")
	}
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, nil, "", err
	}
	absWorkspace, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return nil, nil, "", err
	}
	workspaceName, err := filepath.Rel(absRoot, absWorkspace)
	if err != nil || !filepath.IsLocal(workspaceName) {
		return nil, nil, "", fmt.Errorf("workspace must be inside workspace root")
	}
	storeName := filepath.Join(namespace, runID, kind, job)
	// 禁止源目录包含目标目录，避免上传工作目录时递归复制制品本身
	if pathsOverlap(workspaceName, storeName) {
		return nil, nil, "", fmt.Errorf("workspace and %s directory must not overlap", kind)
	}
	base, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = base.Close() }()
	if err := checkTransferPath(ctx, base, workspaceName, false); err != nil {
		return nil, nil, "", fmt.Errorf("open workspace: %w", err)
	}
	workspace, err := base.OpenRoot(workspaceName)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open workspace: %w", err)
	}
	if err := checkTransferPath(ctx, base, storeName, create); err != nil {
		_ = workspace.Close()
		return nil, nil, "", fmt.Errorf("open %s directory: %w", kind, err)
	}
	if create {
		if err := base.MkdirAll(storeName, 0o755); err != nil {
			_ = workspace.Close()
			return nil, nil, "", fmt.Errorf("create %s directory: %w", kind, err)
		}
	}
	store, err := base.OpenRoot(storeName)
	if err != nil {
		_ = workspace.Close()
		return nil, nil, "", fmt.Errorf("open %s directory: %w", kind, err)
	}
	return workspace, store, filepath.Join(absRoot, storeName), nil
}

func pathsOverlap(a, b string) bool {
	a, b = filepath.ToSlash(a), filepath.ToSlash(b)
	return a == "." || b == "." || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// transferName 仅接受工作目录内的相对路径，通配符使用可移植的斜杠语法
func transferName(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") {
		return "", fmt.Errorf("invalid relative path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("path %q must not traverse parent directories", name)
		}
	}
	clean := path.Clean(name)
	if !fs.ValidPath(clean) {
		return "", fmt.Errorf("invalid relative path %q", name)
	}
	return clean, nil
}

// checkTransferPath 拒绝路径中的符号链接，os.Root 同时防止检查后的目录替换越界
func checkTransferPath(ctx context.Context, root *os.Root, name string, allowMissing bool) error {
	current := "."
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
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
			return fmt.Errorf("symbolic links are not supported: %s", current)
		}
	}
	return nil
}

type contextTransferFS struct {
	ctx context.Context
	fs.FS
}

func (f contextTransferFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.FS.Open(name)
}

func expandTransferPaths(ctx context.Context, root *os.Root, patterns []string) ([]string, error) {
	selected := make(map[string]struct{})
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := transferName(pattern)
		if err != nil {
			return nil, err
		}
		matches, err := fs.Glob(contextTransferFS{ctx: ctx, FS: root.FS()}, name)
		if err != nil {
			return nil, fmt.Errorf("invalid path pattern %q: %w", pattern, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("path pattern %q matched no files", pattern)
		}
		for _, match := range matches {
			if err := checkTransferPath(ctx, root, match, false); err != nil {
				return nil, err
			}
			selected[match] = struct{}{}
		}
	}
	paths := make([]string, 0, len(selected))
	for name := range selected {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths, nil
}

func copyRootPath(ctx context.Context, src, dst *os.Root, name string) error {
	if err := checkTransferPath(ctx, src, name, false); err != nil {
		return err
	}
	if err := checkTransferPath(ctx, dst, name, true); err != nil {
		return err
	}
	info, err := src.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyRootFile(ctx, src, dst, name)
	}
	if err := dst.MkdirAll(filepath.FromSlash(name), 0o755); err != nil {
		return err
	}
	dir, err := src.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	// 分批读取目录，避免大目录占用大量内存并及时响应取消
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			if err := copyRootPath(ctx, src, dst, path.Join(name, entry.Name())); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// copyRootFile 使用临时文件和原子重命名，取消或失败不会留下截断的目标文件
func copyRootFile(ctx context.Context, src, dst *os.Root, name string) error {
	if err := checkTransferPath(ctx, src, name, false); err != nil {
		return err
	}
	if err := checkTransferPath(ctx, dst, name, true); err != nil {
		return err
	}
	info, err := src.Lstat(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files can be copied: %s", name)
	}
	in, err := src.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err = in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files can be copied: %s", name)
	}
	dir := filepath.Dir(filepath.FromSlash(name))
	if err := dst.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(dir, ".arcentra-copy-"+rand.Text())
	out, err := dst.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		_ = dst.Remove(temporary)
	}()
	if _, err := io.Copy(out, &contextTransferReader{ctx: ctx, reader: in}); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return dst.Rename(temporary, filepath.FromSlash(name))
}

type contextTransferReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextTransferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
