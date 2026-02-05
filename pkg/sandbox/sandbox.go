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

package sandbox

import (
	"context"
	"io"
	"time"
)

// Sandbox defines the interface for sandbox execution environments
// Sandbox provides isolated execution environment for running tasks
type Sandbox interface {
	// Create creates a new sandbox container
	// Returns container ID and error
	Create(ctx context.Context, opts *CreateOptions) (string, error)

	// Start starts a sandbox container
	Start(ctx context.Context, containerID string) error

	// Execute executes a command in the sandbox container
	// Returns execution result including exit code, stdout, stderr
	Execute(ctx context.Context, containerID string, cmd []string, opts *ExecuteOptions) (*ExecuteResult, error)

	// Stop stops a running sandbox container
	Stop(ctx context.Context, containerID string, timeout time.Duration) error

	// Remove removes a sandbox container
	Remove(ctx context.Context, containerID string) error

	// GetLogs retrieves logs from a sandbox container
	GetLogs(ctx context.Context, containerID string, opts *LogOptions) (io.ReadCloser, error)

	// Cleanup cleans up resources (containers, images, etc.)
	Cleanup(ctx context.Context) error

	// Close closes the sandbox client connection
	Close() error
}

// CreateOptions options for creating a sandbox container
type CreateOptions struct {
	// Image is the container image to use
	Image string

	// Command is the command to run in the container
	Command []string

	// Args are the arguments for the command
	Args []string

	// Env are environment variables
	Env map[string]string

	// WorkingDir is the working directory in the container
	WorkingDir string

	// NetworkMode is the network mode (bridge, host, none)
	NetworkMode string

	// Resources are resource limits
	Resources *Resources

	// Mounts are volume mounts
	Mounts []Mount

	// Labels are container labels
	Labels map[string]string

	// Hostname is the container hostname
	Hostname string

	// Privileged enables privileged mode
	Privileged bool
}

// Resources defines resource limits for a container
type Resources struct {
	// CPU is CPU limit (e.g., "1", "0.5", "1000m")
	CPU string

	// Memory is memory limit (e.g., "1G", "512M", "1024m")
	Memory string

	// CPUShares is CPU shares (relative weight)
	CPUShares int64

	// MemoryReservation is memory reservation
	MemoryReservation string
}

// Mount defines a volume mount
type Mount struct {
	// Source is the source path on the host
	Source string

	// Target is the target path in the container
	Target string

	// Type is the mount type (bind, volume, tmpfs)
	Type string

	// ReadOnly indicates if the mount is read-only
	ReadOnly bool
}

// ExecuteOptions options for executing a command in a container
type ExecuteOptions struct {
	// Env are additional environment variables
	Env map[string]string

	// WorkingDir is the working directory
	WorkingDir string

	// User is the user to run as (e.g., "root", "1000:1000")
	User string

	// TTY enables TTY mode
	TTY bool

	// Stdin is the stdin reader
	Stdin io.Reader

	// Stdout is the stdout writer
	Stdout io.Writer

	// Stderr is the stderr writer
	Stderr io.Writer

	// Timeout is the execution timeout
	Timeout time.Duration
}

// ExecuteResult result of executing a command
type ExecuteResult struct {
	// ExitCode is the exit code
	ExitCode int32

	// Stdout is the stdout output
	Stdout string

	// Stderr is the stderr output
	Stderr string

	// StartTime is when execution started
	StartTime time.Time

	// EndTime is when execution ended
	EndTime time.Time

	// Duration is the execution duration
	Duration time.Duration
}

// LogOptions options for retrieving logs
type LogOptions struct {
	// Follow follows log output
	Follow bool

	// Tail is the number of lines to show from the end
	Tail int

	// Since is the timestamp to show logs since
	Since time.Time

	// Until is the timestamp to show logs until
	Until time.Time

	// Timestamps includes timestamps in log output
	Timestamps bool
}
