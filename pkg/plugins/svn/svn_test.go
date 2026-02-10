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

package svn

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSVNPlugin(t *testing.T) {
	plugin := NewSVN()

	assert.NotNil(t, plugin)
	assert.Equal(t, "svn", plugin.name)
	assert.Equal(t, "SVN version control plugin for repository operations", plugin.description)
	assert.Equal(t, "1.0.0", plugin.version)
	assert.Equal(t, "svn", plugin.cfg.SVNPath)
	assert.Equal(t, 300, plugin.cfg.Timeout)
	assert.True(t, plugin.cfg.NonInteractive)
}

func TestSVNPlugin_Name(t *testing.T) {
	plugin := NewSVN()
	name := plugin.Name()

	assert.Equal(t, "svn", name)
}

func TestSVNPlugin_Description(t *testing.T) {
	plugin := NewSVN()
	desc := plugin.Description()

	assert.Equal(t, "SVN version control plugin for repository operations", desc)
}

func TestSVNPlugin_Version(t *testing.T) {
	plugin := NewSVN()
	version := plugin.Version()

	assert.Equal(t, "1.0.0", version)
}

func TestSVNPlugin_Type(t *testing.T) {
	p := NewSVN()
	typ := p.Type()

	assert.Equal(t, plugin.TypeSource, typ)
}

func TestSVNPlugin_Init(t *testing.T) {
	tests := []struct {
		name        string
		config      SVNConfig
		expectError bool
		errorMsg    string
		skipIfNoSVN bool
	}{
		{
			name: "正常配置",
			config: SVNConfig{
				SVNPath:         "svn",
				Timeout:         60,
				WorkDir:         "/tmp",
				TrustServerCert: true,
			},
			expectError: false,
			skipIfNoSVN: true,
		},
		{
			name: "空SVNPath使用默认值",
			config: SVNConfig{
				SVNPath: "",
			},
			expectError: false,
			skipIfNoSVN: true,
		},
		{
			name: "不存在的SVN路径",
			config: SVNConfig{
				SVNPath: "/nonexistent/svn",
			},
			expectError: true,
			errorMsg:    "svn not found",
			skipIfNoSVN: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipIfNoSVN {
				if _, err := exec.LookPath("svn"); err != nil {
					t.Skip("svn command not found, skipping test")
				}
			}

			plugin := NewSVN()

			configJSON, err := sonic.Marshal(tt.config)
			require.NoError(t, err)

			err = plugin.Init(configJSON)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				if tt.config.SVNPath != "" {
					assert.Equal(t, tt.config.SVNPath, plugin.cfg.SVNPath)
				} else {
					assert.Equal(t, "svn", plugin.cfg.SVNPath)
				}
			}
		})
	}
}

func TestSVNPlugin_InitWithEmptyConfig(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})

	assert.NoError(t, err)
	assert.Equal(t, "svn", plugin.cfg.SVNPath)
}

func TestSVNPlugin_Cleanup(t *testing.T) {
	plugin := NewSVN()
	err := plugin.Cleanup()

	assert.NoError(t, err)
}

func TestSVNPlugin_CheckoutWithMissingRepo(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("checkout", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository URL is required")
}

func TestSVNPlugin_Status(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建临时目录作为工作副本
	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	// 尝试初始化一个 SVN 工作副本（如果没有可用的仓库，这个测试会跳过）
	// 注意：实际的 SVN 测试需要 SVN 服务器，这里只测试基本功能

	// 测试状态（在没有工作副本的情况下会失败，但这是预期的）
	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("status", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_StatusWithMissingPath(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("status", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "working copy path is required")
}

func TestSVNPlugin_StatusWithVerbose(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path":    workPath,
		"verbose": true,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("status", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_Update(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("update", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_UpdateWithMissingPath(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("update", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "working copy path is required")
}

func TestSVNPlugin_Log(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("log", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_LogWithMissingPath(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("log", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path or URL is required")
}

func TestSVNPlugin_Info(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("info", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_InfoWithMissingPath(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("info", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path or URL is required")
}

func TestSVNPlugin_List(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("list", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_ListWithMissingPath(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("list", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path or URL is required")
}

func TestSVNPlugin_ExecuteUnknownAction(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	_, err = plugin.Execute("unknown_action", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
}

func TestSVNPlugin_ExecuteWithInvalidJSON(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	invalidJSON := json.RawMessage(`{"invalid": json`)
	_, err = plugin.Execute("checkout", invalidJSON, nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestSVNPlugin_ResultStructure(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("status", paramsJSON, nil)
	require.NoError(t, err)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)

	// 验证结果结构
	assert.Contains(t, resultMap, "stdout")
	assert.Contains(t, resultMap, "stderr")
	assert.Contains(t, resultMap, "exit_code")
	assert.Contains(t, resultMap, "duration_ms")
	assert.Contains(t, resultMap, "success")

	// 验证类型
	_, ok := resultMap["stdout"].(string)
	assert.True(t, ok)
	_, ok = resultMap["stderr"].(string)
	assert.True(t, ok)
	_, ok = resultMap["success"].(bool)
	assert.True(t, ok)
}

func TestSVNPlugin_WithAuth(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path": workPath,
		"auth": map[string]string{
			"username": "testuser",
			"password": "testpass",
		},
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("status", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}

func TestSVNPlugin_WithRevision(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn command not found, skipping test")
	}

	plugin := NewSVN()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "work")

	params := map[string]any{
		"path":     workPath,
		"revision": "HEAD",
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("info", paramsJSON, nil)

	// 即使失败，也应该返回结果
	assert.NotNil(t, result)
}
