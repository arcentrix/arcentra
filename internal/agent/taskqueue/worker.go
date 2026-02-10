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

package taskqueue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

func StartWorker(ctx context.Context, agentConf *config.AgentConfig) (nova.TaskQueue, error) {
	if agentConf == nil {
		return nil, nil
	}
	cfg := agentConf.MessageQueue.Kafka
	if cfg.BootstrapServers == "" {
		return nil, nil
	}
	queueCfg := agentConf.TaskQueue
	delaySlotDuration := time.Duration(queueCfg.DelaySlotDuration) * time.Second
	options := []nova.QueueOption{
		nova.WithKafka(cfg.BootstrapServers,
			nova.WithKafkaAuth(cfg.SecurityProtocol, cfg.Sasl.Mechanism, cfg.Sasl.Username, cfg.Sasl.Password),
			nova.WithKafkaSSL(cfg.Ssl.CaFile, cfg.Ssl.CertFile, cfg.Ssl.KeyFile, cfg.Ssl.Password),
			nova.WithKafkaClientProgramName("arcentra-agent"),
			nova.WithKafkaAutoCommit(queueCfg.AutoCommit),
			nova.WithKafkaSessionTimeout(queueCfg.SessionTimeout),
			nova.WithKafkaMaxPollInterval(queueCfg.MaxPollInterval),
			nova.WithKafkaDelaySlots(queueCfg.DelaySlotCount, delaySlotDuration),
		),
	}
	if opt := withMessageFormat(queueCfg.MessageFormat); opt != nil {
		options = append(options, opt)
	}
	if opt := withMessageCodec(queueCfg.MessageCodec); opt != nil {
		options = append(options, opt)
	}
	queue, err := nova.NewTaskQueue(options...)
	if err != nil {
		return nil, fmt.Errorf("create task queue: %w", err)
	}

	handler := nova.HandlerFunc(func(ctx context.Context, task *nova.Task) error {
		if task == nil {
			return nil
		}
		switch task.Type {
		case taskqueue.TaskTypeStepRun:
			var payload taskqueue.StepRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				return fmt.Errorf("unmarshal step run payload: %w", err)
			}
			log.Infow("received step run task",
				"stepRunId", payload.StepRunId,
				"jobName", payload.JobName,
				"stepName", payload.StepName,
			)
			return nil
		default:
			log.Debugw("unknown task type", "type", task.Type)
			return nil
		}
	})

	if err := queue.Start(handler); err != nil {
		return nil, fmt.Errorf("start task queue: %w", err)
	}

	return queue, nil
}

func withMessageFormat(value string) nova.QueueOption {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return nil
	}
	return nova.WithMessageFormat(nova.MessageFormat(value))
}

func withMessageCodec(value string) nova.QueueOption {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return nil
	}
	codec, err := nova.NewMessageCodec(nova.MessageFormat(value))
	if err != nil {
		return nil
	}
	return nova.WithMessageCodec(codec)
}
