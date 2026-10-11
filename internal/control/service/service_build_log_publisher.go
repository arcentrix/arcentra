package service

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/bytedance/sonic"
)

// DirectBuildLogPublisher 为未配置 Kafka 的控制面提供同一持久化及实时订阅路径。
type DirectBuildLogPublisher struct{ Aggregator *LogAggregator }

// Publish 仅在数据库持久化成功后确认；本地 spool 负责故障重试。
func (p *DirectBuildLogPublisher) Publish(ctx context.Context, msg *logstream.BuildLogMessage) error {
	if p == nil || p.Aggregator == nil || msg == nil || msg.EventID == "" {
		return fmt.Errorf("direct build logs require an aggregator and stable event id")
	}
	payload, err := sonic.Marshal(msg)
	if err != nil {
		return err
	}
	entry, err := decodeBuildLogEntry(payload, "", 0, 0)
	if err != nil {
		return err
	}
	if err = p.Aggregator.PushLogContext(ctx, entry); err != nil {
		return err
	}
	return p.Aggregator.FlushStep(ctx, entry.StepRunID)
}
