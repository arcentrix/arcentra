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
	"encoding/json"
	"fmt"

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

// handleArtifactsUpload 将当前作业的制品保存到本次流水线运行目录
func (m *Manager) handleArtifactsUpload(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var args ArtifactsUploadArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse upload params: %w", err)
	}
	if len(args.Paths) == 0 {
		return nil, fmt.Errorf("paths are required")
	}

	workspace, artifacts, artifactsDir, err := openTransferRoots(ctx, opts, "artifacts", "", true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workspace.Close() }()
	defer func() { _ = artifacts.Close() }()

	paths, err := expandTransferPaths(ctx, workspace, args.Paths)
	if err != nil {
		return nil, err
	}
	for _, name := range paths {
		if err := copyRootPath(ctx, workspace, artifacts, name); err != nil {
			return nil, fmt.Errorf("upload %s: %w", name, err)
		}
	}

	return sonic.Marshal(map[string]any{
		"success":        true,
		"uploaded_files": paths,
		"artifacts_dir":  artifactsDir,
	})
}

// handleArtifactsDownload 从指定作业下载本次流水线运行的制品并保留目录结构
func (m *Manager) handleArtifactsDownload(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var args ArtifactsDownloadArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse download params: %w", err)
	}

	workspace, artifacts, _, err := openTransferRoots(ctx, opts, "artifacts", args.Job, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workspace.Close() }()
	defer func() { _ = artifacts.Close() }()

	requested := args.Paths
	if len(requested) == 0 && args.Path != "" {
		requested = []string{args.Path}
	}
	if len(requested) == 0 {
		requested = []string{"*"}
	}
	paths, err := expandTransferPaths(ctx, artifacts, requested)
	if err != nil {
		return nil, err
	}
	for _, name := range paths {
		if err := copyRootPath(ctx, artifacts, workspace, name); err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
	}

	return sonic.Marshal(map[string]any{
		"success":          true,
		"downloaded_files": paths,
	})
}
