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

package nova

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mqkafka "github.com/arcentrix/arcentra/pkg/mq/kafka"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// KafkaConfig represents Kafka configuration
type KafkaConfig struct {
	BootstrapServers  string        // Kafka broker address
	GroupID           string        // Consumer group ID
	TopicPrefix       string        // Topic prefix
	ClientProgram     string        // Client program name
	DelaySlotCount    int           // Number of delay topic slots
	DelaySlotDuration time.Duration // Time interval for each delay slot
	AutoCommit        bool          // Whether to auto-commit
	SessionTimeout    int           // Session timeout in milliseconds
	MaxPollInterval   int           // Maximum poll interval in milliseconds
	// Authentication configuration
	SASLMechanism    string // SASL mechanism: PLAIN, SCRAM-SHA-256, SCRAM-SHA-512
	SASLUsername     string // SASL username
	SASLPassword     string // SASL password
	SecurityProtocol string // Security protocol: PLAINTEXT, SSL, SASL_PLAINTEXT, SASL_SSL
	SSLCAFile        string // SSL CA certificate file path
	SSLCertFile      string // SSL client certificate file path
	SSLKeyFile       string // SSL client key file path
	SSLPassword      string // SSL key password (optional)
}

// NewKafkaConfig creates a Kafka configuration using the option pattern
func NewKafkaConfig(bootstrapServers string, opts ...KafkaOption) *KafkaConfig {
	config := &KafkaConfig{
		BootstrapServers:  bootstrapServers,
		DelaySlotCount:    DefaultDelaySlotCount,
		DelaySlotDuration: DefaultDelaySlotDuration,
		AutoCommit:        DefaultAutoCommit,
		SessionTimeout:    DefaultSessionTimeout,
		MaxPollInterval:   DefaultMaxPollInterval,
	}

	for _, opt := range opts {
		opt(config)
	}

	return config
}

// kafkaBroker is the Kafka broker implementation
type kafkaBroker struct {
	producer      *mqkafka.Producer
	consumer      *mqkafka.Consumer
	delayConsumer *mqkafka.Consumer
	config        *KafkaConfig
	mu            sync.RWMutex
}

// newKafkaBroker creates a Kafka broker
func newKafkaBroker(config *queueConfig) (MessageQueueBroker, DelayManager, error) {
	kafkaConfig := config.kafkaConfig
	if kafkaConfig == nil {
		// Create configuration using option pattern
		kafkaConfig = NewKafkaConfig(
			config.BootstrapServers,
			WithKafkaGroupID(config.GroupID),
			WithKafkaTopicPrefix(config.TopicPrefix),
			WithKafkaAutoCommit(config.AutoCommit),
			WithKafkaSessionTimeout(config.SessionTimeout),
			WithKafkaMaxPollInterval(config.MaxPollInterval),
		)
		kafkaConfig.DelaySlotCount = config.DelaySlotCount
		kafkaConfig.DelaySlotDuration = config.DelaySlotDuration
	}

	if strings.TrimSpace(kafkaConfig.TopicPrefix) == "" {
		kafkaConfig.TopicPrefix = config.TopicPrefix
	}
	kafkaConfig.TopicPrefix = normalizeQueuePrefix(kafkaConfig.TopicPrefix)
	if kafkaConfig.DelaySlotCount <= 0 {
		kafkaConfig.DelaySlotCount = DefaultDelaySlotCount
	}
	if kafkaConfig.DelaySlotDuration <= 0 {
		kafkaConfig.DelaySlotDuration = DefaultDelaySlotDuration
	}

	clientOptions := []mqkafka.Option{
		mqkafka.WithSecurityProtocol(kafkaConfig.SecurityProtocol),
		mqkafka.WithSaslMechanism(kafkaConfig.SASLMechanism),
		mqkafka.WithSaslUsername(kafkaConfig.SASLUsername),
		mqkafka.WithSaslPassword(kafkaConfig.SASLPassword),
		mqkafka.WithSslCaFile(kafkaConfig.SSLCAFile),
		mqkafka.WithSslCertFile(kafkaConfig.SSLCertFile),
		mqkafka.WithSslKeyFile(kafkaConfig.SSLKeyFile),
		mqkafka.WithSslPassword(kafkaConfig.SSLPassword),
	}

	programName := strings.TrimSpace(kafkaConfig.ClientProgram)
	if programName == "" {
		programName = "arcentra"
	}
	consumerGroupID := strings.TrimSpace(kafkaConfig.GroupID)
	if consumerGroupID == "" {
		queueGroupID := strings.TrimSpace(config.GroupID)
		if queueGroupID != "" && queueGroupID != DefaultGroupID {
			consumerGroupID = queueGroupID
		}
	}
	if consumerGroupID == "" {
		consumerGroupID = programName
	}
	kafkaConfig.GroupID = consumerGroupID

	producer, err := mqkafka.NewProducer(
		kafkaConfig.BootstrapServers,
		programName,
		mqkafka.WithProducerOptions(clientOptions...),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create producer: %w", err)
	}

	consumer, err := mqkafka.NewConsumer(
		kafkaConfig.BootstrapServers,
		fmt.Sprintf("%s%s", kafkaConfig.TopicPrefix, PriorityNormalSuffix),
		programName,
		mqkafka.WithConsumerGroupID(consumerGroupID),
		mqkafka.WithConsumerOptions(clientOptions...),
		mqkafka.WithConsumerEnableAutoCommit(kafkaConfig.AutoCommit),
		mqkafka.WithConsumerSessionTimeoutMs(kafkaConfig.SessionTimeout),
		mqkafka.WithConsumerMaxPollIntervalMs(kafkaConfig.MaxPollInterval),
	)
	if err != nil {
		producer.Close()
		return nil, nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	// 延迟消息与普通作业必须由独立消费者读取，避免互相覆盖订阅或抢走消息
	targetTopic := fmt.Sprintf("%s_TASKS", kafkaConfig.TopicPrefix)
	delayConsumer, err := mqkafka.NewConsumer(kafkaConfig.BootstrapServers, targetTopic, programName+"-delay",
		mqkafka.WithConsumerGroupID(consumerGroupID+"-delay"),
		mqkafka.WithConsumerOptions(clientOptions...), mqkafka.WithConsumerEnableAutoCommit(false),
		mqkafka.WithConsumerSessionTimeoutMs(kafkaConfig.SessionTimeout), mqkafka.WithConsumerMaxPollIntervalMs(kafkaConfig.MaxPollInterval))
	if err != nil {
		_ = consumer.Close()
		producer.Close()
		return nil, nil, fmt.Errorf("create delay consumer: %w", err)
	}
	broker := &kafkaBroker{producer: producer, consumer: consumer, delayConsumer: delayConsumer, config: kafkaConfig}
	delayManager := NewDelayTopicManager(producer, delayConsumer, targetTopic, kafkaConfig.DelaySlotCount, kafkaConfig.DelaySlotDuration)

	return broker, delayManager, nil
}

// ProducerMessage sends a single message
func (b *kafkaBroker) ProducerMessage(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	if err := b.producer.Producer(ctx, topic, key, value, headers); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// ProducerBatchMessages sends multiple messages in batch
func (b *kafkaBroker) ProducerBatchMessages(ctx context.Context, topic string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	var firstErr error

	for _, msg := range messages {
		if err := b.producer.Producer(ctx, topic, msg.Key, msg.Value, msg.Headers); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if firstErr != nil {
		return fmt.Errorf("failed to send batch: %w", firstErr)
	}

	return nil
}

// Subscribe subscribes to topics and consumes messages
func (b *kafkaBroker) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	if err := b.consumer.Subscribe(topics); err != nil {
		return fmt.Errorf("failed to subscribe topics: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, err := b.consumer.ReadMessage(100 * time.Millisecond)
			if err != nil {
				var kafkaErr ckafka.Error
				if errors.As(err, &kafkaErr) && kafkaErr.Code() == ckafka.ErrTimedOut {
					continue
				}
				// Log error but continue running
				continue
			}

			// Convert message format
			headers := make(map[string]string)
			for _, h := range msg.Headers {
				headers[h.Key] = string(h.Value)
			}

			message := &Message{
				Key:     string(msg.Key),
				Value:   msg.Value,
				Headers: headers,
			}

			// 在当前消息成功处理和提交前不读取下一条，避免后续 offset 跳过失败作业
			if err := retryDelivery(ctx, func() error { return handler(ctx, message) }); err != nil {
				var poison permanentDeliveryError
				if !errors.As(err, &poison) {
					return err
				}
				// 成功保留原始毒消息后再推进 offset，避免丢失或阻塞后续任务
				if err := retryDelivery(ctx, func() error {
					return b.ProducerMessage(ctx, b.config.TopicPrefix+"_DEAD_LETTER", message.Key, message.Value, map[string]string{"error": poison.Error()})
				}); err != nil {
					return err
				}
			}
			if !b.config.AutoCommit {
				if err := retryDelivery(ctx, func() error {
					err := b.consumer.CommitMessage(msg)
					if lostConsumerAssignment(err) {
						return permanentDeliveryError{cause: err}
					}
					return err
				}); err != nil {
					var lost permanentDeliveryError
					if !errors.As(err, &lost) {
						return err
					}
					// 已失去分区归属时必须恢复 poll 以重新加入组；重放由作业 claim 防重
				}
			}

		}
	}
}

// Close closes the connection
func (b *kafkaBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error

	if b.consumer != nil {
		if err := b.consumer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close consumer: %w", err))
		}
		b.consumer = nil
	}

	if b.delayConsumer != nil {
		if err := b.delayConsumer.Close(); err != nil {
			errs = append(errs, err)
		}
		b.delayConsumer = nil
	}
	if b.producer != nil {
		b.producer.Close()
		b.producer = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing kafka broker: %v", errs)
	}

	return nil
}

func lostConsumerAssignment(err error) bool {
	var kafkaErr ckafka.Error
	if !errors.As(err, &kafkaErr) {
		return false
	}
	switch kafkaErr.Code() {
	case ckafka.ErrIllegalGeneration, ckafka.ErrUnknownMemberID, ckafka.ErrRebalanceInProgress, ckafka.ErrAssignmentLost, ckafka.ErrState:
		return true
	}
	return false
}
