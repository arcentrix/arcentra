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

package executor

import (
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/config"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
)

const (
	eventTopicPipeline = "EVENT_PIPELINE"
	eventTopicAgent    = "EVENT_AGENT"
	eventTopicPlatform = "EVENT_PLATFORM"
	eventTopicArtifact = "EVENT_ARTIFACT"
)

// BuildEventEmitterConfig builds emitter config from app config.
func BuildEventEmitterConfig(appConf *config.AppConfig) EventEmitterConfig {
	if appConf == nil {
		return EventEmitterConfig{}
	}
	timeout := time.Duration(appConf.Events.Timeout) * time.Second
	return EventEmitterConfig{
		SourcePrefix:   appConf.Events.SourcePrefix,
		PublishTimeout: timeout,
	}
}

// NewEventPublisherFromConfig builds a publisher based on app config.
func NewEventPublisherFromConfig(appConf *config.AppConfig) EventPublisher {
	if appConf == nil {
		return nil
	}

	var publishers []EventPublisher

	mqKafka := appConf.MessageQueue.Kafka
	if mqKafka.BootstrapServers != "" {
		kafkaPublisher, err := NewKafkaTopicPublisher(
			mqKafka.BootstrapServers,
			"arcentra",
			resolveEventTopic,
			kafka.WithProducerClientOptions(
				kafka.WithSecurityProtocol(mqKafka.SecurityProtocol),
				kafka.WithSaslMechanism(mqKafka.Sasl.Mechanism),
				kafka.WithSaslUsername(mqKafka.Sasl.Username),
				kafka.WithSaslPassword(mqKafka.Sasl.Password),
				kafka.WithSslCaFile(mqKafka.Ssl.CaFile),
				kafka.WithSslCertFile(mqKafka.Ssl.CertFile),
				kafka.WithSslKeyFile(mqKafka.Ssl.KeyFile),
				kafka.WithSslPassword(mqKafka.Ssl.Password),
			),
			kafka.WithProducerAcks(mqKafka.Acks),
			kafka.WithProducerRetries(mqKafka.Retries),
			kafka.WithProducerCompression(mqKafka.Compression),
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
