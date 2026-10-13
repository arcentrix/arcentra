// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentconfig "github.com/arcentrix/arcentra/internal/agent/config"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
)

func TestTaskQueueConfigRejectedBeforeApplicationStartup(t *testing.T) {
	for _, endpoint := range []string{"control", "agent"} {
		for _, tc := range []struct {
			name, brokers, format, codec, errField string
		}{
			{name: "default", brokers: "127.0.0.1:9092"},
			{name: "mixed JSON implementations", brokers: "127.0.0.1:9092", format: "json", codec: "sonic"},
			{name: "legacy protobuf", brokers: "127.0.0.1:9092", format: "protobuf", codec: "protobuf", errField: "messageFormat"},
			{name: "shadowed unsupported format", brokers: "127.0.0.1:9092", format: "blob", codec: "json", errField: "messageFormat"},
			{name: "invalid codec", brokers: "127.0.0.1:9092", format: "sonic", codec: "typo", errField: "messageCodec"},
			{name: "unconfigured Kafka", format: "protobuf", codec: "protobuf"},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				content := fmt.Sprintf("[grpc]\nserverAddr = \"127.0.0.1:9090\"\n[messageQueue.kafka]\nbootstrapServers = %q\n[taskQueue]\nmessageFormat = %q\nmessageCodec = %q\n", tc.brokers, tc.format, tc.codec)
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if endpoint == "control" {
					_, err = controlconfig.ProvideConf(path)
				} else {
					_, err = agentconfig.NewConf(path)
				}
				if tc.errField == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "taskQueue."+tc.errField) {
					t.Fatalf("expected %s startup failure, got %v", tc.errField, err)
				}
			})
		}
	}
}

func TestShippedTaskQueueConfigsUseSupportedTransport(t *testing.T) {
	control, err := controlconfig.ProvideConf("../../conf.d/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if control.TaskQueue.MessageFormat != "sonic" || control.TaskQueue.MessageCodec != "sonic" {
		t.Fatal("controller sample must use Sonic JSON")
	}
	for _, file := range []string{"agent.toml", "agent.baremetal.example.toml"} {
		t.Run(file, func(t *testing.T) {
			agent, err := agentconfig.NewConf(filepath.Join("../../conf.d", file))
			if err != nil {
				t.Fatal(err)
			}
			if agent.TaskQueue.MessageFormat != "sonic" || agent.TaskQueue.MessageCodec != "sonic" {
				t.Fatal("agent sample must use Sonic JSON")
			}
		})
	}
}
