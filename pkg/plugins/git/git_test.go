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

package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "tmp_git_*")
	require.NoError(t, err)
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	return abs
}

func skipIfDotGitNotWritable(t *testing.T) {
	t.Helper()
	tmpDir := workspaceTempDir(t)
	dotGit := filepath.Join(tmpDir, "repo", ".git")
	if err := os.MkdirAll(dotGit, 0o755); err != nil {
		t.Skipf("skip git integration tests: cannot create .git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dotGit, "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Skipf("skip git integration tests: .git is not writable in this environment: %v", err)
	}
}

func TestNewGitPlugin(t *testing.T) {
	plugin := NewGit()

	assert.NotNil(t, plugin)
	assert.Equal(t, "git", plugin.name)
	assert.Equal(t, "Git version control plugin for repository operations", plugin.description)
	assert.Equal(t, "1.0.0", plugin.version)
	assert.Equal(t, "git", plugin.cfg.GitPath)
	assert.Equal(t, 300, plugin.cfg.Timeout)
	assert.False(t, plugin.cfg.Shallow)
}

func TestGitPlugin_Name(t *testing.T) {
	plugin := NewGit()
	name := plugin.Name()

	assert.Equal(t, "git", name)
}

func TestGitPlugin_Description(t *testing.T) {
	plugin := NewGit()
	desc := plugin.Description()

	assert.Equal(t, "Git version control plugin for repository operations", desc)
}

func TestGitPlugin_Version(t *testing.T) {
	plugin := NewGit()
	version := plugin.Version()

	assert.Equal(t, "1.0.0", version)
}

func TestGitPlugin_Type(t *testing.T) {
	p := NewGit()
	typ := p.Type()

	assert.Equal(t, plugin.TypeSource, typ)
}

func TestGitPlugin_Init(t *testing.T) {
	tests := []struct {
		name        string
		config      GitConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "正常配置",
			config: GitConfig{
				GitPath: "git",
				Timeout: 60,
				WorkDir: "/tmp",
			},
			expectError: false,
		},
		{
			name: "空GitPath使用默认值",
			config: GitConfig{
				GitPath: "",
			},
			expectError: false,
		},
		{
			name: "不存在的Git路径",
			config: GitConfig{
				GitPath: "/nonexistent/git",
			},
			expectError: true,
			errorMsg:    "git not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := NewGit()

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
				if tt.config.GitPath != "" {
					assert.Equal(t, tt.config.GitPath, plugin.cfg.GitPath)
				} else {
					assert.Equal(t, "git", plugin.cfg.GitPath)
				}
			}
		})
	}
}

func TestGitPlugin_InitWithEmptyConfig(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})

	assert.NoError(t, err)
	assert.Equal(t, "git", plugin.cfg.GitPath)
}

func TestGitPlugin_Cleanup(t *testing.T) {
	plugin := NewGit()
	err := plugin.Cleanup()

	assert.NoError(t, err)
}

func TestGitPlugin_Clone(t *testing.T) {
	// 跳过如果没有 git 命令
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建一个临时目录作为测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	// 初始化一个测试 git 仓库
	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	// 添加一个文件并提交
	err = os.WriteFile(filepath.Join(repoPath, "test.txt"), []byte("test"), 0o644)
	require.NoError(t, err)

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	// 测试克隆
	cloneDir := workspaceTempDir(t)
	params := map[string]any{
		"repo": repoPath,
		"path": filepath.Join(cloneDir, "cloned"),
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("clone", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
	assert.Contains(t, resultMap, "path")
}

func TestGitPlugin_CloneWithInvalidRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{
		"repo": "/nonexistent/repo",
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("clone", paramsJSON, nil)
	assert.Error(t, err)
}

func TestGitPlugin_CloneWithMissingRepo(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("clone", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository URL is required")
}

func TestGitPlugin_Checkout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Initial commit")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	// 创建分支
	cmd = exec.Command("git", "checkout", "-b", "test-branch")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// 测试检出
	params := map[string]any{
		"ref":  "test-branch",
		"path": repoPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("checkout", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
}

func TestGitPlugin_CheckoutWithMissingRef(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{
		"path": "/tmp/repo",
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("checkout", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ref")
}

func TestGitPlugin_Status(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	// 测试状态
	params := map[string]any{
		"path": repoPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("status", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
	assert.Contains(t, resultMap, "stdout")
}

func TestGitPlugin_StatusWithShortFormat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	params := map[string]any{
		"path":  repoPath,
		"short": true,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("status", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
}

func TestGitPlugin_StatusWithMissingPath(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	params := map[string]any{}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	_, err = plugin.Execute("status", paramsJSON, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository path is required")
}

func TestGitPlugin_Log(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Commit 1")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	// 测试 log
	params := map[string]any{
		"path":  repoPath,
		"limit": 10,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("log", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
	assert.Contains(t, resultMap, "stdout")
}

func TestGitPlugin_Branch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Initial")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	// 测试 branch
	params := map[string]any{
		"path": repoPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("branch", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
}

func TestGitPlugin_Tag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Initial")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "tag", "v1.0.0")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// 测试 tag
	params := map[string]any{
		"path": repoPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, err := plugin.Execute("tag", paramsJSON, nil)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	var resultMap map[string]any
	err = sonic.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.True(t, resultMap["success"].(bool))
	assert.Contains(t, resultMap["stdout"].(string), "v1.0.0")
}

func TestGitPlugin_Pull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	// 创建测试仓库
	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Initial")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
	err = cmd.Run()
	require.NoError(t, err)

	// 测试 pull（在没有远程的情况下会失败，但这是预期的）
	params := map[string]any{
		"path": repoPath,
	}
	paramsJSON, err := sonic.Marshal(params)
	require.NoError(t, err)

	result, _ := plugin.Execute("pull", paramsJSON, nil)

	// pull 可能会失败（没有远程），但我们检查它返回了结果
	assert.NotNil(t, result)
}

func TestGitPlugin_ExecuteUnknownAction(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	_, err = plugin.Execute("unknown_action", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
}

func TestGitPlugin_ExecuteWithInvalidJSON(t *testing.T) {
	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	invalidJSON := json.RawMessage(`{"invalid": json`)
	_, err = plugin.Execute("clone", invalidJSON, nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestGitPlugin_ResultStructure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command not found, skipping test")
	}
	skipIfDotGitNotWritable(t)

	plugin := NewGit()
	err := plugin.Init(json.RawMessage{})
	require.NoError(t, err)

	tmpDir := workspaceTempDir(t)
	repoPath := filepath.Join(tmpDir, "test-repo")

	err = exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	params := map[string]any{
		"path": repoPath,
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
