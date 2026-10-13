// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/nova"
)

func TestStartWorkerRejectsInvalidTransportBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct{ format, codec, field string }{
		{"protobuf", "protobuf", "messageFormat"},
		{"blob", "json", "messageFormat"},
		{"typo", "sonic", "messageFormat"},
		{"sonic", "protobuf", "messageCodec"},
		{"json", "blob", "messageCodec"},
		{"json", "typo", "messageCodec"},
	} {
		t.Run(tc.format+"/"+tc.codec, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "not-created")
			conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal", WorkspaceDir: workspace}, TaskQueue: nova.TaskQueueConfig{MessageFormat: tc.format, MessageCodec: tc.codec}}
			conf.MessageQueue.Kafka.BootstrapServers = "127.0.0.1:9092"
			queue, err := StartWorker(context.Background(), conf, nil, nil)
			if queue != nil || err == nil || !strings.Contains(err.Error(), "taskQueue."+tc.field) {
				t.Fatalf("invalid transport silently accepted or masked: queue=%v err=%v", queue, err)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatalf("invalid codec created workspace/outbox: %v", err)
			}
		})
	}
}

func TestStartWorkerPreservesUnconfiguredKafka(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal"}, TaskQueue: nova.TaskQueueConfig{MessageFormat: "protobuf", MessageCodec: "typo"}}
	queue, err := StartWorker(context.Background(), conf, nil, nil)
	if queue != nil || err != nil {
		t.Fatalf("unconfigured Kafka changed: queue=%v err=%v", queue, err)
	}
}

func TestStartWorkerPropagatesBrokerInitializationError(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "codec-test-agent", Mode: "baremetal", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: "codec-test.invalid:9090"}, TaskQueue: nova.TaskQueueConfig{MessageFormat: "json", MessageCodec: "sonic"}}
	conf.MessageQueue.Kafka.BootstrapServers = "127.0.0.1:9092"
	conf.MessageQueue.Kafka.SecurityProtocol = "CANARY_BROKER_SECRET_INVALID_PROTOCOL"
	conf.MessageQueue.Kafka.Sasl.Username = "CANARY_BROKER_USERNAME"
	conf.MessageQueue.Kafka.Sasl.Password = "CANARY_BROKER_PASSWORD"
	queue, err := StartWorker(context.Background(), conf, &agentgrpc.ClientWrapper{AgentClient: &acceptTerminalClient{}}, nil)
	if queue != nil || err == nil || !strings.Contains(err.Error(), "create task queue") || errors.Unwrap(err) == nil {
		t.Fatalf("configured Kafka failure lost: queue=%v err=%v", queue, err)
	}
	if strings.Contains(err.Error(), "CANARY_BROKER") {
		t.Fatalf("startup error exposed broker settings")
	}
}
