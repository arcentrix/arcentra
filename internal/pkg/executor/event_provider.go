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

package executor

import (
	"time"

	"github.com/arcentrix/arcentra/internal/engine/config"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
)

const (
	eventTopic           = "ARCENTRA_EVENTS"
	defaultKafkaClientId = "arcentra-events"
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
	if appConf == nil || !appConf.Events.Enabled {
		return nil
	}

	var publishers []EventPublisher

	if appConf.Events.Kafka.Enabled {
		kafkaPublisher, err := NewKafkaPublisher(
			appConf.Events.Kafka.BootstrapServers,
			eventTopic,
			kafka.WithProducerClientOptions(
				kafka.WithClientId(defaultKafkaClientId),
				kafka.WithSecurityProtocol(appConf.Events.Kafka.SecurityProtocol),
				kafka.WithSaslMechanism(appConf.Events.Kafka.Sasl.Mechanism),
				kafka.WithSaslUsername(appConf.Events.Kafka.Sasl.Username),
				kafka.WithSaslPassword(appConf.Events.Kafka.Sasl.Password),
				kafka.WithSslCaFile(appConf.Events.Kafka.Ssl.CaFile),
				kafka.WithSslCertFile(appConf.Events.Kafka.Ssl.CertFile),
				kafka.WithSslKeyFile(appConf.Events.Kafka.Ssl.KeyFile),
				kafka.WithSslPassword(appConf.Events.Kafka.Ssl.Password),
			),
			kafka.WithProducerAcks(appConf.Events.Kafka.Acks),
			kafka.WithProducerRetries(appConf.Events.Kafka.Retries),
			kafka.WithProducerCompression(appConf.Events.Kafka.Compression),
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
