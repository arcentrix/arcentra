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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
)

// Config is the plugin's configuration structure
type Config struct {
	// SVN executable path (default: svn)
	SVNPath string `json:"svnPath"`
	// Default timeout in seconds (0 means no timeout)
	Timeout int `json:"timeout"`
	// Default working directory
	WorkDir string `json:"workDir"`
	// Default username for authentication
	Username string `json:"username"`
	// Default password for authentication
	Password string `json:"password"`
	// Whether to trust server certificates
	TrustServerCert bool `json:"trustServerCert"`
	// Non-interactive mode
	NonInteractive bool `json:"nonInteractive"`
}

// CheckoutArgs contains arguments for checking out a repository
type CheckoutArgs struct {
	Repo     string            `json:"repo"`     // Repository URL
	Path     string            `json:"path"`     // Destination path (optional)
	Revision string            `json:"revision"` // Revision to checkout (optional, HEAD by default)
	Depth    string            `json:"depth"`    // Depth (empty, files, immediates, infinity)
	Auth     map[string]string `json:"auth"`     // Authentication (username, password)
	Env      map[string]string `json:"env"`      // Additional environment variables
}

// UpdateArgs contains arguments for updating a working copy
type UpdateArgs struct {
	Path     string            `json:"path"`     // Working copy path (required)
	Revision string            `json:"revision"` // Revision to update to (optional, HEAD by default)
	Depth    string            `json:"depth"`    // Depth (empty, files, immediates, infinity)
	Auth     map[string]string `json:"auth"`     // Authentication (username, password)
	Env      map[string]string `json:"env"`      // Additional environment variables
}

// StatusArgs contains arguments for checking working copy status
type StatusArgs struct {
	Path        string            `json:"path"`         // Working copy path (required)
	Verbose     bool              `json:"verbose"`      // Verbose output
	ShowUpdates bool              `json:"show_updates"` // Show update information
	Env         map[string]string `json:"env"`          // Additional environment variables
}

// LogArgs contains arguments for viewing commit history
type LogArgs struct {
	Path     string            `json:"path"`     // Working copy path or URL (required)
	Revision string            `json:"revision"` // Revision range (optional, e.g., "r1:r100")
	Limit    int               `json:"limit"`    // Limit number of entries
	Verbose  bool              `json:"verbose"`  // Verbose output
	Auth     map[string]string `json:"auth"`     // Authentication (username, password)
	Env      map[string]string `json:"env"`      // Additional environment variables
}

// InfoArgs contains arguments for viewing repository information
type InfoArgs struct {
	Path     string            `json:"path"`     // Working copy path or URL (required)
	Revision string            `json:"revision"` // Revision (optional)
	Auth     map[string]string `json:"auth"`     // Authentication (username, password)
	Env      map[string]string `json:"env"`      // Additional environment variables
}

// ListArgs contains arguments for listing directory contents
type ListArgs struct {
	Path      string            `json:"path"`      // Working copy path or URL (required)
	Revision  string            `json:"revision"`  // Revision (optional)
	Recursive bool              `json:"recursive"` // Recursive listing
	Auth      map[string]string `json:"auth"`      // Authentication (username, password)
	Env       map[string]string `json:"env"`       // Additional environment variables
}

// SVN implements the svn plugin
type SVN struct {
	*plugin.Base
	name        string
	description string
	version string
	cfg     Config
}

// Action definitions
var (
	actions = map[string]string{
		"checkout": "Checkout (clone) an SVN repository",
		"update":   "Update a working copy",
		"status":   "Check working copy status",
		"log":      "View commit history",
		"info":     "View repository information",
		"list":     "List directory contents",
	}
)

// NewSVN creates a new svn plugin instance
func NewSVN() *SVN {
	p := &SVN{
		Base:        plugin.NewPluginBase(),
		name:        "svn",
		description: "SVN version control plugin for repository operations",
		version:     "1.0.0",
		cfg: Config{
			SVNPath:         "svn",
			Timeout:         300, // 5 minutes default
			TrustServerCert: false,
			NonInteractive:  true,
		},
	}

	// Register actions using Action Registry
	p.registerActions()
	return p
}

// registerActions registers all actions for this plugin
func (p *SVN) registerActions() {
	// Register "checkout" action
	if err := p.Registry().RegisterFunc("checkout", actions["checkout"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.checkout(params, opts)
	}); err != nil {
		return
	}

	// Register "update" action
	if err := p.Registry().RegisterFunc("update", actions["update"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.update(params, opts)
	}); err != nil {
		return
	}

	// Register "status" action
	if err := p.Registry().RegisterFunc("status", actions["status"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.status(params, opts)
	}); err != nil {
		return
	}

	// Register "log" action
	if err := p.Registry().RegisterFunc("log", actions["log"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.log(params, opts)
	}); err != nil {
		return
	}

	// Register "info" action
	if err := p.Registry().RegisterFunc("info", actions["info"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.info(params, opts)
	}); err != nil {
		return
	}

	// Register "list" action
	if err := p.Registry().RegisterFunc("list", actions["list"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.list(params, opts)
	}); err != nil {
		return
	}
}

// Name returns the plugin name
func (p *SVN) Name() string {
	return p.name
}

// Description returns the plugin description
func (p *SVN) Description() string {
	return p.description
}

// Version returns the plugin version
func (p *SVN) Version() string {
	return p.version
}

// Type returns the plugin type
func (p *SVN) Type() plugin.Type {
	return plugin.TypeSource
}

// Author returns the plugin author
func (p *SVN) Author() string {
	return "Arcentra Authors."
}

// Repository returns the plugin repository
func (p *SVN) Repository() string {
	return "https://github.com/arcentrix/arcentra"
}

// Init initializes the plugin
func (p *SVN) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}
	}

	// Validate svn path
	if p.cfg.SVNPath == "" {
		p.cfg.SVNPath = "svn"
	}

	// Check if svn exists
	if _, err := exec.LookPath(p.cfg.SVNPath); err != nil {
		return fmt.Errorf("svn not found: %s", p.cfg.SVNPath)
	}

	log.Infow("svn plugin initialized", "plugin", "svn", "svn_path", p.cfg.SVNPath, "timeout", p.cfg.Timeout)
	return nil
}

// Cleanup cleans up the plugin
func (p *SVN) Cleanup() error {
	log.Infow("svn plugin cleanup completed", "plugin", "svn")
	return nil
}

// Execute executes svn operations using Action Registry
func (p *SVN) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.Base.Execute(action, params, opts)
}

// ===== Action Handlers =====

// checkout checks out (clones) an SVN repository
func (p *SVN) checkout(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var checkoutParams CheckoutArgs
	if err := sonic.Unmarshal(params, &checkoutParams); err != nil {
		return nil, fmt.Errorf("failed to parse checkout params: %w", err)
	}

	if checkoutParams.Repo == "" {
		return nil, fmt.Errorf("repository URL is required")
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if checkoutParams.Path == "" {
					checkoutParams.Path = workspace
				}
			}
		}
	}

	// Determine destination path
	destPath := checkoutParams.Path
	if destPath == "" {
		// Extract repo name from URL
		repoName := filepath.Base(strings.TrimSuffix(checkoutParams.Repo, "/"))
		destPath = repoName
	}

	// Build svn checkout command
	args := []string{"checkout"}

	// Revision
	if checkoutParams.Revision != "" {
		args = append(args, "-r", checkoutParams.Revision)
	}

	// Depth
	if checkoutParams.Depth != "" {
		args = append(args, "--depth", checkoutParams.Depth)
	}

	args = append(args, checkoutParams.Repo, destPath)

	// Execute checkout
	result, err := p.runSVNCommand(args, checkoutParams.Auth, checkoutParams.Env, "")
	if err != nil {
		return nil, fmt.Errorf("checkout failed: %w", err)
	}

	result["path"] = destPath
	return sonic.Marshal(result)
}

// update updates a working copy
func (p *SVN) update(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var updateParams UpdateArgs
	if err := sonic.Unmarshal(params, &updateParams); err != nil {
		return nil, fmt.Errorf("failed to parse update params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if updateParams.Path == "" {
					updateParams.Path = workspace
				}
			}
		}
	}

	if updateParams.Path == "" {
		return nil, fmt.Errorf("working copy path is required")
	}

	args := []string{"update"}

	// Revision
	if updateParams.Revision != "" {
		args = append(args, "-r", updateParams.Revision)
	}

	// Depth
	if updateParams.Depth != "" {
		args = append(args, "--depth", updateParams.Depth)
	}

	args = append(args, updateParams.Path)

	result, err := p.runSVNCommand(args, updateParams.Auth, updateParams.Env, updateParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// status checks working copy status
func (p *SVN) status(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var statusParams StatusArgs
	if err := sonic.Unmarshal(params, &statusParams); err != nil {
		return nil, fmt.Errorf("failed to parse status params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if statusParams.Path == "" {
					statusParams.Path = workspace
				}
			}
		}
	}

	if statusParams.Path == "" {
		return nil, fmt.Errorf("working copy path is required")
	}

	args := []string{"status"}
	if statusParams.Verbose {
		args = append(args, "-v")
	}
	if statusParams.ShowUpdates {
		args = append(args, "-u")
	}
	args = append(args, statusParams.Path)

	result, err := p.runSVNCommand(args, nil, statusParams.Env, statusParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// log views commit history
func (p *SVN) log(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var logParams LogArgs
	if err := sonic.Unmarshal(params, &logParams); err != nil {
		return nil, fmt.Errorf("failed to parse log params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if logParams.Path == "" {
					logParams.Path = workspace
				}
			}
		}
	}

	if logParams.Path == "" {
		return nil, fmt.Errorf("path or URL is required")
	}

	args := []string{"log"}
	if logParams.Verbose {
		args = append(args, "-v")
	}
	if logParams.Revision != "" {
		args = append(args, "-r", logParams.Revision)
	}
	if logParams.Limit > 0 {
		args = append(args, "-l", fmt.Sprintf("%d", logParams.Limit))
	}
	args = append(args, logParams.Path)

	result, err := p.runSVNCommand(args, logParams.Auth, logParams.Env, "")
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// info views repository information
func (p *SVN) info(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var infoParams InfoArgs
	if err := sonic.Unmarshal(params, &infoParams); err != nil {
		return nil, fmt.Errorf("failed to parse info params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if infoParams.Path == "" {
					infoParams.Path = workspace
				}
			}
		}
	}

	if infoParams.Path == "" {
		return nil, fmt.Errorf("path or URL is required")
	}

	args := []string{"info"}
	if infoParams.Revision != "" {
		args = append(args, "-r", infoParams.Revision)
	}
	args = append(args, infoParams.Path)

	result, err := p.runSVNCommand(args, infoParams.Auth, infoParams.Env, "")
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// list lists directory contents
func (p *SVN) list(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var listParams ListArgs
	if err := sonic.Unmarshal(params, &listParams); err != nil {
		return nil, fmt.Errorf("failed to parse list params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if listParams.Path == "" {
					listParams.Path = workspace
				}
			}
		}
	}

	if listParams.Path == "" {
		return nil, fmt.Errorf("path or URL is required")
	}

	args := []string{"list"}
	if listParams.Recursive {
		args = append(args, "-R")
	}
	if listParams.Revision != "" {
		args = append(args, "-r", listParams.Revision)
	}
	args = append(args, listParams.Path)

	result, err := p.runSVNCommand(args, listParams.Auth, listParams.Env, "")
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// runSVNCommand executes an svn command
func (p *SVN) runSVNCommand(args []string, auth map[string]string, env map[string]string, workDir string) (map[string]any, error) {
	ctx := context.Background()
	timeout := time.Duration(p.cfg.Timeout) * time.Second

	if p.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, p.cfg.SVNPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	} else if p.cfg.WorkDir != "" {
		cmd.Dir = p.cfg.WorkDir
	}

	// Set environment variables
	cmd.Env = os.Environ()

	// Configure authentication
	username := p.cfg.Username
	password := p.cfg.Password
	if auth != nil {
		if u, ok := auth["username"]; ok && u != "" {
			username = u
		}
		if pw, ok := auth["password"]; ok && pw != "" {
			password = pw
		}
	}

	// Add authentication arguments
	if username != "" {
		cmd.Args = append(cmd.Args, "--username", username)
	}
	if password != "" {
		cmd.Args = append(cmd.Args, "--password", password)
	}

	// Non-interactive mode
	if p.cfg.NonInteractive {
		cmd.Args = append(cmd.Args, "--non-interactive")
	}

	// Trust server certificate
	if p.cfg.TrustServerCert {
		cmd.Args = append(cmd.Args, "--trust-server-cert")
	}

	// Add custom environment variables
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	duration := time.Since(startTime)

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
			// For non-exit errors (e.g., path errors), set exit_code to -1
			result["exit_code"] = -1
		}
	} else {
		result["exit_code"] = 0
	}

	return result, nil
}

// init registers the plugin
func init() {
	plugin.Register(NewSVN())
}
