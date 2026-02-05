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
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
)

func TestWithRocketMQGroupID(t *testing.T) {
	groupID := "test-group-id"
	opt := WithRocketMQGroupID(groupID)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.GroupID != groupID {
		t.Errorf("expected GroupID to be '%s', got %s", groupID, config.GroupID)
	}
}

func TestWithRocketMQTopicPrefix(t *testing.T) {
	prefix := "test-prefix"
	opt := WithRocketMQTopicPrefix(prefix)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
}

func TestWithRocketMQConsumerModel(t *testing.T) {
	tests := []struct {
		name  string
		model consumer.MessageModel
	}{
		{
			name:  "Clustering model",
			model: consumer.Clustering,
		},
		{
			name:  "Broadcasting model",
			model: consumer.BroadCasting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := WithRocketMQConsumerModel(tt.model)
			config := &RocketMQConfig{}
			opt.apply(config)

			if config.ConsumerModel != tt.model {
				t.Errorf("expected ConsumerModel to be %v, got %v", tt.model, config.ConsumerModel)
			}
		})
	}
}

func TestWithRocketMQConsumeTimeout(t *testing.T) {
	timeout := 10 * time.Minute
	opt := WithRocketMQConsumeTimeout(timeout)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.ConsumeTimeout != timeout {
		t.Errorf("expected ConsumeTimeout to be %v, got %v", timeout, config.ConsumeTimeout)
	}
}

func TestWithRocketMQMaxReconsumeTimes(t *testing.T) {
	times := int32(5)
	opt := WithRocketMQMaxReconsumeTimes(times)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.MaxReconsumeTimes != times {
		t.Errorf("expected MaxReconsumeTimes to be %d, got %d", times, config.MaxReconsumeTimes)
	}
}

func TestWithRocketMQAuth(t *testing.T) {
	accessKey := "test-access-key"
	secretKey := "test-secret-key"
	opt := WithRocketMQAuth(accessKey, secretKey)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.AccessKey != accessKey {
		t.Errorf("expected AccessKey to be '%s', got %s", accessKey, config.AccessKey)
	}
	if config.SecretKey != secretKey {
		t.Errorf("expected SecretKey to be '%s', got %s", secretKey, config.SecretKey)
	}
}

func TestWithRocketMQCredentials(t *testing.T) {
	credentials := &primitive.Credentials{
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	}
	opt := WithRocketMQCredentials(credentials)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.Credentials != credentials {
		t.Error("expected Credentials to be set")
	}
}

func TestWithRocketMQCredentials_Nil(t *testing.T) {
	opt := WithRocketMQCredentials(nil)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.Credentials != nil {
		t.Error("expected Credentials to be nil")
	}
}

func TestWithRocketMQDelaySlots(t *testing.T) {
	count := 48
	duration := 30 * time.Minute
	opt := WithRocketMQDelaySlots(count, duration)
	config := &RocketMQConfig{}
	opt.apply(config)

	if config.DelaySlotCount != count {
		t.Errorf("expected DelaySlotCount to be %d, got %d", count, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != duration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", duration, config.DelaySlotDuration)
	}
}

func TestNewRocketMQConfig(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	config := NewRocketMQConfig(nameServers)

	if len(config.NameServer) != len(nameServers) {
		t.Errorf("expected NameServer length to be %d, got %d", len(nameServers), len(config.NameServer))
	}
	if config.NameServer[0] != nameServers[0] {
		t.Errorf("expected NameServer[0] to be '%s', got %s", nameServers[0], config.NameServer[0])
	}
	if config.DelaySlotCount != DefaultDelaySlotCount {
		t.Errorf("expected DelaySlotCount to be %d, got %d", DefaultDelaySlotCount, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != DefaultDelaySlotDuration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", DefaultDelaySlotDuration, config.DelaySlotDuration)
	}
	if config.ConsumerModel != consumer.Clustering {
		t.Errorf("expected ConsumerModel to be %v, got %v", consumer.Clustering, config.ConsumerModel)
	}
	if config.ConsumeTimeout != 5*time.Minute {
		t.Errorf("expected ConsumeTimeout to be %v, got %v", 5*time.Minute, config.ConsumeTimeout)
	}
	if config.MaxReconsumeTimes != 3 {
		t.Errorf("expected MaxReconsumeTimes to be 3, got %d", config.MaxReconsumeTimes)
	}
}

func TestNewRocketMQConfig_WithOptions(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	groupID := "test-group"
	prefix := "test-prefix"
	accessKey := "test-access-key"
	secretKey := "test-secret-key"

	config := NewRocketMQConfig(nameServers,
		WithRocketMQGroupID(groupID),
		WithRocketMQTopicPrefix(prefix),
		WithRocketMQAuth(accessKey, secretKey),
	)

	if config.NameServer[0] != nameServers[0] {
		t.Errorf("expected NameServer[0] to be '%s', got %s", nameServers[0], config.NameServer[0])
	}
	if config.GroupID != groupID {
		t.Errorf("expected GroupID to be '%s', got %s", groupID, config.GroupID)
	}
	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
	if config.AccessKey != accessKey {
		t.Errorf("expected AccessKey to be '%s', got %s", accessKey, config.AccessKey)
	}
	if config.SecretKey != secretKey {
		t.Errorf("expected SecretKey to be '%s', got %s", secretKey, config.SecretKey)
	}
}

func TestNewRocketMQConfig_MultipleNameServers(t *testing.T) {
	nameServers := []string{"localhost:9876", "localhost:9877", "localhost:9878"}
	config := NewRocketMQConfig(nameServers)

	if len(config.NameServer) != len(nameServers) {
		t.Errorf("expected NameServer length to be %d, got %d", len(nameServers), len(config.NameServer))
	}
	for i, ns := range nameServers {
		if config.NameServer[i] != ns {
			t.Errorf("expected NameServer[%d] to be '%s', got %s", i, ns, config.NameServer[i])
		}
	}
}
