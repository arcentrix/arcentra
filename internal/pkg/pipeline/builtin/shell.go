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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// ShellScriptArgs contains arguments for executing a shell script
type ShellScriptArgs struct {
	Script string            `json:"script"`
	Args   []string          `json:"args,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// ShellCommandArgs contains arguments for executing a shell command
type ShellCommandArgs struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// ShellConfig contains optional shell execution configuration
type ShellConfig struct {
	Shell          string `json:"shell,omitempty"`          // Shell interpreter path, defaults to /bin/sh
	Timeout        int    `json:"timeout,omitempty"`        // Timeout in seconds, 0 means no timeout
	AllowDangerous bool   `json:"allowDangerous,omitempty"` // Whether to allow dangerous operations
}

// dangerousPatterns 危险操作模式列表
var dangerousPatterns = []string{
	"rm -rf",
	":(){ :|:& };:",
	"mkfs",
	"dd if=",
	"> /dev/",
}

// handleShellScript handles shell script execution
func (m *Manager) handleShellScript(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var scriptParams ShellScriptArgs
	if err := sonic.Unmarshal(params, &scriptParams); err != nil {
		return nil, fmt.Errorf("failed to parse script params: %w", err)
	}

	if scriptParams.Script == "" {
		return nil, fmt.Errorf("script content is required")
	}

	// 解析配置（从params中提取config字段，如果存在）
	var config ShellConfig
	var paramsMap map[string]any
	if err := json.Unmarshal(params, &paramsMap); err == nil {
		if cfg, exists := paramsMap["config"].(map[string]any); exists {
			configJSON, _ := json.Marshal(cfg)
			_ = json.Unmarshal(configJSON, &config)
		}
	}

	// Security check: prevent dangerous operations
	if !config.AllowDangerous {
		for _, pattern := range dangerousPatterns {
			if strings.Contains(scriptParams.Script, pattern) {
				return nil, fmt.Errorf("dangerous operation detected and not allowed: %s", pattern)
			}
		}
	}

	// Merge environment variables
	env := make(map[string]string)
	if opts != nil && opts.Env != nil {
		maps.Copy(env, opts.Env)
	}
	if scriptParams.Env != nil {
		maps.Copy(env, scriptParams.Env)
	}

	workDir := opts.Workspace
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Determine shell path
	shell := config.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Validate shell exists
	if _, err := os.Stat(shell); os.IsNotExist(err) {
		return nil, fmt.Errorf("shell not found: %s", shell)
	}

	// Create temporary script file with UUID-based name for uniqueness
	tmpDir := os.TempDir()
	tmpFileName := filepath.Join(tmpDir, fmt.Sprintf("%s.sh", uuid.New().String()))
	tmpFile, err := os.Create(tmpFileName)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		tmpFile.Close()
		if err := os.Remove(tmpFileName); err != nil {
			// Ignore cleanup errors
			_ = err
		}
	}()

	// Write script content
	if _, err := tmpFile.WriteString(scriptParams.Script); err != nil {
		return nil, fmt.Errorf("failed to write script: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Set executable permissions
	if err := os.Chmod(tmpFileName, 0755); err != nil {
		return nil, fmt.Errorf("failed to chmod script file: %w", err)
	}

	// Execute script
	return m.runShellCommand(ctx, shell, append([]string{tmpFileName}, scriptParams.Args...), env, workDir, config.Timeout)
}

// handleShellCommand handles shell command execution
func (m *Manager) handleShellCommand(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var cmdParams ShellCommandArgs
	if err := sonic.Unmarshal(params, &cmdParams); err != nil {
		return nil, fmt.Errorf("failed to parse command params: %w", err)
	}

	if cmdParams.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	// Parse config from params if exists
	var config ShellConfig
	var paramsMap map[string]any
	if err := sonic.Unmarshal(params, &paramsMap); err == nil {
		if cfg, exists := paramsMap["config"].(map[string]any); exists {
			configJSON, _ := sonic.Marshal(cfg)
			_ = sonic.Unmarshal(configJSON, &config)
		}
	}

	// Security check: prevent dangerous operations
	if !config.AllowDangerous {
		dangerous := []string{"rm -rf /", ":(){ :|:& };:", "mkfs", "dd if=/dev/zero", "> /dev/"}
		for _, pattern := range dangerous {
			if strings.Contains(cmdParams.Command, pattern) {
				return nil, fmt.Errorf("dangerous operation detected and not allowed: %s", pattern)
			}
		}
	}

	// Merge environment variables
	env := make(map[string]string)
	if opts != nil && opts.Env != nil {
		maps.Copy(env, opts.Env)
	}
	if cmdParams.Env != nil {
		maps.Copy(env, cmdParams.Env)
	}

	workDir := opts.Workspace
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Determine shell path
	shell := config.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Validate shell exists
	if _, err := os.Stat(shell); os.IsNotExist(err) {
		return nil, fmt.Errorf("shell not found: %s", shell)
	}

	// Execute command
	args := append([]string{"-c", cmdParams.Command}, cmdParams.Args...)
	return m.runShellCommand(ctx, shell, args, env, workDir, config.Timeout)
}

// runShellCommand executes a shell command
// shell is the shell interpreter path (e.g., "/bin/sh")
// args are arguments passed to the shell
// timeout is timeout in seconds, 0 means no timeout
func (m *Manager) runShellCommand(ctx context.Context, shell string, args []string, env map[string]string, workDir string, timeout int) (json.RawMessage, error) {
	// Create context with timeout if configured
	cmdCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	// Create command
	cmd := exec.CommandContext(cmdCtx, shell, args...)
	cmd.Dir = workDir

	// Set environment variables
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Record start time
	startTime := time.Now()

	// Execute command
	err := cmd.Run()
	duration := time.Since(startTime)

	// Prepare result
	result := map[string]any{
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"duration_ms": duration.Milliseconds(),
		"success":     err == nil,
	}

	if err != nil {
		result["error"] = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result["exit_code"] = exitErr.ExitCode()
		} else {
			// For non-exit errors (e.g., path errors, timeout), set exit_code to -1
			result["exit_code"] = -1
		}
	} else {
		result["exit_code"] = 0
	}

	return sonic.Marshal(result)
}
