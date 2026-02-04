// Copyright 2025 Arcade Team
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
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcade/pkg/safe"
	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQConfig represents RabbitMQ configuration
type RabbitMQConfig struct {
	URL               string        // AMQP URL
	Exchange          string        // Exchange name
	TopicPrefix       string        // Topic prefix
	DelaySlotCount    int           // Number of delay topic slots
	DelaySlotDuration time.Duration // Time interval for each delay slot
	PrefetchCount     int           // Prefetch count
	PrefetchSize      int           // Prefetch size
	// Authentication configuration
	Username  string      // Username (if not included in URL)
	Password  string      // Password (if not included in URL)
	TLSConfig *tls.Config // TLS configuration (optional)
}

// NewRabbitMQConfig creates a RabbitMQ configuration using the option pattern
func NewRabbitMQConfig(url string, opts ...RabbitMQOption) *RabbitMQConfig {
	config := &RabbitMQConfig{
		URL:               url,
		DelaySlotCount:    DefaultDelaySlotCount,
		DelaySlotDuration: DefaultDelaySlotDuration,
		PrefetchCount:     10,
		PrefetchSize:      0,
	}

	for _, opt := range opts {
		opt.apply(config)
	}

	return config
}

// rabbitmqBroker is the RabbitMQ broker implementation
type rabbitmqBroker struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	config  *RabbitMQConfig
	mu      sync.RWMutex
}

// newRabbitMQBroker creates a RabbitMQ broker
func newRabbitMQBroker(config *queueConfig) (MessageQueueBroker, DelayManager, error) {
	rabbitmqConfig := config.rabbitmqConfig
	if rabbitmqConfig == nil {
		rabbitmqConfig = NewRabbitMQConfig(
			config.BootstrapServers,
			WithRabbitMQExchange(config.TopicPrefix),
			WithRabbitMQTopicPrefix(config.TopicPrefix),
		)
		rabbitmqConfig.DelaySlotCount = config.DelaySlotCount
		rabbitmqConfig.DelaySlotDuration = config.DelaySlotDuration
	}

	// Connect to RabbitMQ
	var conn *amqp.Connection
	var err error

	// Build new URL if username and password are provided but not included in URL
	url := rabbitmqConfig.URL
	if rabbitmqConfig.Username != "" && rabbitmqConfig.Password != "" {
		// Check if URL already contains authentication info
		if !containsAuthInfo(url) {
			url = buildURL(url, rabbitmqConfig.Username, rabbitmqConfig.Password)
		}
	}

	// Choose connection method based on TLS configuration
	if rabbitmqConfig.TLSConfig != nil {
		conn, err = amqp.DialTLS(url, rabbitmqConfig.TLSConfig)
	} else {
		conn, err = amqp.Dial(url)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	// Create channel
	channel, err := conn.Channel()
	if err != nil {
		err = conn.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Set prefetch count
	if err = channel.Qos(rabbitmqConfig.PrefetchCount, rabbitmqConfig.PrefetchSize, false); err != nil {
		err = channel.Close()
		if err != nil {
			return nil, nil, err
		}
		err = conn.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	// Declare exchange
	if err = channel.ExchangeDeclare(
		rabbitmqConfig.Exchange,
		"topic", // topic type exchange
		true,    // durable
		false,   // auto-deleted
		false,   // internal
		false,   // no-wait
		nil,     // arguments
	); err != nil {
		err = channel.Close()
		if err != nil {
			return nil, nil, err
		}
		err = conn.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("failed to declare exchange: %w", err)
	}

	broker := &rabbitmqBroker{
		conn:    conn,
		channel: channel,
		config:  rabbitmqConfig,
	}

	// Create delay manager (using RabbitMQ delay message plugin or dead letter queue)
	delayManager := NewRabbitMQDelayManager(
		channel,
		rabbitmqConfig.Exchange,
		fmt.Sprintf("%s-tasks", rabbitmqConfig.TopicPrefix),
		rabbitmqConfig.DelaySlotCount,
		rabbitmqConfig.DelaySlotDuration,
	)

	return broker, delayManager, nil
}

// SendMessage sends a single message
func (b *rabbitmqBroker) SendMessage(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	// Convert headers to amqp.Table
	amqpHeaders := make(amqp.Table)
	for k, v := range headers {
		amqpHeaders[k] = v
	}

	err := b.channel.PublishWithContext(
		ctx,
		b.config.Exchange,
		topic, // routing key
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         value,
			MessageId:    key,
			Headers:      amqpHeaders,
			DeliveryMode: amqp.Persistent, // Persistent message
			Timestamp:    time.Now(),
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

// SendBatchMessages sends multiple messages in batch
func (b *rabbitmqBroker) SendBatchMessages(ctx context.Context, topic string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	var firstErr error
	for _, msg := range messages {
		// Convert headers to amqp.Table
		amqpHeaders := make(amqp.Table)
		for k, v := range msg.Headers {
			amqpHeaders[k] = v
		}

		err := b.channel.PublishWithContext(
			ctx,
			b.config.Exchange,
			topic, // routing key
			false, // mandatory
			false, // immediate
			amqp.Publishing{
				ContentType:  "application/json",
				Body:         msg.Value,
				MessageId:    msg.Key,
				Headers:      amqpHeaders,
				DeliveryMode: amqp.Persistent,
				Timestamp:    time.Now(),
			},
		)

		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to publish message: %w", err)
			}
		}
	}

	return firstErr
}

// Subscribe subscribes to topics and consumes messages
func (b *rabbitmqBroker) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	// Create queue and bind for each topic
	for _, topic := range topics {
		queueName := fmt.Sprintf("%s-%s", b.config.TopicPrefix, topic)

		// Declare queue
		queue, err := b.channel.QueueDeclare(
			queueName,
			true,  // durable
			false, // delete when unused
			false, // exclusive
			false, // no-wait
			nil,   // arguments
		)
		if err != nil {
			return fmt.Errorf("failed to declare queue: %w", err)
		}

		// Bind queue to exchange
		if err := b.channel.QueueBind(
			queue.Name,
			topic, // routing key
			b.config.Exchange,
			false,
			nil,
		); err != nil {
			return fmt.Errorf("failed to bind queue: %w", err)
		}

		// Consume messages
		msgs, err := b.channel.Consume(
			queue.Name,
			"",    // consumer tag
			false, // auto-ack (manual acknowledgment)
			false, // exclusive
			false, // no-local
			false, // no-wait
			nil,   // args
		)
		if err != nil {
			return fmt.Errorf("failed to register consumer: %w", err)
		}

		// Start consumption goroutine
		safe.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-msgs:
					if !ok {
						return
					}

					// Convert message format
					headers := make(map[string]string)
					for k, v := range msg.Headers {
						if str, ok := v.(string); ok {
							headers[k] = str
						}
					}

					message := &Message{
						Key:     msg.MessageId,
						Value:   msg.Body,
						Headers: headers,
					}

					// Process message
					if err := handler(ctx, message); err != nil {
						// Reject message and requeue
						_ = msg.Nack(false, true)
						continue
					}

					// Acknowledge message
					_ = msg.Ack(false)
				}
			}
		})
	}

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

// Close closes the connection
func (b *rabbitmqBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error

	if b.channel != nil {
		if err := b.channel.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close channel: %w", err))
		}
	}

	if b.conn != nil {
		if err := b.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing rabbitmq broker: %v", errs)
	}

	return nil
}

// containsAuthInfo checks if the URL already contains authentication information
func containsAuthInfo(url string) bool {
	// Check if URL contains @ symbol (before hostname)
	for i := 0; i < len(url); i++ {
		if url[i] == '@' {
			// Check if there's a : before @ (indicating username:password format)
			if i > 0 {
				for j := i - 1; j >= 0; j-- {
					if url[j] == ':' {
						return true
					}
					if url[j] == '/' || url[j] == '@' {
						break
					}
				}
			}
		}
	}
	return false
}

// buildURL builds a RabbitMQ URL with authentication information
func buildURL(baseURL, username, password string) string {
	// Return directly if URL already contains authentication info
	if containsAuthInfo(baseURL) {
		return baseURL
	}

	// Parse protocol and host part
	protocol := "amqp://"
	hostPart := baseURL

	if len(baseURL) >= 7 && baseURL[:7] == "amqps://" {
		protocol = "amqps://"
		hostPart = baseURL[7:]
	} else if len(baseURL) >= 6 && baseURL[:6] == "amqp://" {
		hostPart = baseURL[6:]
	} else if len(baseURL) >= 5 && baseURL[:5] == "amqps" {
		protocol = "amqps://"
		if len(baseURL) > 5 && baseURL[5] == ':' {
			hostPart = baseURL[6:]
			if len(hostPart) > 2 && hostPart[:2] == "//" {
				hostPart = hostPart[2:]
			}
		}
	} else if len(baseURL) >= 4 && baseURL[:4] == "amqp" {
		if len(baseURL) > 4 && baseURL[4] == ':' {
			hostPart = baseURL[5:]
			if len(hostPart) > 2 && hostPart[:2] == "//" {
				hostPart = hostPart[2:]
			}
		}
	}

	// Build new URL
	return fmt.Sprintf("%s%s:%s@%s", protocol, username, password, hostPart)
}
