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

package logstream

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/bytedance/sonic"
)

const buildLogsTopic = "BUILD_LOGS"

type KafkaLogPublisher struct {
	producer *kafka.Producer
}

func NewKafkaLogPublisher(cfg kafka.KafkaConfig) (*KafkaLogPublisher, error) {
	if cfg.BootstrapServers == "" {
		return nil, nil
	}
	clientOptions := []kafka.ClientOption{
		kafka.WithSecurityProtocol(cfg.SecurityProtocol),
		kafka.WithSaslMechanism(cfg.Sasl.Mechanism),
		kafka.WithSaslUsername(cfg.Sasl.Username),
		kafka.WithSaslPassword(cfg.Sasl.Password),
		kafka.WithSslCaFile(cfg.Ssl.CaFile),
		kafka.WithSslCertFile(cfg.Ssl.CertFile),
		kafka.WithSslKeyFile(cfg.Ssl.KeyFile),
		kafka.WithSslPassword(cfg.Ssl.Password),
	}

	producer, err := kafka.NewProducer(
		cfg.BootstrapServers,
		"arcentra-agent",
		kafka.WithProducerClientOptions(clientOptions...),
		kafka.WithProducerAcks(cfg.Acks),
		kafka.WithProducerRetries(cfg.Retries),
		kafka.WithProducerCompression(cfg.Compression),
	)
	if err != nil {
		return nil, fmt.Errorf("create kafka log producer: %w", err)
	}

	return &KafkaLogPublisher{producer: producer}, nil
}

func (p *KafkaLogPublisher) Publish(ctx context.Context, msg *logstream.BuildLogMessage) error {
	if p == nil || p.producer == nil || msg == nil {
		return nil
	}
	payload, err := sonic.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal log message: %w", err)
	}
	return p.producer.Send(ctx, buildLogsTopic, logstream.BuildLogKey(msg), payload, nil)
}

func (p *KafkaLogPublisher) Close() {
	if p == nil || p.producer == nil {
		return
	}
	p.producer.Close()
}
