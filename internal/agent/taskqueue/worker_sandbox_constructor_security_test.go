// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
)

// TestOfflineConstructorRejectsUnsafeConfigBeforeRuntimeConnection exercises
// the real constructor after production policy acceptance. The synthetic CA is
// configured without starting a controller or daemon; no RPC is needed for the
// trust binding. Every expected error must precede the unavailable backend.
func TestOfflineConstructorRejectsUnsafeConfigBeforeRuntimeConnection(t *testing.T) {
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: controllerWorkerTLS(t).clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.ControllerIdentityBinding(); err != nil {
		t.Fatal("synthetic explicit TLS trust is not bound", err)
	}
	for _, test := range []struct {
		name, want string
		mutate     func(*config.AgentConfig)
	}{
		{"missing-namespace", "requires explicit", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Namespace = "" }},
		{"missing-journal", "requires explicit", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.JournalDir = "" }},
		{"missing-fifo", "requires explicit", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.FIFODir = "" }},
		{"missing-user", "requires explicit", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = "" }},
		{"missing-workspace", "clean absolute", func(c *config.AgentConfig) { c.Agent.WorkspaceDir = "" }},
		{"relative-workspace", "clean absolute", func(c *config.AgentConfig) { c.Agent.WorkspaceDir = "workspace" }},
		{"relative-journal", "clean absolute", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.JournalDir = "journal" }},
		{"relative-fifo", "clean absolute", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.FIFODir = "fifo" }},
		{"filesystem-root", "clean absolute", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.FIFODir = "/" }},
		{"fifo-equals-workspace", "must not overlap", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.FIFODir = c.Agent.WorkspaceDir }},
		{"fifo-inside-workspace", "must not overlap", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.FIFODir = filepath.Join(c.Agent.WorkspaceDir, "fifo")
		}},
		{"workspace-inside-fifo", "must not overlap", func(c *config.AgentConfig) {
			c.Agent.WorkspaceDir = filepath.Join(c.Agent.Sandbox.Containerd.FIFODir, "work")
		}},
		{"fifo-equals-journal", "must not overlap", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.FIFODir = c.Agent.Sandbox.Containerd.JournalDir
		}},
		{"fifo-inside-journal", "must not overlap", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.FIFODir = filepath.Join(c.Agent.Sandbox.Containerd.JournalDir, "fifo")
		}},
		{"journal-inside-fifo", "must not overlap", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.JournalDir = filepath.Join(c.Agent.Sandbox.Containerd.FIFODir, "journal")
		}},
		{"implicit-network", "network must explicitly be none", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Network = "" }},
		{"host-network", "network must explicitly be none", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Network = "host" }},
		{"legacy-image", "not legacy defaults", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Image = "alpine:latest" }},
		{"legacy-cpu", "not legacy defaults", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Resources.CPU = "1" }},
		{"legacy-memory", "not legacy defaults", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Resources.Memory = "64Mi" }},
		{"implicit-gid", "non-root UID:GID", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = "1000" }},
		{"named-user", "UID:GID must match", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = "agent:agent" }},
		{"root-uid", "UID:GID must match", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = fmt.Sprintf("0:%d", os.Getegid()) }},
		{"root-gid", "UID:GID must match", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = fmt.Sprintf("%d:0", os.Geteuid()) }},
		{"foreign-uid", "UID:GID must match", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.User = fmt.Sprintf("%d:%d", os.Geteuid()+1, os.Getegid())
		}},
		{"foreign-gid", "UID:GID must match", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.User = fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()+1)
		}},
		{"uid-overflow", "UID:GID must match", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.User = "4294967296:1000" }},
		{"unbounded-images", "image allowlist", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Policy.AllowedImages = nil }},
		{"mutable-image", "immutable image", func(c *config.AgentConfig) {
			c.Agent.Sandbox.Containerd.Policy.AllowedImages = []string{"alpine:latest"}
		}},
		{"unbounded-cpu", "CPU maximum", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Policy.MaxCPU = "" }},
		{"unbounded-memory", "memory maximum", func(c *config.AgentConfig) { c.Agent.Sandbox.Containerd.Policy.MaxMemory = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			conf, _, _ := offlineWorkerFixture(t)
			root := t.TempDir()
			conf.Agent.WorkspaceDir = filepath.Join(root, "work")
			cfg := &conf.Agent.Sandbox.Containerd
			cfg.Namespace, cfg.Network = "constructor-preflight", "none"
			cfg.JournalDir, cfg.FIFODir = filepath.Join(root, "journal"), filepath.Join(root, "fifo")
			cfg.User = fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
			cfg.UnixSocket = filepath.Join(root, "no-daemon.sock")
			for _, path := range []string{conf.Agent.WorkspaceDir, cfg.JournalDir, cfg.FIFODir} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			test.mutate(conf)
			runtime, err := newOfflineRuntime(context.Background(), conf, client)
			if runtime != nil || err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "connection failed") {
				t.Fatalf("configuration was not rejected before runtime connection: runtime=%v error=%v want=%q", runtime != nil, err, test.want)
			}
			for _, name := range []string{"work", "journal", "fifo"} {
				path := filepath.Join(root, name)
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("rejected constructor changed root permissions: %s %v", name, err)
				}
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejected constructor allocated runtime or journal state: %s %v", name, err)
				}
			}
		})
	}
}
