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
	"os"
	"path/filepath"

	"github.com/bytedance/sonic"
)

// ReportsDotenvArgs contains arguments for dotenv reports
type ReportsDotenvArgs struct {
	Dotenv []string `json:"dotenv"` // List of dotenv file paths
}

// handleReportsDotenv handles dotenv report generation
func (m *Manager) handleReportsDotenv(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var dotenvParams ReportsDotenvArgs
	if err := sonic.Unmarshal(params, &dotenvParams); err != nil {
		return nil, fmt.Errorf("failed to parse dotenv params: %w", err)
	}

	if len(dotenvParams.Dotenv) == 0 {
		return nil, fmt.Errorf("dotenv files are required")
	}

	// 获取reports目录
	if opts == nil || opts.ExecutionContext == nil {
		return nil, fmt.Errorf("execution context is required")
	}

	execCtx := opts.ExecutionContext

	workspaceRoot := execCtx.GetWorkspaceRoot()
	pipelineName := execCtx.GetPipeline().Namespace
	buildID := "latest" // TODO: Get build ID from context if available

	reportsDir := filepath.Join(workspaceRoot, pipelineName, buildID, "reports")
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		return nil, fmt.Errorf("create reports directory: %w", err)
	}

	// 复制dotenv文件到reports目录
	reportedFiles := make([]string, 0)
	for _, dotenvPath := range dotenvParams.Dotenv {
		srcPath := filepath.Join(opts.Workspace, dotenvPath)
		dstPath := filepath.Join(reportsDir, filepath.Base(dotenvPath))

		if err := m.copyFile(srcPath, dstPath); err != nil {
			return nil, fmt.Errorf("copy dotenv file %s: %w", dotenvPath, err)
		}

		reportedFiles = append(reportedFiles, dotenvPath)
	}

	result := map[string]any{
		"success":        true,
		"reported_files": reportedFiles,
		"reports_dir":    reportsDir,
	}

	return sonic.Marshal(result)
}
