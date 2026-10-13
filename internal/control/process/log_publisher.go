package process

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// initLogPublisher 使用进程级队列，启动即恢复历史日志，不依赖新 Run 的触发。
func (e *Process) initLogPublisher() {
	if e.appConf == nil || e.appConf.MessageQueue.Kafka.BootstrapServers == "" {
		return
	}
	publisher, err := executor.NewReliableKafkaLogPublisher(e.appConf.MessageQueue.Kafka, "arcentra-control-logs", e.appConf.LogSpool, "./data/build-log-spool/control")
	if err != nil {
		e.logInitErr = taskqueue.WrapStartupError("create control Kafka log publisher", err)
		return
	}
	e.logPublisher = publisher
	if publisher != nil {
		e.closeLogs = publisher.Close
	}
}

// initLocalLogPublisher 通过共享聚合器将无 Kafka 本地执行日志直接持久化。
func (e *Process) initLocalLogPublisher(aggregator *service.LogAggregator) {
	if e.appConf == nil || e.appConf.MessageQueue.Kafka.BootstrapServers != "" {
		return
	}
	if aggregator == nil {
		e.logInitErr = fmt.Errorf("log aggregator is required for local execution")
		return
	}
	cfg := e.appConf.LogSpool
	if cfg.Directory == "" {
		cfg.Directory = "./data/build-log-spool/control"
	}
	if cfg.MaxDiskUsageMB == 0 {
		cfg.MaxDiskUsageMB = 512
	}
	if cfg.MaxDiskUsageMB < 1 || cfg.MaxDiskUsageMB > 1024*1024 {
		e.logInitErr = fmt.Errorf("invalid log spool maxDiskUsageMB")
		return
	}
	publisher, err := executor.NewDurableLogPublisher(context.Background(), &service.DirectBuildLogPublisher{Aggregator: aggregator}, cfg.Directory, cfg.MaxDiskUsageMB*1024*1024)
	if err != nil {
		e.logInitErr = err
		return
	}
	e.logPublisher = publisher
	e.closeLogs = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := publisher.Close(ctx); err != nil {
			log.Errorw("close local log spool failed", "error", err)
		}
	}
	if err = publisher.Start(context.Background()); err != nil {
		e.logInitErr = err
		e.closeLogs()
	}
}
