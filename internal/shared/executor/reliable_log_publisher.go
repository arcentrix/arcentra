package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
)

// ReliableKafkaLogPublisher 先同步落盘，再重试发送至 Kafka；确认前保留本地记录。
type ReliableKafkaLogPublisher struct {
	*DurableLogPublisher
	transport *KafkaLogPublisher
	once      sync.Once
}

// NewReliableKafkaLogPublisher 创建进程独占的日志队列，启动时重放旧记录。
func NewReliableKafkaLogPublisher(cfg kafka.Config, clientID string, spool logstream.SpoolConfig, defaultDir string) (*ReliableKafkaLogPublisher, error) {
	if spool.Directory == "" {
		spool.Directory = defaultDir
	}
	if spool.MaxDiskUsageMB == 0 {
		spool.MaxDiskUsageMB = 512
	}
	if spool.MaxDiskUsageMB < 1 || spool.MaxDiskUsageMB > 1024*1024 {
		return nil, fmt.Errorf("log spool maxDiskUsageMB must be between 1 and 1048576")
	}
	// 日志持久化必须等待 broker 确认；禁止 acks=0 绕过确认语义。
	cfg.Acks = "all"
	transport, err := NewKafkaLogPublisher(cfg, clientID)
	if err != nil {
		return nil, err
	}
	durable, err := NewDurableLogPublisher(context.Background(), transport, spool.Directory, spool.MaxDiskUsageMB*1024*1024)
	if err != nil {
		transport.Close()
		return nil, err
	}
	if err = durable.Start(context.Background()); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = durable.Close(closeCtx)
		transport.Close()
		return nil, err
	}
	return &ReliableKafkaLogPublisher{DurableLogPublisher: durable, transport: transport}, nil
}

// Close 停止重放并保留未确认记录供下次启动继续发送。
func (p *ReliableKafkaLogPublisher) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.DurableLogPublisher.Close(ctx); err != nil {
			log.Errorw("close durable build log spool failed", "error", err)
		}
		p.transport.Close()
	})
}

// UnavailableLogPublisher 将初始化故障显式传播到执行路径，避免无日志成功。
type UnavailableLogPublisher struct{ Cause error }

// Publish 返回初始化故障，不接受无法持久化的日志。
func (p *UnavailableLogPublisher) Publish(context.Context, *logstream.BuildLogMessage) error {
	return fmt.Errorf("build log publisher unavailable: %w", p.Cause)
}
