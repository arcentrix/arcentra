// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/outbox"
	"github.com/arcentrix/arcentra/pkg/safe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func largeGatewayEvents(count int) []outbox.Event {
	events := make([]outbox.Event, count)
	for i := range events {
		events[i] = outbox.Event{Seq: uint64(i + 1), EventID: fmt.Sprintf("evt-%d", i+1), EventType: "outbox", Payload: map[string]any{
			"type": "arcentra.log", "data": strings.Repeat(`"\`, 32*1024),
		}}
	}
	return events
}

func TestGatewaySenderBoundsMessagesByEncodedBytesAndCount(t *testing.T) {
	events := largeGatewayEvents(100)
	batches, err := buildEventBatches(events, "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) < 2 {
		t.Fatalf("large events were not split: %d batches", len(batches))
	}
	var last uint64
	for _, request := range batches {
		if size := proto.Size(request); size > outbox.MaxGatewayMessageBytes {
			t.Fatalf("batch size %d exceeds gRPC maximum", size)
		}
		for _, event := range request.GetBatch().Events {
			if event.Meta.Seq != last+1 {
				t.Fatalf("sequence order changed: %d after %d", event.Meta.Seq, last)
			}
			last = event.Meta.Seq
		}
	}
	if last != 100 {
		t.Fatalf("only sent %d events", last)
	}
	tiny := make([]outbox.Event, outbox.MaxGatewayBatchEvents+1)
	for i := range tiny {
		tiny[i] = outbox.Event{Seq: uint64(i + 1), EventID: fmt.Sprintf("evt-%d", i+1), EventType: "outbox", Payload: map[string]any{}}
	}
	batches, err = buildEventBatches(tiny, "agent", "")
	if err != nil || len(batches) != 2 {
		t.Fatalf("count-limited batching = %d, %v", len(batches), err)
	}
}

func TestGatewaySenderRejectsOversizedOrInvalidPayload(t *testing.T) {
	for _, payload := range []map[string]any{
		{"data": strings.Repeat("x", outbox.MaxEventPayloadBytes)}, {"invalid": make(chan int)}, nil,
	} {
		if _, err := buildEventBatches([]outbox.Event{{Seq: 1, Payload: payload}}, "agent", ""); err == nil {
			t.Fatal("invalid event was converted to an empty payload")
		}
	}
	if _, err := buildEventBatches([]outbox.Event{{Seq: 1, Payload: map[string]any{"data": strings.Repeat("x", outbox.MaxEventPayloadBytes)}}}, "agent", ""); !errors.Is(err, outbox.ErrEventPayloadTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	for _, response := range []*gatewayv1.PushEventsResponse{nil, {}, {Ack: &gatewayv1.IngestAck{Status: gatewayv1.IngestAck_STATUS_REJECTED, LastAcceptedSeq: 999}}} {
		if _, err := parsePushEventsResponse(response); err == nil {
			t.Fatalf("invalid ack accepted: %v", response)
		}
	}
}

type gatewaySenderTestServer struct {
	gatewayv1.UnimplementedGatewayServiceServer
	service         *controlservice.GatewayServiceImpl
	mu              sync.Mutex
	batchSizes      []int
	failSecondBatch bool
}

func (s *gatewaySenderTestServer) PushEvents(stream grpc.ClientStreamingServer[gatewayv1.PushEventsRequest, gatewayv1.PushEventsResponse]) error {
	return s.service.PushEvents(&gatewaySenderTestServerStream{ClientStreamingServer: stream, owner: s})
}

type gatewaySenderTestServerStream struct {
	grpc.ClientStreamingServer[gatewayv1.PushEventsRequest, gatewayv1.PushEventsResponse]
	owner   *gatewaySenderTestServer
	batches int
}

func (s *gatewaySenderTestServerStream) Context() context.Context {
	return agentidentity.WithIdentity(s.ClientStreamingServer.Context(), agentidentity.Identity{ID: "agent", Roles: []string{"agent"}})
}
func (s *gatewaySenderTestServerStream) Recv() (*gatewayv1.PushEventsRequest, error) {
	request, err := s.ClientStreamingServer.Recv()
	if err != nil {
		return nil, err
	}
	if request.GetBatch() != nil {
		s.batches++
		s.owner.mu.Lock()
		s.owner.batchSizes = append(s.owner.batchSizes, proto.Size(request))
		fail := s.owner.failSecondBatch && s.batches == 2
		if fail {
			s.owner.failSecondBatch = false
		}
		s.owner.mu.Unlock()
		if fail {
			return nil, status.Error(codes.Unavailable, "injected disconnect before second batch")
		}
	}
	return request, nil
}

func TestGatewaySenderLargeBatchesOverGRPCWithRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(context.Background()).AutoMigrate(&model.GatewayEvent{}, &model.GatewayEventCursor{}); err != nil {
		t.Fatal(err)
	}
	repository := repo.NewGatewayEventRepository(db)
	service := &gatewaySenderTestServer{service: controlservice.NewGatewayServiceImpl(repository), failSecondBatch: true}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer() // Intentionally keep the real default 4 MiB receive limit.
	gatewayv1.RegisterGatewayServiceServer(server, service)
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///gateway-batch-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sender := NewGatewayServiceImpl(conn, "agent", "")
	events := largeGatewayEvents(100)
	if result, err := sender.Send(ctx, 0, events); err == nil || result.LastSeq != 0 {
		t.Fatalf("failed stream was acknowledged: %#v, %v", result, err)
	}
	seq, err := repository.LastAcceptedSeq(ctx, "agent")
	if err != nil || seq == 0 || seq >= 100 {
		t.Fatalf("first batch was not durably retained: %d, %v", seq, err)
	}
	// Retry through the wrapper's shared sender path, with the original checkpoint.
	wrapper := &GatewaySenderFromWrapper{agentID: "agent"}
	result, err := wrapper.sendWithClient(ctx, 0, events, gatewayv1.NewGatewayServiceClient(conn))
	if err != nil || result.LastSeq != 100 {
		t.Fatalf("large retry = %#v, %v", result, err)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&model.GatewayEvent{}).Count(&count).Error; err != nil || count != 100 {
		t.Fatalf("durable event count = %d, %v", count, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.batchSizes) < 4 {
		t.Fatalf("expected split initial/retry messages, got %v", service.batchSizes)
	}
	for _, size := range service.batchSizes {
		if size > outbox.MaxGatewayMessageBytes {
			t.Fatalf("oversize message delivered: %d", size)
		}
	}
}
