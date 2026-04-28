// Copyright 2026 Arcentra Authors.
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

package process

import (
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/google/wire"
)

// ProviderSet provides the pipeline process and its Kafka task queue producer.
var ProviderSet = wire.NewSet(
	ProvideEngine,
	ProvideTaskQueueProducer,
)

// ProvideEngine creates the pipeline process with all required dependencies.
func ProvideEngine(
	repos *repo.Repositories,
	pluginMgr *plugin.Manager,
	taskQueue nova.TaskQueue,
	st storage.IStorage,
	logger *log.Logger,
	appConf *config.AppConfig,
	services *service.Services,
) *Process {
	return NewProcess(repos, pluginMgr, taskQueue, st, logger, appConf, services.Secret)
}

// ProvideTaskQueueProducer creates a Kafka-backed task queue for the control
// plane to enqueue job run tasks. Returns nil when Kafka is not configured,
// enabling a pure local-execution mode.
func ProvideTaskQueueProducer(appConf *config.AppConfig, _ *log.Logger) nova.TaskQueue {
	kafkaCfg := appConf.MessageQueue.Kafka
	if kafkaCfg.BootstrapServers == "" {
		log.Info("Kafka not configured, pipeline process running in local-only mode")
		return nil
	}

	queueCfg := appConf.TaskQueue
	options := []nova.QueueOption{
		nova.WithKafka(kafkaCfg.BootstrapServers,
			nova.WithKafkaAuth(kafkaCfg.SecurityProtocol, kafkaCfg.Sasl.Mechanism, kafkaCfg.Sasl.Username, kafkaCfg.Sasl.Password),
			nova.WithKafkaSSL(kafkaCfg.Ssl.CaFile, kafkaCfg.Ssl.CertFile, kafkaCfg.Ssl.KeyFile, kafkaCfg.Ssl.Password),
			nova.WithKafkaClientProgramName("arcentra-control"),
		),
	}

	if queueCfg.MessageFormat != "" {
		options = append(options, nova.WithMessageFormat(nova.MessageFormat(queueCfg.MessageFormat)))
	}
	if queueCfg.MessageCodec != "" {
		codec, err := nova.NewMessageCodec(nova.MessageFormat(queueCfg.MessageCodec))
		if err == nil {
			options = append(options, nova.WithMessageCodec(codec))
		}
	}

	tq, err := nova.NewTaskQueue(options...)
	if err != nil {
		log.Warnw("failed to create task queue producer, running in local-only mode", "error", err)
		return nil
	}
	return tq
}
