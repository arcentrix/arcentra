// Copyright 2026 Arcentra Authors.
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

package outbox

import (
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/outbox"
)

// ProvideExecutorManager creates an executor.Manager with ShellExecutor registered and
// EventPublisher set to OutboxPublisher (when outbox is non-nil), so step events go to the local outbox.
func ProvideExecutorManager(o *outbox.Outbox) *executor.Manager {
	m := executor.NewExecutorManager()
	m.Register(executor.NewShellExecutor())
	if o != nil {
		m.SetEventPublisher(NewPublisher(o), executor.EventEmitterConfig{})
	}
	return m
}

// ProvideOutbox creates an Outbox from agent config and gRPC client.
// Returns (nil, nil) when agent ID is not set (outbox disabled).
func ProvideOutbox(ac *config.AgentConfig, grpcClient *grpc.ClientWrapper) (*outbox.Outbox, error) {
	if ac == nil || ac.Agent.ID == "" {
		return nil, nil
	}
	cfg := ConfigFromAgentConfig(ac)
	sender := service.NewGatewaySenderFromWrapper(grpcClient, ac.Agent.ID, "")
	return outbox.NewOutbox(cfg, sender)
}

// ConfigFromAgentConfig builds outbox.Config from agent config.
// AgentID is taken from ac.Agent.ID; WALDir is 工作目录/data/wal (workspaceDir/data/wal, or "./data/wal" when workspaceDir is empty).
func ConfigFromAgentConfig(ac *config.AgentConfig) outbox.Config {
	workDir := ac.Agent.WorkspaceDir
	if workDir == "" {
		workDir = "."
	}
	cfg := outbox.Config{
		AgentID:    ac.Agent.ID,
		PipelineID: "",
		WALDir:     workDir + "/data/wal",
	}
	if ac.Outbox.SendIntervalMs > 0 {
		cfg.SendInterval = time.Duration(ac.Outbox.SendIntervalMs) * time.Millisecond
	}
	if ac.Outbox.SendBatchSize > 0 {
		cfg.SendBatchSize = ac.Outbox.SendBatchSize
	}
	if ac.Outbox.MaxDiskUsageMB > 0 {
		cfg.MaxDiskUsageMB = ac.Outbox.MaxDiskUsageMB
	}
	return cfg
}
