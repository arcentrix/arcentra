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

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

// ContainerdSandbox implements Sandbox interface using containerd
type ContainerdSandbox struct {
	client     *containerd.Client
	namespace  string
	logger     log.Logger
	config     *ContainerdConfig
	containers map[string]containerd.Container
}

// ContainerdConfig containerd sandbox configuration
type ContainerdConfig struct {
	// UnixSocket is the containerd unix socket path
	UnixSocket string

	// Namespace is the containerd namespace
	Namespace string

	// DefaultImage is the default container image
	DefaultImage string

	// NetworkMode is the default network mode
	NetworkMode string

	// Resources are default resource limits
	Resources *Resources
}

// NewContainerdSandbox creates a new containerd sandbox instance
func NewContainerdSandbox(config *ContainerdConfig, logger log.Logger) (*ContainerdSandbox, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if config.UnixSocket == "" {
		config.UnixSocket = "/run/containerd/containerd.sock"
	}

	if config.Namespace == "" {
		config.Namespace = "arcentra"
	}

	client, err := containerd.New(config.UnixSocket)
	if err != nil {
		return nil, fmt.Errorf("connect to containerd: %w", err)
	}

	sb := &ContainerdSandbox{
		client:     client,
		namespace:  config.Namespace,
		logger:     logger,
		config:     config,
		containers: make(map[string]containerd.Container),
	}

	if logger.Log != nil {
		logger.Log.Infow("containerd sandbox initialized",
			"socket", config.UnixSocket,
			"namespace", config.Namespace)
	}

	return sb, nil
}

// Create creates a new sandbox container
func (s *ContainerdSandbox) Create(ctx context.Context, opts *CreateOptions) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("options are required")
	}

	image := opts.Image
	if image == "" {
		image = s.config.DefaultImage
	}
	if image == "" {
		return "", fmt.Errorf("image is required")
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	// Pull image if needed
	img, err := s.client.GetImage(ctx, image)
	if err != nil {
		if logger := s.logger.Log; logger != nil {
			logger.Debugw("pulling image", "image", image)
		}
		img, err = s.client.Pull(ctx, image, containerd.WithPullUnpack)
		if err != nil {
			return "", fmt.Errorf("pull image %s: %w", image, err)
		}
	}

	// Generate container ID
	containerID := generateContainerID()

	// Prepare container spec
	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(img),
		oci.WithProcessArgs(append(opts.Command, opts.Args...)...),
	}

	// Set environment variables
	if len(opts.Env) > 0 {
		env := make([]string, 0, len(opts.Env))
		for k, v := range opts.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		specOpts = append(specOpts, oci.WithEnv(env))
	}

	// Set working directory
	if opts.WorkingDir != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(opts.WorkingDir))
	}

	// Set hostname
	if opts.Hostname != "" {
		specOpts = append(specOpts, oci.WithHostname(opts.Hostname))
	}

	// Configure network
	networkMode := opts.NetworkMode
	if networkMode == "" {
		networkMode = s.config.NetworkMode
	}
	if networkMode == "" {
		networkMode = "bridge"
	}

	switch networkMode {
	case "none":
		// No network namespace - container will have no network interfaces
		specOpts = append(specOpts, func(_ context.Context, _ oci.Client, _ *containers.Container, spec *specs.Spec) error {
			if spec.Linux == nil {
				spec.Linux = &specs.Linux{}
			}
			spec.Linux.Namespaces = append(spec.Linux.Namespaces, specs.LinuxNamespace{
				Type: specs.NetworkNamespace,
				Path: "",
			})
			return nil
		})
	case "host":
		specOpts = append(specOpts, oci.WithHostNamespace(specs.NetworkNamespace), oci.WithHostHostsFile, oci.WithHostResolvconf)
	case "bridge":
		// Default bridge network (containerd default)
		// Additional network configuration can be added here
	default:
		return "", fmt.Errorf("unsupported network mode: %s", networkMode)
	}

	// Configure resources
	resources := opts.Resources
	if resources == nil {
		resources = s.config.Resources
	}
	if resources != nil {
		specOpts = append(specOpts, s.withResources(resources))
	}

	// Configure mounts
	if len(opts.Mounts) > 0 {
		mounts := make([]specs.Mount, 0, len(opts.Mounts))
		for _, m := range opts.Mounts {
			mountType := m.Type
			if mountType == "" {
				mountType = "bind"
			}
			mounts = append(mounts, specs.Mount{
				Source:      m.Source,
				Destination: m.Target,
				Type:        mountType,
				Options:     s.getMountOptions(m.ReadOnly),
			})
		}
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}

	// Create container
	container, err := s.client.NewContainer(ctx, containerID,
		containerd.WithImage(img),
		containerd.WithNewSnapshot(containerID+"-snapshot", img),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	s.containers[containerID] = container

	if s.logger.Log != nil {
		s.logger.Log.Debugw("container created",
			"container_id", containerID,
			"image", image)
	}

	return containerID, nil
}

// Start starts a sandbox container
func (s *ContainerdSandbox) Start(ctx context.Context, containerID string) error {
	container, err := s.getContainer(containerID)
	if err != nil {
		return err
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		return fmt.Errorf("start task: %w", err)
	}

	if s.logger.Log != nil {
		s.logger.Log.Debugw("container started", "container_id", containerID)
	}

	return nil
}

// Execute executes a command in the sandbox container
func (s *ContainerdSandbox) Execute(ctx context.Context, containerID string, cmd []string, opts *ExecuteOptions) (*ExecuteResult, error) {
	result := &ExecuteResult{
		StartTime: time.Now(),
	}

	if len(cmd) == 0 {
		result.ExitCode = -1
		result.Stderr = "command is required"
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("command is required")
	}

	container, err := s.getContainer(containerID)
	if err != nil {
		result.ExitCode = -1
		result.Stderr = err.Error()
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, err
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	// Set execution timeout
	if opts != nil && opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Prepare task options
	var stdin io.Reader
	var stdout, stderr io.Writer
	if opts != nil {
		stdin = opts.Stdin
		stdout = opts.Stdout
		stderr = opts.Stderr
	}
	taskOpts := []cio.Opt{
		cio.WithStreams(stdin, stdout, stderr),
	}
	if opts != nil && opts.TTY {
		taskOpts = append(taskOpts, cio.WithTerminal)
	}

	// Create task
	task, err := container.NewTask(ctx, cio.NewCreator(taskOpts...))
	if err != nil {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf("create task: %v", err)
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, err
	}
	defer task.Delete(ctx)

	// Start task
	if err := task.Start(ctx); err != nil {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf("start task: %v", err)
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, err
	}

	// Wait for task to complete
	statusC, err := task.Wait(ctx)
	if err != nil {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf("wait task: %v", err)
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, err
	}

	// Get exit status
	status := <-statusC
	result.ExitCode = int32(status.ExitCode())
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	if s.logger.Log != nil {
		s.logger.Log.Debugw("command executed",
			"container_id", containerID,
			"command", strings.Join(cmd, " "),
			"exit_code", result.ExitCode,
			"duration", result.Duration)
	}

	return result, nil
}

// Stop stops a running sandbox container
func (s *ContainerdSandbox) Stop(ctx context.Context, containerID string, timeout time.Duration) error {
	container, err := s.getContainer(containerID)
	if err != nil {
		return err
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	task, err := container.Task(ctx, nil)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if err := task.Kill(ctx, unix.SIGTERM); err != nil {
		return fmt.Errorf("kill task: %w", err)
	}

	// Wait for task to exit with timeout
	done := make(chan error, 1)
	safe.Go(func() {
		_, err := task.Wait(ctx)
		done <- err
	})

	select {
	case err := <-done:
		if err != nil {
			// Force kill if graceful shutdown failed
			_ = task.Kill(ctx, unix.SIGKILL)
		}
		return err
	case <-time.After(timeout):
		// Force kill on timeout
		_ = task.Kill(ctx, unix.SIGKILL)
		return fmt.Errorf("stop timeout after %v", timeout)
	}
}

// Remove removes a sandbox container
func (s *ContainerdSandbox) Remove(ctx context.Context, containerID string) error {
	container, err := s.getContainer(containerID)
	if err != nil {
		return err
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	// Delete task if exists
	task, err := container.Task(ctx, nil)
	if err == nil {
		_, _ = task.Delete(ctx)
	}

	// Delete container
	if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("delete container: %w", err)
	}

	delete(s.containers, containerID)

	if s.logger.Log != nil {
		s.logger.Log.Debugw("container removed", "container_id", containerID)
	}

	return nil
}

// GetLogs retrieves logs from a sandbox container
func (s *ContainerdSandbox) GetLogs(ctx context.Context, containerID string, opts *LogOptions) (io.ReadCloser, error) {
	container, err := s.getContainer(containerID)
	if err != nil {
		return nil, err
	}

	ctx = namespaces.WithNamespace(ctx, s.namespace)

	// Get task to verify container is running
	_, err = container.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	// Get log file path
	// Note: containerd stores logs in different locations depending on configuration
	// This is a simplified implementation. In production, you should use containerd's log API
	logPath := fmt.Sprintf("/var/log/containers/%s.log", containerID)

	file, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	return file, nil
}

// Cleanup cleans up resources
func (s *ContainerdSandbox) Cleanup(ctx context.Context) error {
	ctx = namespaces.WithNamespace(ctx, s.namespace)

	for containerID := range s.containers {
		// Try to stop and remove container
		_ = s.Stop(ctx, containerID, 5*time.Second)
		_ = s.Remove(ctx, containerID)
	}

	s.containers = make(map[string]containerd.Container)

	if s.logger.Log != nil {
		s.logger.Log.Infow("sandbox cleanup completed")
	}

	return nil
}

// Close closes the sandbox client connection
func (s *ContainerdSandbox) Close() error {
	if err := s.Cleanup(context.Background()); err != nil {
		if s.logger.Log != nil {
			s.logger.Log.Warnw("cleanup failed during close", "error", err)
		}
	}

	if s.client != nil {
		return s.client.Close()
	}

	return nil
}

// getContainer gets a container by ID
func (s *ContainerdSandbox) getContainer(containerID string) (containerd.Container, error) {
	container, ok := s.containers[containerID]
	if !ok {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}
	return container, nil
}

// withResources configures resource limits
func (s *ContainerdSandbox) withResources(resources *Resources) oci.SpecOpts {
	return func(ctx context.Context, client oci.Client, c *containers.Container, spec *specs.Spec) error {
		if spec.Linux == nil {
			spec.Linux = &specs.Linux{}
		}
		if spec.Linux.Resources == nil {
			spec.Linux.Resources = &specs.LinuxResources{}
		}

		// CPU limits
		if resources.CPU != "" {
			quota, period := parseCPU(resources.CPU)
			if quota > 0 && period > 0 {
				if spec.Linux.Resources.CPU == nil {
					spec.Linux.Resources.CPU = &specs.LinuxCPU{}
				}
				spec.Linux.Resources.CPU.Quota = &quota
				spec.Linux.Resources.CPU.Period = &period
			}
		}

		if resources.CPUShares > 0 {
			if spec.Linux.Resources.CPU == nil {
				spec.Linux.Resources.CPU = &specs.LinuxCPU{}
			}
			shares := uint64(resources.CPUShares)
			spec.Linux.Resources.CPU.Shares = &shares
		}

		// Memory limits
		if resources.Memory != "" {
			memory := parseMemory(resources.Memory)
			if memory > 0 {
				if spec.Linux.Resources.Memory == nil {
					spec.Linux.Resources.Memory = &specs.LinuxMemory{}
				}
				spec.Linux.Resources.Memory.Limit = &memory
			}
		}

		if resources.MemoryReservation != "" {
			reservation := parseMemory(resources.MemoryReservation)
			if reservation > 0 {
				if spec.Linux.Resources.Memory == nil {
					spec.Linux.Resources.Memory = &specs.LinuxMemory{}
				}
				spec.Linux.Resources.Memory.Reservation = &reservation
			}
		}

		return nil
	}
}

// getMountOptions returns mount options based on read-only flag
func (s *ContainerdSandbox) getMountOptions(readOnly bool) []string {
	if readOnly {
		return []string{"ro"}
	}
	return []string{"rw"}
}

// parseCPU parses CPU string (e.g., "1", "0.5", "1000m") to quota and period
func parseCPU(cpu string) (int64, uint64) {
	cpu = strings.TrimSpace(cpu)

	// Handle millicores (e.g., "1000m")
	if strings.HasSuffix(cpu, "m") {
		millicores := strings.TrimSuffix(cpu, "m")
		var m int64
		fmt.Sscanf(millicores, "%d", &m)
		return m * 10000, 10000000 // quota = millicores * 10000, period = 10000000 (10ms)
	}

	// Handle decimal (e.g., "0.5", "1.5")
	var f float64
	if _, err := fmt.Sscanf(cpu, "%f", &f); err == nil {
		return int64(f * 100000), 100000 // quota = cores * 100000, period = 100000 (1ms)
	}

	// Handle integer (e.g., "1", "2")
	var i int64
	if _, err := fmt.Sscanf(cpu, "%d", &i); err == nil {
		return i * 100000, 100000
	}

	return 0, 0
}

// parseMemory parses memory string (e.g., "1G", "512M", "1024m") to bytes
func parseMemory(memory string) int64 {
	memory = strings.TrimSpace(memory)
	memory = strings.ToUpper(memory)

	var size int64
	var unit string

	if strings.HasSuffix(memory, "G") {
		fmt.Sscanf(memory, "%d%s", &size, &unit)
		return size * 1024 * 1024 * 1024
	}
	if strings.HasSuffix(memory, "M") {
		fmt.Sscanf(memory, "%d%s", &size, &unit)
		return size * 1024 * 1024
	}
	if strings.HasSuffix(memory, "K") {
		fmt.Sscanf(memory, "%d%s", &size, &unit)
		return size * 1024
	}

	// No unit, assume bytes
	fmt.Sscanf(memory, "%d", &size)
	return size
}

// generateContainerID generates a unique container ID
func generateContainerID() string {
	return fmt.Sprintf("arcentra-%d", time.Now().UnixNano())
}
