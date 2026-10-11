// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func encodeBuildLogForTest(t *testing.T, payload logstream.BuildLogMessage) []byte {
	t.Helper()
	value, err := sonic.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBuildLogEventIdentityIgnoresBrokerMetadata(t *testing.T) {
	payload := logstream.BuildLogMessage{
		EventID: "073b7b54-629d-4a91-bfd8-e0777c66c909", StepRunID: "step",
		Timestamp: 42, LineNumber: 3, Level: "info", Content: "same content",
		Stream: "stdout", PluginName: "shell", AgentID: "agent-1",
	}
	value := encodeBuildLogForTest(t, payload)
	first, err := decodeBuildLogEntry(value, "BUILD_LOGS", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if first.LogID != "643890bf3dfe3715fb9677d001000c7dc30fe9e3b15f0e3a26d494eacf8feb4c" {
		t.Fatalf("stable event identity format changed: %q", first.LogID)
	}
	for _, location := range []struct {
		topic     string
		partition int32
		offset    int64
	}{{"BUILD_LOGS", 0, 200}, {"BUILD_LOGS", 1, 100}, {"BUILD_LOGS_REPLAY", 7, 1000}} {
		replay, err := decodeBuildLogEntry(value, location.topic, location.partition, location.offset)
		if err != nil {
			t.Fatal(err)
		}
		if *replay != *first {
			t.Fatalf("broker replay changed decoded entry: first=%#v replay=%#v", first, replay)
		}
	}
	if len(first.LogID) != 64 || first.StepRunID != payload.StepRunID || first.Timestamp != payload.Timestamp ||
		first.LineNumber != payload.LineNumber || first.Level != payload.Level || first.Content != payload.Content ||
		first.Stream != payload.Stream || first.PluginName != payload.PluginName || first.AgentID != payload.AgentID {
		t.Fatalf("payload mapping changed: %#v", first)
	}
}

func TestBuildLogEventIdentityIsScoped(t *testing.T) {
	base := logstream.BuildLogMessage{EventID: "event-1", ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", StepRunID: "step", Content: "same content"}
	first, err := decodeBuildLogEntry(encodeBuildLogForTest(t, base), "BUILD_LOGS", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"event", "project", "pipeline", "run", "step"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			switch field {
			case "event":
				changed.EventID += "-2"
			case "project":
				changed.ProjectID += "-2"
			case "pipeline":
				changed.PipelineID += "-2"
			case "run":
				changed.PipelineRunID += "-2"
			case "step":
				changed.StepRunID += "-2"
			}
			entry, err := decodeBuildLogEntry(encodeBuildLogForTest(t, changed), "BUILD_LOGS", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if first.LogID == entry.LogID {
				t.Fatalf("distinct %s scope was deduplicated", field)
			}
		})
	}
	// 分隔符或转义字符不能把不同作用域编码成相同的身份
	left := base
	left.ProjectID, left.PipelineID = "project:segment", `pipeline"\\`
	right := base
	right.ProjectID, right.PipelineID = "project", `segment:pipeline"\\`
	leftEntry, err := decodeBuildLogEntry(encodeBuildLogForTest(t, left), "BUILD_LOGS", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	rightEntry, err := decodeBuildLogEntry(encodeBuildLogForTest(t, right), "BUILD_LOGS", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if leftEntry.LogID == rightEntry.LogID {
		t.Fatal("scope fields were ambiguously joined")
	}
}

func TestBuildLogLegacyIdentityIsPreserved(t *testing.T) {
	value := encodeBuildLogForTest(t, logstream.BuildLogMessage{StepRunID: "step", Content: "legacy"})
	for _, offset := range []int64{0, 17, 1 << 40} {
		entry, err := decodeBuildLogEntry(value, "BUILD_LOGS", 3, offset)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("BUILD_LOGS:3:%d", offset))))
		if entry.LogID != want {
			t.Fatalf("legacy offset %d identity = %q, want %q", offset, entry.LogID, want)
		}
		replay, err := decodeBuildLogEntry(value, "BUILD_LOGS", 3, offset)
		if err != nil || replay.LogID != entry.LogID {
			t.Fatalf("legacy broker redelivery changed identity: %#v, %v", replay, err)
		}
	}
	first, _ := decodeBuildLogEntry(value, "BUILD_LOGS", 3, 17)
	second, _ := decodeBuildLogEntry(value, "BUILD_LOGS", 3, 18)
	if first.LogID == second.LogID {
		t.Fatal("distinct legacy records were deduplicated by content")
	}
}

func TestBuildLogRejectsMalformedPayloads(t *testing.T) {
	for name, value := range map[string][]byte{
		"invalid JSON": []byte(`{"stepRunId":`),
		"missing step": []byte(`{"eventId":"event-1"}`),
		"blank step":   []byte(`{"stepRunId":" ","eventId":"event-1"}`),
		"wrong type":   []byte(`{"stepRunId":"step","eventId":42}`),
		"unsafe id":    encodeBuildLogForTest(t, logstream.BuildLogMessage{StepRunID: "step", EventID: "event\x00id"}),
		"oversized id": encodeBuildLogForTest(t, logstream.BuildLogMessage{StepRunID: "step", EventID: strings.Repeat("a", logstream.MaxEventIDLength+1)}),
	} {
		t.Run(name, func(t *testing.T) {
			if entry, err := decodeBuildLogEntry(value, "BUILD_LOGS", 0, 100); err == nil || entry != nil {
				t.Fatalf("malformed payload accepted: %#v, %v", entry, err)
			}
		})
	}
}

func TestBuildLogReplayPersistsExactlyOnceAfterRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "build-logs.db")
	open := func() (*LogAggregator, repo.ILogRepository, func()) {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		repository := repo.NewLogRepository(db)
		if err := repository.EnsureSchema(ctx); err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		aggregator := NewLogAggregator(repository)
		return aggregator, repository, func() {
			if err := aggregator.Close(ctx); err != nil {
				t.Error(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Error(err)
			}
		}
	}
	persist := func(aggregator *LogAggregator, payload logstream.BuildLogMessage, partition int32, offset int64) {
		t.Helper()
		entry, err := decodeBuildLogEntry(encodeBuildLogForTest(t, payload), "BUILD_LOGS", partition, offset)
		if err != nil {
			t.Fatal(err)
		}
		if err := aggregator.PushLogContext(ctx, entry); err != nil {
			t.Fatal(err)
		}
		if err := aggregator.FlushStep(ctx, entry.StepRunID); err != nil {
			t.Fatal(err)
		}
	}

	payload := logstream.BuildLogMessage{EventID: "event-1", StepRunID: "step", Content: "repeated output", Timestamp: 1}
	aggregator, _, closeFirst := open()
	persist(aggregator, payload, 0, 100)
	closeFirst()

	// 模拟发布确认丢失后重启：相同持久化事件被发布到新的 Kafka 位置
	aggregator, repository, closeReplay := open()
	defer closeReplay()
	subscriptionCtx, cancelSubscription := context.WithCancel(ctx)
	defer cancelSubscription()
	subscription := aggregator.Subscribe(subscriptionCtx, "step")
	persist(aggregator, payload, 0, 101)
	persist(aggregator, payload, 2, 250)
	entries, err := repository.ListByStepRunID(ctx, "step", 0, 10)
	if err != nil || len(entries) != 1 || entries[0].LineNumber != 1 {
		t.Fatalf("republication duplicated the durable log: %#v, %v", entries, err)
	}
	select {
	case entry := <-subscription:
		t.Fatalf("republication rebroadcast a persisted log: %#v", entry)
	default:
	}
	// 相同内容的不同事件必须保留，即使时间戳和原始行号也相同
	payload.EventID = "event-2"
	persist(aggregator, payload, 0, 102)
	entries, err = repository.ListByStepRunID(ctx, "step", 0, 10)
	if err != nil || len(entries) != 2 || entries[0].LogID == entries[1].LogID || entries[1].LineNumber != 2 {
		t.Fatalf("distinct same-content event was lost: %#v, %v", entries, err)
	}
	select {
	case entry := <-subscription:
		if entry.LineNumber != 2 || entry.LogID != entries[1].LogID {
			t.Fatalf("replay advanced the next event sequence: %#v", entry)
		}
	default:
		t.Fatal("distinct event was not broadcast")
	}
	persist(aggregator, payload, 4, 300)
	select {
	case entry := <-subscription:
		t.Fatalf("same-process replay rebroadcast a persisted log: %#v", entry)
	default:
	}
	for _, entry := range entries {
		if entry.Content != payload.Content {
			t.Fatalf("log content changed: %#v", entry)
		}
	}
}
