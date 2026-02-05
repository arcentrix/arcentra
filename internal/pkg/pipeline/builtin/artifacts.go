// Copyright 2025 Arcentra Team
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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bytedance/sonic"
)

// ArtifactsUploadArgs 制品上传参数
type ArtifactsUploadArgs struct {
	Paths     []string `json:"paths"`               // 要上传的文件/目录路径（相对于workspace）
	ExpireIn  string   `json:"expireIn,omitempty"`  // 过期时间（如 "7d", "30d"）
	When      string   `json:"when,omitempty"`      // 何时上传：always, on_success, on_failure
	Name      string   `json:"name,omitempty"`      // 制品名称
	Public    bool     `json:"public,omitempty"`    // 是否公开
	Untracked bool     `json:"untracked,omitempty"` // 是否包含未跟踪文件
}

// ArtifactsDownloadArgs 制品下载参数
type ArtifactsDownloadArgs struct {
	Job   string   `json:"job,omitempty"`   // 从哪个job下载
	Paths []string `json:"paths,omitempty"` // 要下载的文件路径
	Path  string   `json:"path,omitempty"`  // 单个文件路径
}

// handleArtifactsUpload handles artifact upload
func (m *Manager) handleArtifactsUpload(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var uploadParams ArtifactsUploadArgs
	if err := sonic.Unmarshal(params, &uploadParams); err != nil {
		return nil, fmt.Errorf("failed to parse upload params: %w", err)
	}

	if len(uploadParams.Paths) == 0 {
		return nil, fmt.Errorf("paths are required")
	}

	// 获取artifacts目录
	if opts == nil || opts.ExecutionContext == nil {
		return nil, fmt.Errorf("execution context is required")
	}

	execCtx := opts.ExecutionContext

	// 获取artifacts目录路径
	workspaceRoot := execCtx.GetWorkspaceRoot()
	pipelineName := execCtx.GetPipeline().Namespace
	buildID := "latest" // TODO: Get build ID from context if available

	artifactsDir := filepath.Join(workspaceRoot, pipelineName, buildID, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, fmt.Errorf("create artifacts directory: %w", err)
	}

	// 上传文件
	uploadedFiles := make([]string, 0)
	for _, path := range uploadParams.Paths {
		srcPath := filepath.Join(opts.Workspace, path)
		dstPath := filepath.Join(artifactsDir, path)

		// 确保目标目录存在
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return nil, fmt.Errorf("create destination directory: %w", err)
		}

		// 复制文件或目录
		if err := m.copyPath(srcPath, dstPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", path, err)
		}

		uploadedFiles = append(uploadedFiles, path)
	}

	result := map[string]any{
		"success":        true,
		"uploaded_files": uploadedFiles,
		"artifacts_dir":  artifactsDir,
	}

	return sonic.Marshal(result)
}

// handleArtifactsDownload handles artifact download
func (m *Manager) handleArtifactsDownload(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var downloadParams ArtifactsDownloadArgs
	if err := sonic.Unmarshal(params, &downloadParams); err != nil {
		return nil, fmt.Errorf("failed to parse download params: %w", err)
	}

	// 获取artifacts目录
	if opts == nil || opts.ExecutionContext == nil {
		return nil, fmt.Errorf("execution context is required")
	}

	execCtx := opts.ExecutionContext

	workspaceRoot := execCtx.GetWorkspaceRoot()
	pipelineName := execCtx.GetPipeline().Namespace
	buildID := "latest" // TODO: Get build ID from context if available

	// 确定源job
	sourceJob := downloadParams.Job
	if sourceJob == "" {
		sourceJob = opts.Job.Name
	}

	artifactsDir := filepath.Join(workspaceRoot, pipelineName, buildID, "artifacts")

	// 确定要下载的路径
	paths := downloadParams.Paths
	if len(paths) == 0 && downloadParams.Path != "" {
		paths = []string{downloadParams.Path}
	}
	if len(paths) == 0 {
		// 下载所有文件
		paths = []string{"*"}
	}

	// 下载文件
	downloadedFiles := make([]string, 0)
	for _, path := range paths {
		srcPath := filepath.Join(artifactsDir, path)
		dstPath := filepath.Join(opts.Workspace, filepath.Base(path))

		if err := m.copyPath(srcPath, dstPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", path, err)
		}

		downloadedFiles = append(downloadedFiles, path)
	}

	result := map[string]any{
		"success":          true,
		"downloaded_files": downloadedFiles,
	}

	return sonic.Marshal(result)
}

// copyPath copies a file or directory
func (m *Manager) copyPath(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return m.copyDir(src, dst)
	}

	return m.copyFile(src, dst)
}

// copyFile copies a file
func (m *Manager) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// copyDir copies a directory recursively
func (m *Manager) copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := m.copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := m.copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}
