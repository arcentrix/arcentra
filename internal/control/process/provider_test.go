// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"errors"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/nova"
)

func TestProvideTaskQueueProducerRejectsInvalidTransport(t *testing.T) {
	for _, tc := range []struct{ format, codec, field string }{
		{"protobuf", "protobuf", "messageFormat"},
		{"blob", "json", "messageFormat"},
		{"typo", "sonic", "messageFormat"},
		{"sonic", "protobuf", "messageCodec"},
		{"json", "blob", "messageCodec"},
		{"json", "typo", "messageCodec"},
	} {
		t.Run(tc.format+"/"+tc.codec, func(t *testing.T) {
			conf := &config.AppConfig{TaskQueue: nova.TaskQueueConfig{MessageFormat: tc.format, MessageCodec: tc.codec}}
			conf.MessageQueue.Kafka.BootstrapServers = "127.0.0.1:9092"
			queue, err := ProvideTaskQueueProducer(conf, nil)
			if queue != nil || err == nil || !strings.Contains(err.Error(), "taskQueue."+tc.field) {
				t.Fatalf("invalid transport silently accepted: queue=%v err=%v", queue, err)
			}
		})
	}
}

func TestProvideTaskQueueProducerPreservesUnconfiguredLocalMode(t *testing.T) {
	conf := &config.AppConfig{TaskQueue: nova.TaskQueueConfig{MessageFormat: "protobuf", MessageCodec: "typo"}}
	queue, err := ProvideTaskQueueProducer(conf, nil)
	if queue != nil || err != nil {
		t.Fatalf("unconfigured Kafka must remain local-only: queue=%v err=%v", queue, err)
	}
}

func TestProvideTaskQueueProducerPropagatesBrokerInitializationError(t *testing.T) {
	for _, format := range []string{"", "json", "sonic", " SONIC "} {
		t.Run(format, func(t *testing.T) {
			conf := &config.AppConfig{TaskQueue: nova.TaskQueueConfig{MessageFormat: format}}
			conf.MessageQueue.Kafka.BootstrapServers = "127.0.0.1:9092"
			conf.MessageQueue.Kafka.SecurityProtocol = "CANARY_BROKER_SECRET_INVALID_PROTOCOL"
			conf.MessageQueue.Kafka.Sasl.Username = "CANARY_BROKER_USERNAME"
			conf.MessageQueue.Kafka.Sasl.Password = "CANARY_BROKER_PASSWORD"
			queue, err := ProvideTaskQueueProducer(conf, nil)
			if queue != nil || err == nil || !strings.Contains(err.Error(), "create control task queue producer") || errors.Unwrap(err) == nil {
				t.Fatalf("configured Kafka silently downgraded: queue=%v err=%v", queue, err)
			}
			if strings.Contains(err.Error(), "CANARY_BROKER") {
				t.Fatalf("startup error exposed broker settings")
			}
		})
	}
}

func TestProvideEngineKafkaStartupErrorDoesNotExposeBrokerSettings(t *testing.T) {
	conf := &config.AppConfig{}
	conf.MessageQueue.Kafka.BootstrapServers = "127.0.0.1:9092"
	conf.MessageQueue.Kafka.SecurityProtocol = "CANARY_PRIVATE_BROKER_CONFIG"
	engine, err := ProvideEngine(nil, nil, nil, nil, nil, conf, &service.Services{})
	if engine != nil || err == nil || !strings.Contains(err.Error(), "create control Kafka log publisher") || strings.Contains(err.Error(), "CANARY_PRIVATE") || errors.Unwrap(err) == nil {
		t.Fatalf("unsafe or missing Kafka log startup error: engine=%v err=%v", engine, err)
	}
}
