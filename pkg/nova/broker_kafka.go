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

package nova

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// KafkaConfig represents Kafka configuration
type KafkaConfig struct {
	BootstrapServers  string        // Kafka broker address
	GroupID           string        // Consumer group ID
	TopicPrefix       string        // Topic prefix
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
		opt.apply(config)
	}

	return config
}

// kafkaBroker is the Kafka broker implementation
type kafkaBroker struct {
	producer *kafka.Producer
	consumer *kafka.Consumer
	config   *KafkaConfig
	mu       sync.RWMutex
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

	// Create producer configuration
	producerConfig := &kafka.ConfigMap{
		"bootstrap.servers":                     kafkaConfig.BootstrapServers,
		"acks":                                  "all",
		"retries":                               3,
		"max.in.flight.requests.per.connection": 5,
		"compression.type":                      "snappy",
	}

	// Apply authentication configuration
	applyKafkaAuthConfig(producerConfig, kafkaConfig)

	producer, err := kafka.NewProducer(producerConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create producer: %w", err)
	}

	// Create consumer configuration
	consumerConfig := &kafka.ConfigMap{
		"bootstrap.servers":    kafkaConfig.BootstrapServers,
		"group.id":             kafkaConfig.GroupID,
		"auto.offset.reset":    "earliest",
		"enable.auto.commit":   kafkaConfig.AutoCommit,
		"session.timeout.ms":   kafkaConfig.SessionTimeout,
		"max.poll.interval.ms": kafkaConfig.MaxPollInterval,
	}

	// Apply authentication configuration
	applyKafkaAuthConfig(consumerConfig, kafkaConfig)

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		producer.Close()
		return nil, nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	broker := &kafkaBroker{
		producer: producer,
		consumer: consumer,
		config:   kafkaConfig,
	}

	// Create delay manager
	targetTopic := fmt.Sprintf("%s_TASKS", kafkaConfig.TopicPrefix)
	delayManager := NewDelayTopicManager(
		producer,
		consumer,
		targetTopic,
		kafkaConfig.DelaySlotCount,
		kafkaConfig.DelaySlotDuration,
	)

	return broker, delayManager, nil
}

// SendMessage sends a single message
func (b *kafkaBroker) SendMessage(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	kafkaHeaders := make([]kafka.Header, 0, len(headers))
	for k, v := range headers {
		kafkaHeaders = append(kafkaHeaders, kafka.Header{
			Key:   k,
			Value: []byte(v),
		})
	}

	message := &kafka.Message{
		TopicPartition: kafka.TopicPartition{
			Topic:     &topic,
			Partition: kafka.PartitionAny,
		},
		Key:     []byte(key),
		Value:   value,
		Headers: kafkaHeaders,
	}

	deliveryChan := make(chan kafka.Event, 1)
	if err := b.producer.Produce(message, deliveryChan); err != nil {
		return fmt.Errorf("failed to produce message: %w", err)
	}

	// Wait for send result
	select {
	case e := <-deliveryChan:
		m := e.(*kafka.Message)
		if m.TopicPartition.Error != nil {
			return fmt.Errorf("failed to deliver message: %w", m.TopicPartition.Error)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SendBatchMessages sends multiple messages in batch
func (b *kafkaBroker) SendBatchMessages(ctx context.Context, topic string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	deliveryChan := make(chan kafka.Event, len(messages))
	var firstErr error

	for _, msg := range messages {
		kafkaHeaders := make([]kafka.Header, 0, len(msg.Headers))
		for k, v := range msg.Headers {
			kafkaHeaders = append(kafkaHeaders, kafka.Header{
				Key:   k,
				Value: []byte(v),
			})
		}

		message := &kafka.Message{
			TopicPartition: kafka.TopicPartition{
				Topic:     &topic,
				Partition: kafka.PartitionAny,
			},
			Key:     []byte(msg.Key),
			Value:   msg.Value,
			Headers: kafkaHeaders,
		}

		if err := b.producer.Produce(message, deliveryChan); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to produce message: %w", err)
			}
		}
	}

	// Wait for all messages to be sent
	successCount := 0
	for i := 0; i < len(messages); i++ {
		select {
		case e := <-deliveryChan:
			m := e.(*kafka.Message)
			if m.TopicPartition.Error == nil {
				successCount++
			} else if firstErr == nil {
				firstErr = m.TopicPartition.Error
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if firstErr != nil {
		return fmt.Errorf("failed to send batch: %w (success: %d/%d)", firstErr, successCount, len(messages))
	}

	return nil
}

// Subscribe subscribes to topics and consumes messages
func (b *kafkaBroker) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	if err := b.consumer.SubscribeTopics(topics, nil); err != nil {
		return fmt.Errorf("failed to subscribe topics: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, err := b.consumer.ReadMessage(100 * time.Millisecond)
			if err != nil {
				if err.(kafka.Error).Code() == kafka.ErrTimedOut {
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

			// Process message
			if err := handler(ctx, message); err != nil {
				// Log error but continue processing
				continue
			}

			// Manually commit offset
			if !b.config.AutoCommit {
				if _, err := b.consumer.CommitMessage(msg); err != nil {
					// Log error but continue processing
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
	}

	if b.producer != nil {
		b.producer.Flush(15 * 1000) // Wait 15 seconds
		b.producer.Close()
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing kafka broker: %v", errs)
	}

	return nil
}

// applyKafkaAuthConfig applies Kafka authentication configuration
func applyKafkaAuthConfig(config *kafka.ConfigMap, kafkaConfig *KafkaConfig) {
	// Set security protocol
	if kafkaConfig.SecurityProtocol != "" {
		_ = config.SetKey("security.protocol", kafkaConfig.SecurityProtocol)
	}

	// Set SASL configuration
	if kafkaConfig.SASLMechanism != "" {
		_ = config.SetKey("sasl.mechanism", kafkaConfig.SASLMechanism)
		if kafkaConfig.SASLUsername != "" {
			_ = config.SetKey("sasl.username", kafkaConfig.SASLUsername)
		}
		if kafkaConfig.SASLPassword != "" {
			_ = config.SetKey("sasl.password", kafkaConfig.SASLPassword)
		}
	}

	// Set SSL configuration
	if kafkaConfig.SSLCAFile != "" {
		_ = config.SetKey("ssl.ca.location", kafkaConfig.SSLCAFile)
	}
	if kafkaConfig.SSLCertFile != "" {
		_ = config.SetKey("ssl.certificate.location", kafkaConfig.SSLCertFile)
	}
	if kafkaConfig.SSLKeyFile != "" {
		_ = config.SetKey("ssl.key.location", kafkaConfig.SSLKeyFile)
	}
	if kafkaConfig.SSLPassword != "" {
		_ = config.SetKey("ssl.key.password", kafkaConfig.SSLPassword)
	}
}
