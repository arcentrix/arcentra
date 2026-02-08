c
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/bytedance/sonic"
)

const buildLogsTopic = "BUILD_LOGS"

// LogPublisher publishes build logs.
type LogPublisher interface {
	Publish(ctx context.Context, msg *logstream.BuildLogMessage) error
}

// KafkaLogPublisher publishes build logs to Kafka.
type KafkaLogPublisher struct {
	producer *kafka.Producer
}

// NewKafkaLogPublisher creates a Kafka log publisher from Kafka config.
func NewKafkaLogPublisher(cfg kafka.KafkaConfig, clientID string) (*KafkaLogPublisher, error) {
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
		clientID,
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

func convertKafkaConfig(cfg kafka.KafkaConfig) kafka.KafkaConfig {
	return kafka.KafkaConfig{
		BootstrapServers: cfg.BootstrapServers,
		Acks:             cfg.Acks,
		Retries:          cfg.Retries,
		Compression:      cfg.Compression,
		SecurityProtocol: cfg.SecurityProtocol,
		Sasl: kafka.SaslConfig{
			Mechanism: cfg.Sasl.Mechanism,
			Username:  cfg.Sasl.Username,
			Password:  cfg.Sasl.Password,
		},
		Ssl: kafka.SslConfig{
			CaFile:   cfg.Ssl.CaFile,
			CertFile: cfg.Ssl.CertFile,
			KeyFile:  cfg.Ssl.KeyFile,
			Password: cfg.Ssl.Password,
		},
	}
}

// Publish sends a build log message to Kafka.
func (p *KafkaLogPublisher) Publish(ctx context.Context, msg *logstream.BuildLogMessage) error {
	if p == nil || p.producer == nil || msg == nil {
		return nil
	}
	payload, err := sonic.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal build log message: %w", err)
	}
	return p.producer.Send(ctx, buildLogsTopic, msg.BuildLogKey(), payload, nil)
}

// Close closes the Kafka producer.
func (p *KafkaLogPublisher) Close() {
	if p == nil || p.producer == nil {
		return
	}
	p.producer.Close()
}

// BuildLogMessageFromEvent builds a build log message from EventContext.
func BuildLogMessageFromEvent(ctx EventContext, content, stream string) *logstream.BuildLogMessage {
	return &logstream.BuildLogMessage{
		PipelineId: ctx.PipelineId,
		StepName:   ctx.StepName,
		StepRunId:  ctx.StepId,
		PluginName: ctx.PluginName,
		AgentId:    ctx.AgentId,
		Timestamp:  time.Now().Unix(),
		LineNumber: 0,
		Level:      "info",
		Stream:     stream,
		Content:    content,
	}
}
