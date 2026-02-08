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

package git

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

// GitConfig is the plugin's configuration structure
type GitConfig struct {
	// Git executable path (default: git)
	GitPath string `json:"gitPath"`
	// Default timeout in seconds (0 means no timeout)
	Timeout int `json:"timeout"`
	// Default working directory
	WorkDir string `json:"workDir"`
	// Default user name for commits
	UserName string `json:"userName"`
	// Default user email for commits
	UserEmail string `json:"userEmail"`
	// Whether to use shallow clone by default
	Shallow bool `json:"shallow"`
	// Depth for shallow clone
	Depth int `json:"depth"`
}

// CloneArgs contains arguments for cloning a repository
type CloneArgs struct {
	Repo    string            `json:"repo"`    // Repository URL
	Branch  string            `json:"branch"`  // Branch to clone (optional)
	Tag     string            `json:"tag"`     // Tag to clone (optional)
	Commit  string            `json:"commit"`  // Commit SHA to clone (optional)
	Path    string            `json:"path"`    // Destination path (optional)
	Shallow *bool             `json:"shallow"` // Override shallow clone setting
	Depth   *int              `json:"depth"`   // Override depth setting
	Auth    map[string]string `json:"auth"`    // Authentication (username, password, token, ssh_key)
	Env     map[string]string `json:"env"`     // Additional environment variables
}

// CheckoutArgs contains arguments for checking out a branch/tag/commit
type CheckoutArgs struct {
	Ref    string            `json:"ref"`    // Branch, tag, or commit SHA
	Path   string            `json:"path"`   // Repository path (required)
	Force  bool              `json:"force"`  // Force checkout
	Create bool              `json:"create"` // Create branch if not exists
	Auth   map[string]string `json:"auth"`   // Authentication (optional, only needed for remote ops)
	Env    map[string]string `json:"env"`    // Additional environment variables
}

// PullArgs contains arguments for pulling updates
type PullArgs struct {
	Path   string            `json:"path"`   // Repository path (required)
	Branch string            `json:"branch"` // Branch to pull (optional, defaults to current)
	Remote string            `json:"remote"` // Remote name (optional, defaults to origin)
	Rebase bool              `json:"rebase"` // Use rebase instead of merge
	Auth   map[string]string `json:"auth"`   // Authentication (username, password, token, ssh_key)
	Env    map[string]string `json:"env"`    // Additional environment variables
}

// StatusArgs contains arguments for checking repository status
type StatusArgs struct {
	Path  string            `json:"path"`  // Repository path (required)
	Short bool              `json:"short"` // Short format output
	Env   map[string]string `json:"env"`   // Additional environment variables
}

// LogArgs contains arguments for viewing commit history
type LogArgs struct {
	Path   string            `json:"path"`   // Repository path (required)
	Ref    string            `json:"ref"`    // Branch/tag/commit to show log from (optional)
	Limit  int               `json:"limit"`  // Limit number of commits (optional)
	Since  string            `json:"since"`  // Show commits since date (optional)
	Until  string            `json:"until"`  // Show commits until date (optional)
	Author string            `json:"author"` // Filter by author (optional)
	Env    map[string]string `json:"env"`    // Additional environment variables
}

// BranchArgs contains arguments for listing branches
type BranchArgs struct {
	Path   string            `json:"path"`   // Repository path (required)
	Remote bool              `json:"remote"` // List remote branches
	All    bool              `json:"all"`    // List all branches
	Env    map[string]string `json:"env"`    // Additional environment variables
}

// TagArgs contains arguments for listing tags
type TagArgs struct {
	Path string            `json:"path"` // Repository path (required)
	Sort string            `json:"sort"` // Sort order (version, date, etc.)
	Env  map[string]string `json:"env"`  // Additional environment variables
}

// Branch represents a structured branch info.
type Branch struct {
	Name     string `json:"name"`
	CommitId string `json:"commitId"`
	IsRemote bool   `json:"isRemote"`
	IsHead   bool   `json:"isHead"`
	Upstream string `json:"upstream,omitempty"`
}

// Tag represents a structured tag info.
type Tag struct {
	Name     string `json:"name"`
	CommitId string `json:"commitId"`
}

// Commit represents a structured commit info.
type Commit struct {
	CommitId    string    `json:"commitId"`
	AuthorName  string    `json:"authorName"`
	AuthorEmail string    `json:"authorEmail"`
	CommittedAt time.Time `json:"committedAt"`
	Message     string    `json:"message"`
}

// Git implements the git plugin
type Git struct {
	*plugin.PluginBase
	name        string
	description string
	version     string
	cfg         GitConfig
}

// Action definitions
var (
	actions = map[string]string{
		"clone":         "Clone a git repository",
		"checkout":      "Checkout a branch, tag, or commit",
		"pull":          "Pull latest changes from remote",
		"status":        "Check repository status",
		"log":           "View commit history",
		"branch":        "List branches",
		"tag":           "List tags",
		"branches.list": "List branches (structured)",
		"tags.list":     "List tags (structured)",
		"commits.list":  "List commits (structured)",
		"commit.get":    "Get a commit (structured)",
	}
)

// NewGit creates a new git plugin instance
func NewGit() *Git {
	p := &Git{
		PluginBase:  plugin.NewPluginBase(),
		name:        "git",
		description: "Git version control plugin for repository operations",
		version:     "1.0.0",
		cfg: GitConfig{
			GitPath: "git",
			Timeout: 300, // 5 minutes default
			Shallow: false,
			Depth:   1,
		},
	}

	// Register actions using Action Registry
	p.registerActions()
	return p
}

// registerActions registers all actions for this plugin
func (p *Git) registerActions() {
	// Register "clone" action
	if err := p.Registry().RegisterFunc("clone", actions["clone"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.clone(params, opts)
	}); err != nil {
		return
	}

	// Register "checkout" action
	if err := p.Registry().RegisterFunc("checkout", actions["checkout"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.checkout(params, opts)
	}); err != nil {
		return
	}

	// Register "pull" action
	if err := p.Registry().RegisterFunc("pull", actions["pull"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.pull(params, opts)
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

	// Register "branch" action
	if err := p.Registry().RegisterFunc("branch", actions["branch"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.branch(params, opts)
	}); err != nil {
		return
	}

	// Register "tag" action
	if err := p.Registry().RegisterFunc("tag", actions["tag"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.tag(params, opts)
	}); err != nil {
		return
	}

	// Register structured actions
	if err := p.Registry().RegisterFunc("branches.list", actions["branches.list"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.branchesList(params, opts)
	}); err != nil {
		return
	}
	if err := p.Registry().RegisterFunc("tags.list", actions["tags.list"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.tagsList(params, opts)
	}); err != nil {
		return
	}
	if err := p.Registry().RegisterFunc("commits.list", actions["commits.list"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.commitsList(params, opts)
	}); err != nil {
		return
	}
	if err := p.Registry().RegisterFunc("commit.get", actions["commit.get"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		return p.commitGet(params, opts)
	}); err != nil {
		return
	}
}

// Name returns the plugin name
func (p *Git) Name() string {
	return p.name
}

// Description returns the plugin description
func (p *Git) Description() string {
	return p.description
}

// Version returns the plugin version
func (p *Git) Version() string {
	return p.version
}

// Type returns the plugin type
func (p *Git) Type() plugin.PluginType {
	return plugin.TypeSource
}

// Author returns the plugin author
func (p *Git) Author() string {
	return "Arcentra Team"
}

// Repository returns the plugin repository
func (p *Git) Repository() string {
	return "https://github.com/arcentrix/arcentra"
}

// Init initializes the plugin
func (p *Git) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}
	}

	// Validate git path
	if p.cfg.GitPath == "" {
		p.cfg.GitPath = "git"
	}

	// Check if git exists
	if _, err := exec.LookPath(p.cfg.GitPath); err != nil {
		return fmt.Errorf("git not found: %s", p.cfg.GitPath)
	}

	log.Infow("git plugin initialized", "plugin", "git", "git_path", p.cfg.GitPath, "timeout", p.cfg.Timeout)
	return nil
}

// Cleanup cleans up the plugin
func (p *Git) Cleanup() error {
	log.Infow("git plugin cleanup completed", "plugin", "git")
	return nil
}

// Execute executes git operations using Action Registry
func (p *Git) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.PluginBase.Execute(action, params, opts)
}

// clone clones a git repository
func (p *Git) clone(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var cloneParams CloneArgs
	if err := sonic.Unmarshal(params, &cloneParams); err != nil {
		return nil, fmt.Errorf("failed to parse clone params: %w", err)
	}

	if cloneParams.Repo == "" {
		return nil, fmt.Errorf("repository URL is required")
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if cloneParams.Path == "" {
					cloneParams.Path = workspace
				}
			}
		}
	}

	// Determine destination path
	destPath := cloneParams.Path
	if destPath == "" {
		// Extract repo name from URL
		repoName := filepath.Base(strings.TrimSuffix(cloneParams.Repo, ".git"))
		destPath = repoName
	}

	// Build git clone command
	args := []string{"clone"}

	// Shallow clone
	shallow := p.cfg.Shallow
	if cloneParams.Shallow != nil {
		shallow = *cloneParams.Shallow
	}
	if shallow {
		depth := p.cfg.Depth
		if cloneParams.Depth != nil {
			depth = *cloneParams.Depth
		}
		if depth > 0 {
			args = append(args, "--depth", fmt.Sprintf("%d", depth))
		}
		args = append(args, "--shallow-submodules")
	}

	// Branch/Tag/Commit
	if cloneParams.Branch != "" {
		args = append(args, "--branch", cloneParams.Branch)
	} else if cloneParams.Tag != "" {
		args = append(args, "--branch", cloneParams.Tag)
	}

	args = append(args, cloneParams.Repo, destPath)

	// Execute clone
	result, err := p.runGitCommand(args, cloneParams.Auth, cloneParams.Env, "")
	if err != nil {
		return nil, fmt.Errorf("clone failed: %w", err)
	}
	if !result["success"].(bool) {
		errorMsg := p.redactSensitive(result["stderr"].(string), cloneParams.Auth)
		if errorMsg == "" {
			if errStr, ok := result["error"].(string); ok {
				errorMsg = p.redactSensitive(errStr, cloneParams.Auth)
			} else {
				errorMsg = "clone command failed"
			}
		}
		return nil, fmt.Errorf("clone failed: %s", errorMsg)
	}

	// If specific commit was requested, checkout it
	if cloneParams.Commit != "" {
		checkoutResult, err := p.runGitCommand([]string{"checkout", cloneParams.Commit}, nil, cloneParams.Env, destPath)
		if err != nil {
			return nil, fmt.Errorf("checkout commit failed: %w", err)
		}
		result["checkout"] = checkoutResult
	}

	result["path"] = destPath
	return sonic.Marshal(result)
}

// checkout checks out a branch, tag, or commit
func (p *Git) checkout(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var checkoutParams CheckoutArgs
	if err := sonic.Unmarshal(params, &checkoutParams); err != nil {
		return nil, fmt.Errorf("failed to parse checkout params: %w", err)
	}

	if checkoutParams.Ref == "" {
		return nil, fmt.Errorf("ref (branch/tag/commit) is required")
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

	if checkoutParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"checkout"}
	if checkoutParams.Force {
		args = append(args, "-f")
	}
	if checkoutParams.Create {
		args = append(args, "-b", checkoutParams.Ref)
	} else {
		args = append(args, checkoutParams.Ref)
	}

	result, err := p.runGitCommand(args, checkoutParams.Auth, checkoutParams.Env, checkoutParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// pull pulls latest changes from remote
func (p *Git) pull(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var pullParams PullArgs
	if err := sonic.Unmarshal(params, &pullParams); err != nil {
		return nil, fmt.Errorf("failed to parse pull params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if pullParams.Path == "" {
					pullParams.Path = workspace
				}
			}
		}
	}

	if pullParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"pull"}
	if pullParams.Rebase {
		args = append(args, "--rebase")
	}
	if pullParams.Remote != "" {
		args = append(args, pullParams.Remote)
	}
	if pullParams.Branch != "" {
		args = append(args, pullParams.Branch)
	}

	result, err := p.runGitCommand(args, pullParams.Auth, pullParams.Env, pullParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// status checks repository status
func (p *Git) status(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
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
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"status"}
	if statusParams.Short {
		args = append(args, "--short")
	}

	result, err := p.runGitCommand(args, nil, statusParams.Env, statusParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// log views commit history
func (p *Git) log(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
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
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"log", "--pretty=format:%H|%an|%ae|%ad|%s", "--date=iso"}
	if logParams.Limit > 0 {
		args = append(args, fmt.Sprintf("-%d", logParams.Limit))
	}
	if logParams.Since != "" {
		args = append(args, "--since", logParams.Since)
	}
	if logParams.Until != "" {
		args = append(args, "--until", logParams.Until)
	}
	if logParams.Author != "" {
		args = append(args, "--author", logParams.Author)
	}
	if logParams.Ref != "" {
		args = append(args, logParams.Ref)
	}

	result, err := p.runGitCommand(args, nil, logParams.Env, logParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// branch lists branches
func (p *Git) branch(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var branchParams BranchArgs
	if err := sonic.Unmarshal(params, &branchParams); err != nil {
		return nil, fmt.Errorf("failed to parse branch params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if branchParams.Path == "" {
					branchParams.Path = workspace
				}
			}
		}
	}

	if branchParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"branch"}
	if branchParams.All {
		args = append(args, "-a")
	} else if branchParams.Remote {
		args = append(args, "-r")
	}

	result, err := p.runGitCommand(args, nil, branchParams.Env, branchParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

// tag lists tags
func (p *Git) tag(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var tagParams TagArgs
	if err := sonic.Unmarshal(params, &tagParams); err != nil {
		return nil, fmt.Errorf("failed to parse tag params: %w", err)
	}

	// Parse opts for workspace
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if tagParams.Path == "" {
					tagParams.Path = workspace
				}
			}
		}
	}

	if tagParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"tag"}
	if tagParams.Sort != "" {
		args = append(args, "--sort", tagParams.Sort)
	}

	result, err := p.runGitCommand(args, nil, tagParams.Env, tagParams.Path)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

type commitGetArgs struct {
	Path     string            `json:"path"`
	CommitId string            `json:"commitId"`
	Env      map[string]string `json:"env"`
}

// branchesList lists branches in structured format.
func (p *Git) branchesList(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var branchParams BranchArgs
	if err := sonic.Unmarshal(params, &branchParams); err != nil {
		return nil, fmt.Errorf("failed to parse branches.list params: %w", err)
	}
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if branchParams.Path == "" {
					branchParams.Path = workspace
				}
			}
		}
	}
	if branchParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	refs := []string{"refs/heads"}
	if branchParams.All || branchParams.Remote {
		refs = append(refs, "refs/remotes")
	}

	branches := make([]Branch, 0)
	for _, ref := range refs {
		result, err := p.runGitCommand([]string{"for-each-ref", "--format=%(refname:short)%x1f%(objectname)%x1f%(HEAD)%x1f%(upstream:short)", ref}, nil, branchParams.Env, branchParams.Path)
		if err != nil {
			return nil, err
		}
		stdout, _ := result["stdout"].(string)
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Split(line, "\x1f")
			if len(parts) < 2 {
				continue
			}
			b := Branch{
				Name:     strings.TrimSpace(parts[0]),
				CommitId: strings.TrimSpace(parts[1]),
				IsRemote: ref == "refs/remotes",
			}
			if len(parts) >= 3 && strings.TrimSpace(parts[2]) == "*" {
				b.IsHead = true
			}
			if len(parts) >= 4 {
				b.Upstream = strings.TrimSpace(parts[3])
			}
			branches = append(branches, b)
		}
	}

	return sonic.Marshal(map[string]any{"branches": branches})
}

// tagsList lists tags in structured format.
func (p *Git) tagsList(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var tagParams TagArgs
	if err := sonic.Unmarshal(params, &tagParams); err != nil {
		return nil, fmt.Errorf("failed to parse tags.list params: %w", err)
	}
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if tagParams.Path == "" {
					tagParams.Path = workspace
				}
			}
		}
	}
	if tagParams.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"for-each-ref", "refs/tags", "--format=%(refname:short)%x1f%(objectname)%x1f%(peeled)"}
	if tagParams.Sort != "" {
		args = append(args, "--sort", tagParams.Sort)
	}
	result, err := p.runGitCommand(args, nil, tagParams.Env, tagParams.Path)
	if err != nil {
		return nil, err
	}
	stdout, _ := result["stdout"].(string)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	tags := make([]Tag, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 2 {
			continue
		}
		commitId := strings.TrimSpace(parts[1])
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			commitId = strings.TrimSpace(parts[2])
		}
		tags = append(tags, Tag{
			Name:     strings.TrimSpace(parts[0]),
			CommitId: commitId,
		})
	}
	return sonic.Marshal(map[string]any{"tags": tags})
}

// commitsList lists commits in structured format.
func (p *Git) commitsList(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var logParams LogArgs
	if err := sonic.Unmarshal(params, &logParams); err != nil {
		return nil, fmt.Errorf("failed to parse commits.list params: %w", err)
	}
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
		return nil, fmt.Errorf("repository path is required")
	}

	args := []string{"log", "--date=iso-strict", "--pretty=format:%H%x1f%an%x1f%ae%x1f%ad%x1f%s%x1e"}
	if logParams.Limit > 0 {
		args = append(args, fmt.Sprintf("-%d", logParams.Limit))
	}
	if logParams.Since != "" {
		args = append(args, "--since", logParams.Since)
	}
	if logParams.Until != "" {
		args = append(args, "--until", logParams.Until)
	}
	if logParams.Author != "" {
		args = append(args, "--author", logParams.Author)
	}
	if logParams.Ref != "" {
		args = append(args, logParams.Ref)
	}

	result, err := p.runGitCommand(args, nil, logParams.Env, logParams.Path)
	if err != nil {
		return nil, err
	}
	stdout, _ := result["stdout"].(string)
	commits := parseCommitRecords(stdout)
	return sonic.Marshal(map[string]any{"commits": commits})
}

// commitGet gets a single commit in structured format.
func (p *Git) commitGet(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var args commitGetArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse commit.get params: %w", err)
	}
	var optsMap map[string]any
	if len(opts) > 0 {
		if err := sonic.Unmarshal(opts, &optsMap); err == nil {
			if workspace, ok := optsMap["workspace"].(string); ok && workspace != "" {
				if args.Path == "" {
					args.Path = workspace
				}
			}
		}
	}
	if args.Path == "" {
		return nil, fmt.Errorf("repository path is required")
	}
	if args.CommitId == "" {
		return nil, fmt.Errorf("commitId is required")
	}

	result, err := p.runGitCommand([]string{"show", "-s", "--date=iso-strict", "--pretty=format:%H%x1f%an%x1f%ae%x1f%ad%x1f%s", args.CommitId}, nil, args.Env, args.Path)
	if err != nil {
		return nil, err
	}
	stdout, _ := result["stdout"].(string)
	commit, err := parseCommitLine(stdout)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(map[string]any{"commit": commit})
}

// runGitCommand executes a git command
func (p *Git) runGitCommand(args []string, auth map[string]string, env map[string]string, workDir string) (map[string]any, error) {
	ctx := context.Background()
	timeout := time.Duration(p.cfg.Timeout) * time.Second

	if p.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, p.cfg.GitPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	} else if p.cfg.WorkDir != "" {
		cmd.Dir = p.cfg.WorkDir
	}

	// Set environment variables
	cmd.Env = os.Environ()

	// Avoid interactive prompts in CI/daemon mode.
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")

	// Configure git user if provided
	if p.cfg.UserName != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_AUTHOR_NAME=%s", p.cfg.UserName))
		cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_COMMITTER_NAME=%s", p.cfg.UserName))
	}
	if p.cfg.UserEmail != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", p.cfg.UserEmail))
		cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", p.cfg.UserEmail))
	}

	// Add authentication (HTTPS via GIT_ASKPASS, SSH via key file + GIT_SSH_COMMAND).
	if auth != nil {
		if sshKey, ok := auth["ssh_key"]; ok && strings.TrimSpace(sshKey) != "" {
			keyFile, err := os.CreateTemp("", "arcentra_git_sshkey_*")
			if err != nil {
				return nil, fmt.Errorf("create ssh key temp file: %w", err)
			}
			keyPath := keyFile.Name()
			_ = keyFile.Close()
			if err := os.WriteFile(keyPath, []byte(sshKey), 0o600); err != nil {
				_ = os.Remove(keyPath)
				return nil, fmt.Errorf("write ssh key temp file: %w", err)
			}
			defer func() { _ = os.Remove(keyPath) }()
			cmd.Env = append(cmd.Env, fmt.Sprintf(`GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no`, keyPath))
		} else {
			username := strings.TrimSpace(auth["username"])
			password := strings.TrimSpace(auth["password"])
			token := strings.TrimSpace(auth["token"])
			if password == "" && token != "" {
				password = token
				if username == "" {
					username = "oauth2"
				}
			}
			if username != "" && password != "" {
				askpass, err := os.CreateTemp("", "arcentra_git_askpass_*")
				if err != nil {
					return nil, fmt.Errorf("create askpass temp file: %w", err)
				}
				askpassPath := askpass.Name()
				_ = askpass.Close()
				script := `#!/bin/sh
case "$1" in
  *Username*) echo "$GIT_USERNAME" ;;
  *Password*) echo "$GIT_PASSWORD" ;;
  *) echo "" ;;
esac
`
				if err := os.WriteFile(askpassPath, []byte(script), 0o700); err != nil {
					_ = os.Remove(askpassPath)
					return nil, fmt.Errorf("write askpass script: %w", err)
				}
				defer func() { _ = os.Remove(askpassPath) }()
				cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_ASKPASS=%s", askpassPath))
				cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_USERNAME=%s", username))
				cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_PASSWORD=%s", password))
			}
		}
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

func (p *Git) redactSensitive(s string, auth map[string]string) string {
	if s == "" || auth == nil {
		return s
	}
	out := s
	for _, k := range []string{"password", "token", "ssh_key"} {
		if v := strings.TrimSpace(auth[k]); v != "" {
			out = strings.ReplaceAll(out, v, "***")
		}
	}
	return out
}

func parseCommitRecords(stdout string) []Commit {
	records := strings.Split(stdout, "\x1e")
	commits := make([]Commit, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) < 5 {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[3]))
		if err != nil {
			continue
		}
		commits = append(commits, Commit{
			CommitId:    strings.TrimSpace(parts[0]),
			AuthorName:  strings.TrimSpace(parts[1]),
			AuthorEmail: strings.TrimSpace(parts[2]),
			CommittedAt: t,
			Message:     strings.TrimSpace(parts[4]),
		})
	}
	return commits
}

func parseCommitLine(stdout string) (Commit, error) {
	parts := strings.Split(strings.TrimSpace(stdout), "\x1f")
	if len(parts) < 5 {
		return Commit{}, fmt.Errorf("unexpected git output")
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[3]))
	if err != nil {
		return Commit{}, fmt.Errorf("parse commit time: %w", err)
	}
	return Commit{
		CommitId:    strings.TrimSpace(parts[0]),
		AuthorName:  strings.TrimSpace(parts[1]),
		AuthorEmail: strings.TrimSpace(parts[2]),
		CommittedAt: t,
		Message:     strings.TrimSpace(parts[4]),
	}, nil
}

// init registers the plugin
func init() {
	plugin.Register(NewGit())
}
