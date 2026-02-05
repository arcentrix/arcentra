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
)

func TestNewRocketMQConfig_Defaults(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	config := NewRocketMQConfig(nameServers)

	if len(config.NameServer) != 1 || config.NameServer[0] != "localhost:9876" {
		t.Errorf("expected NameServer to be %v, got %v", nameServers, config.NameServer)
	}
	if config.DelaySlotCount != DefaultDelaySlotCount {
		t.Errorf("expected DelaySlotCount to be %d, got %d", DefaultDelaySlotCount, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != DefaultDelaySlotDuration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", DefaultDelaySlotDuration, config.DelaySlotDuration)
	}
	if config.ConsumerModel != consumer.Clustering {
		t.Errorf("expected ConsumerModel to be Clustering, got %v", config.ConsumerModel)
	}
	if config.ConsumeTimeout != 5*time.Minute {
		t.Errorf("expected ConsumeTimeout to be 5m, got %v", config.ConsumeTimeout)
	}
	if config.MaxReconsumeTimes != 3 {
		t.Errorf("expected MaxReconsumeTimes to be 3, got %d", config.MaxReconsumeTimes)
	}
}

func TestRocketMQConfig_MultipleNameServers(t *testing.T) {
	nameServers := []string{"localhost:9876", "localhost:9877", "localhost:9878"}
	config := NewRocketMQConfig(nameServers)

	if len(config.NameServer) != 3 {
		t.Errorf("expected NameServer length to be 3, got %d", len(config.NameServer))
	}
	for i, ns := range nameServers {
		if config.NameServer[i] != ns {
			t.Errorf("expected NameServer[%d] to be %s, got %s", i, ns, config.NameServer[i])
		}
	}
}

func TestRocketMQConfig_WithOptions(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	config := NewRocketMQConfig(nameServers,
		WithRocketMQGroupID("test-group"),
		WithRocketMQTopicPrefix("test-prefix"),
		WithRocketMQConsumerModel(consumer.BroadCasting),
		WithRocketMQConsumeTimeout(10*time.Minute),
		WithRocketMQMaxReconsumeTimes(5),
	)

	if config.GroupID != "test-group" {
		t.Errorf("expected GroupID to be 'test-group', got %s", config.GroupID)
	}
	if config.TopicPrefix != "test-prefix" {
		t.Errorf("expected TopicPrefix to be 'test-prefix', got %s", config.TopicPrefix)
	}
	if config.ConsumerModel != consumer.BroadCasting {
		t.Errorf("expected ConsumerModel to be BroadCasting, got %v", config.ConsumerModel)
	}
	if config.ConsumeTimeout != 10*time.Minute {
		t.Errorf("expected ConsumeTimeout to be 10m, got %v", config.ConsumeTimeout)
	}
	if config.MaxReconsumeTimes != 5 {
		t.Errorf("expected MaxReconsumeTimes to be 5, got %d", config.MaxReconsumeTimes)
	}
}

func TestRocketMQConfig_AuthFields(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	config := NewRocketMQConfig(nameServers,
		WithRocketMQAuth("access-key", "secret-key"),
	)

	if config.AccessKey != "access-key" {
		t.Errorf("expected AccessKey to be 'access-key', got %s", config.AccessKey)
	}
	if config.SecretKey != "secret-key" {
		t.Errorf("expected SecretKey to be 'secret-key', got %s", config.SecretKey)
	}
}

func TestRocketMQConfig_AllFields(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	config := &RocketMQConfig{
		NameServer:        nameServers,
		GroupID:           "test-group",
		TopicPrefix:       "test-prefix",
		DelaySlotCount:    24,
		DelaySlotDuration: time.Hour,
		ConsumerModel:     consumer.Clustering,
		ConsumeTimeout:    5 * time.Minute,
		MaxReconsumeTimes: 3,
		AccessKey:         "access-key",
		SecretKey:         "secret-key",
	}

	if len(config.NameServer) == 0 {
		t.Error("expected NameServer to be set")
	}
	if config.GroupID == "" {
		t.Error("expected GroupID to be set")
	}
	if config.TopicPrefix == "" {
		t.Error("expected TopicPrefix to be set")
	}
	if config.DelaySlotCount != 24 {
		t.Errorf("expected DelaySlotCount to be 24, got %d", config.DelaySlotCount)
	}
	if config.DelaySlotDuration != time.Hour {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", time.Hour, config.DelaySlotDuration)
	}
	if config.ConsumerModel != consumer.Clustering {
		t.Errorf("expected ConsumerModel to be Clustering, got %v", config.ConsumerModel)
	}
	if config.ConsumeTimeout != 5*time.Minute {
		t.Errorf("expected ConsumeTimeout to be 5m, got %v", config.ConsumeTimeout)
	}
	if config.MaxReconsumeTimes != 3 {
		t.Errorf("expected MaxReconsumeTimes to be 3, got %d", config.MaxReconsumeTimes)
	}
	if config.AccessKey != "access-key" {
		t.Errorf("expected AccessKey to be 'access-key', got %s", config.AccessKey)
	}
	if config.SecretKey != "secret-key" {
		t.Errorf("expected SecretKey to be 'secret-key', got %s", config.SecretKey)
	}
}
