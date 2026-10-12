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

// ReportsDotenvArgs 定义 dotenv 报告文件参数
type ReportsDotenvArgs struct {
	Dotenv []string `json:"dotenv"` // 相对于作业工作目录的 dotenv 文件路径
}

// handleReportsDotenv 将 dotenv 报告保存到当前运行和作业的独立目录
func (m *Manager) handleReportsDotenv(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var args ReportsDotenvArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse dotenv params: %w", err)
	}
	if len(args.Dotenv) == 0 {
		return nil, fmt.Errorf("dotenv files are required")
	}

	workspace, reports, reportsDir, err := openTransferRoots(ctx, opts, "reports", "", true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workspace.Close() }()
	defer func() { _ = reports.Close() }()

	paths, err := expandTransferPaths(ctx, workspace, args.Dotenv)
	if err != nil {
		return nil, err
	}
	for _, name := range paths {
		if err := copyRootFile(ctx, workspace, reports, name); err != nil {
			return nil, fmt.Errorf("copy dotenv file %s: %w", name, err)
		}
	}

	return sonic.Marshal(map[string]any{
		"success":        true,
		"reported_files": paths,
		"reports_dir":    reportsDir,
	})
}
