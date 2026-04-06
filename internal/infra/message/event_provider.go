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

package message

import (
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/message/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
)

const (
	eventTopicPipeline = "EVENT_PIPELINE"
	eventTopicAgent    = "EVENT_AGENT"
	eventTopicPlatform = "EVENT_PLATFORM"
	eventTopicArtifact = "EVENT_ARTIFACT"
)

// BuildEventEmitterConfig builds emitter config from source prefix and timeout seconds.
func BuildEventEmitterConfig(sourcePrefix string, timeoutSec int) executor.EventEmitterConfig {
	timeout := time.Duration(timeoutSec) * time.Second
	return executor.EventEmitterConfig{
		SourcePrefix:   sourcePrefix,
		PublishTimeout: timeout,
	}
}

// NewEventPublisherFromKafkaConfig builds a publisher based on Kafka config.
func NewEventPublisherFromKafkaConfig(kafkaCfg kafka.Config) executor.EventPublisher {
	var publishers []executor.EventPublisher

	if kafkaCfg.BootstrapServers != "" {
		kafkaPublisher, err := NewKafkaTopicPublisher(
			kafkaCfg.BootstrapServers,
			"arcentra",
			resolveEventTopic,
			kafka.WithProducerOptions(
				kafka.WithSecurityProtocol(kafkaCfg.SecurityProtocol),
				kafka.WithSaslMechanism(kafkaCfg.Sasl.Mechanism),
				kafka.WithSaslUsername(kafkaCfg.Sasl.Username),
				kafka.WithSaslPassword(kafkaCfg.Sasl.Password),
				kafka.WithSslCaFile(kafkaCfg.Ssl.CaFile),
				kafka.WithSslCertFile(kafkaCfg.Ssl.CertFile),
				kafka.WithSslKeyFile(kafkaCfg.Ssl.KeyFile),
				kafka.WithSslPassword(kafkaCfg.Ssl.Password),
			),
			kafka.WithProducerAcks(kafkaCfg.Acks),
			kafka.WithProducerRetries(kafkaCfg.Retries),
			kafka.WithProducerCompression(kafkaCfg.Compression),
		)
		if err != nil {
			log.Warnw("failed to create kafka event publisher", "error", err)
		} else {
			publishers = append(publishers, kafkaPublisher)
		}
	}

	if len(publishers) == 0 {
		return nil
	}
	return NewMultiPublisher(publishers...)
}

func resolveEventTopic(event map[string]any) string {
	eventType, _ := event["type"].(string)
	if eventType == "" {
		return eventTopicPlatform
	}
	switch {
	case strings.HasPrefix(eventType, "arcentra.pipeline."),
		strings.HasPrefix(eventType, "arcentra.job."),
		strings.HasPrefix(eventType, "arcentra.step."),
		strings.HasPrefix(eventType, "arcentra.task."):
		return eventTopicPipeline
	case strings.HasPrefix(eventType, "arcentra.agent."):
		return eventTopicAgent
	case strings.HasPrefix(eventType, "arcentra.artifact."):
		return eventTopicArtifact
	default:
		return eventTopicPlatform
	}
}
