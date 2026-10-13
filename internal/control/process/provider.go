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
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
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
) (*Process, error) {
	engine := NewProcess(repos, pluginMgr, taskQueue, st, logger, appConf, services.Secret)
	engine.initLocalLogPublisher(services.LogAggregator)
	if engine.logInitErr != nil {
		engine.Stop()
		return nil, engine.logInitErr
	}
	return engine, nil
}

// ProvideTaskQueueProducer 创建控制器任务生产者，未配置 Kafka 时保留本地执行模式
// 已配置 Kafka 的编码或初始化失败必须阻止启动，不得降级为本地执行
func ProvideTaskQueueProducer(appConf *config.AppConfig, _ *log.Logger) (nova.TaskQueue, error) {
	if appConf == nil {
		return nil, fmt.Errorf("control configuration is required")
	}
	kafkaCfg := appConf.MessageQueue.Kafka
	if kafkaCfg.BootstrapServers == "" {
		log.Info("Kafka not configured, pipeline process running in local-only mode")
		return nil, nil
	}

	queueCfg := appConf.TaskQueue
	codec, err := taskqueue.ResolveMessageCodec(queueCfg.MessageFormat, queueCfg.MessageCodec)
	if err != nil {
		return nil, fmt.Errorf("configure control task queue: %w", err)
	}
	options := []nova.QueueOption{
		nova.WithMessageCodec(codec),
		nova.WithKafka(kafkaCfg.BootstrapServers,
			nova.WithKafkaAuth(kafkaCfg.SecurityProtocol, kafkaCfg.Sasl.Mechanism, kafkaCfg.Sasl.Username, kafkaCfg.Sasl.Password),
			nova.WithKafkaSSL(kafkaCfg.Ssl.CaFile, kafkaCfg.Ssl.CertFile, kafkaCfg.Ssl.KeyFile, kafkaCfg.Ssl.Password),
			nova.WithKafkaClientProgramName("arcentra-control"),
		),
	}

	tq, err := nova.NewTaskQueue(options...)
	if err != nil {
		return nil, taskqueue.WrapStartupError("create control task queue producer", err)
	}
	return tq, nil
}
