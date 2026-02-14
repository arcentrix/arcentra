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

package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
)

// WorkspaceManager manages workspace directory structure for pipeline execution
// Directory structure: root/name/build-id/
// - root: system parameter (cached in redis)
// - name: pipeline name
// - build-id: unique build identifier
type WorkspaceManager struct {
	rootPath string
	logger   log.Logger
}

// NewWorkspaceManager creates a new workspace manager
func NewWorkspaceManager(rootPath string, logger log.Logger) *WorkspaceManager {
	return &WorkspaceManager{
		rootPath: rootPath,
		logger:   logger,
	}
}

// WorkspacePaths defines the directory structure for a pipeline workspace
type WorkspacePaths struct {
	// Root workspace path (system parameter)
	Root string
	// Pipeline name directory (root/name)
	Name string
	// Build ID directory (root/name/build-id)
	BuildID string
	// Job workspace directory (root/name/build-id/job)
	Job string
	// Step workspace directory (root/name/build-id/job/step)
	Step string
	// Artifacts directory
	Artifacts string
	// Logs directory
	Logs string
	// Cache directory
	Cache string
	// Temp directory
	Temp string
}

// CreatePipelineWorkspace creates workspace directory structure for a pipeline
// Structure: root/name/distributed-id/
func (wm *WorkspaceManager) CreatePipelineWorkspace(name, buildID string) (*WorkspacePaths, error) {
	buildPath := filepath.Join(wm.rootPath, name, buildID)

	paths := &WorkspacePaths{
		Root:      wm.rootPath,
		Name:      filepath.Join(wm.rootPath, name),
		BuildID:   buildPath,
		Artifacts: filepath.Join(buildPath, "artifacts"),
		Logs:      filepath.Join(buildPath, "logs"),
		Cache:     filepath.Join(buildPath, "cache"),
		Temp:      filepath.Join(buildPath, "tmp"),
	}

	// Create all directories
	dirs := []string{
		paths.Name,
		paths.BuildID,
		paths.Artifacts,
		paths.Logs,
		paths.Cache,
		paths.Temp,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create pipeline workspace directory %s: %w", dir, err)
		}
	}

	if wm.logger.Log != nil {
		wm.logger.Log.Debugw("created pipeline workspace", "path", buildPath, "name", name, "build_id", buildID)
	}

	return paths, nil
}

// CleanupWorkspace removes workspace directory and all its contents
func (wm *WorkspaceManager) CleanupWorkspace(path string) error {
	if path == "" {
		return nil
	}

	// Check if path is within root to prevent accidental deletion
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	absRoot, err := filepath.Abs(wm.rootPath)
	if err != nil {
		return fmt.Errorf("get absolute root path: %w", err)
	}

	// Ensure path is within root
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == "../" {
		return fmt.Errorf("path %s is outside workspace root %s", path, wm.rootPath)
	}

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("cleanup workspace %s: %w", path, err)
	}

	if wm.logger.Log != nil {
		wm.logger.Log.Debugw("cleaned up workspace", "path", path)
	}

	return nil
}

// CleanupWorkspaces removes workspaces older than specified duration
// Cleans up build-id directories under root/name/
func (wm *WorkspaceManager) CleanupWorkspaces(name string, maxAge time.Duration) error {
	namePath := filepath.Join(wm.rootPath, name)

	entries, err := os.ReadDir(namePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read name directory: %w", err)
	}

	now := time.Now()
	cleaned := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		buildIDPath := filepath.Join(namePath, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		age := now.Sub(info.ModTime())
		if age > maxAge {
			if err := wm.CleanupWorkspace(buildIDPath); err != nil {
				if wm.logger.Log != nil {
					wm.logger.Log.Warnw("failed to cleanup old workspace", "path", buildIDPath, "error", err)
				}
				continue
			}
			cleaned++
		}
	}

	if wm.logger.Log != nil && cleaned > 0 {
		wm.logger.Log.Infow("cleaned up old workspaces", "count", cleaned, "name", name)
	}

	return nil
}

// GetWorkspaceSize returns the total size of workspace directory in bytes
func (wm *WorkspaceManager) GetWorkspaceSize(path string) (int64, error) {
	var size int64

	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})

	return size, err
}

// EnsureWorkspace ensures workspace directory exists, creates if not
func (wm *WorkspaceManager) EnsureWorkspace(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("ensure workspace exists: %w", err)
		}
	}
	return nil
}
