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
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
)

func TestContainerdSandbox_Create(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := log.Logger{Log: log.GetLogger()}
	config := &ContainerdConfig{
		UnixSocket:   "/run/containerd/containerd.sock",
		Namespace:    "arcentra-test",
		DefaultImage: "alpine:latest",
		NetworkMode:  "bridge",
		Resources: &Resources{
			CPU:    "1",
			Memory: "512M",
		},
	}

	sb, err := NewContainerdSandbox(config, logger)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}
	defer func() { _ = sb.Close() }()

	ctx := context.Background()

	opts := &CreateOptions{
		Image:       "alpine:latest",
		Command:     []string{"sleep"},
		Args:        []string{"10"},
		NetworkMode: "bridge",
		Resources: &Resources{
			CPU:    "0.5",
			Memory: "256M",
		},
		Env: map[string]string{
			"TEST": "value",
		},
	}

	containerID, err := sb.Create(ctx, opts)
	if err != nil {
		t.Fatalf("failed to create container: %v", err)
	}

	if containerID == "" {
		t.Error("container ID is empty")
	}

	// Cleanup
	_ = sb.Remove(ctx, containerID)
}

func TestContainerdSandbox_Execute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := log.Logger{Log: log.GetLogger()}
	config := &ContainerdConfig{
		UnixSocket:   "/run/containerd/containerd.sock",
		Namespace:    "arcentra-test",
		DefaultImage: "alpine:latest",
		NetworkMode:  "bridge",
	}

	sb, err := NewContainerdSandbox(config, logger)
	if err != nil {
		t.Fatalf("failed to create sandbox: %v", err)
	}
	defer func() { _ = sb.Close() }()

	ctx := context.Background()

	// Create container
	opts := &CreateOptions{
		Image:   "alpine:latest",
		Command: []string{"sh"},
		Args:    []string{"-c", "echo 'Hello World'"},
	}

	containerID, err := sb.Create(ctx, opts)
	if err != nil {
		t.Fatalf("failed to create container: %v", err)
	}
	defer func() { _ = sb.Remove(ctx, containerID) }()

	// Execute command
	executeOpts := &ExecuteOptions{
		Timeout: 30 * time.Second,
	}

	result, err := sb.Execute(ctx, containerID, []string{"echo", "test"}, executeOpts)
	if err != nil {
		t.Fatalf("failed to execute command: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
		t.Errorf("stdout: %s", result.Stdout)
		t.Errorf("stderr: %s", result.Stderr)
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input  string
		quota  int64
		period uint64
	}{
		{"1", 100000, 100000},
		{"0.5", 50000, 100000},
		{"1000m", 10000000, 10000000},
		{"500m", 5000000, 10000000},
	}

	for _, tt := range tests {
		quota, period := parseCPU(tt.input)
		if quota != tt.quota || period != tt.period {
			t.Errorf("parseCPU(%s) = (%d, %d), want (%d, %d)",
				tt.input, quota, period, tt.quota, tt.period)
		}
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input  string
		output int64
	}{
		{"1G", 1024 * 1024 * 1024},
		{"512M", 512 * 1024 * 1024},
		{"1024K", 1024 * 1024},
		{"1024", 1024},
	}

	for _, tt := range tests {
		result := parseMemory(tt.input)
		if result != tt.output {
			t.Errorf("parseMemory(%s) = %d, want %d", tt.input, result, tt.output)
		}
	}
}
