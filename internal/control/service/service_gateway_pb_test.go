// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/safe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type gatewayEventTestStream struct {
	grpc.ServerStream
	ctx      context.Context
	requests []*gatewayv1.PushEventsRequest
	response *gatewayv1.PushEventsResponse
	closeErr error
}

func (s *gatewayEventTestStream) Context() context.Context { return s.ctx }
func (s *gatewayEventTestStream) Recv() (*gatewayv1.PushEventsRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	req := s.requests[0]
	s.requests = s.requests[1:]
	return req, nil
}
func (s *gatewayEventTestStream) SendAndClose(response *gatewayv1.PushEventsResponse) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	s.response = response
	return nil
}

type gatewayLogTestStream struct {
	grpc.ServerStream
	ctx      context.Context
	response *gatewayv1.PushLogsResponse
}

func (s *gatewayLogTestStream) Context() context.Context                  { return s.ctx }
func (s *gatewayLogTestStream) Recv() (*gatewayv1.PushLogsRequest, error) { return nil, io.EOF }
func (s *gatewayLogTestStream) SendAndClose(response *gatewayv1.PushLogsResponse) error {
	s.response = response
	return nil
}

func gatewayServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}

func gatewayTestContext() context.Context {
	return agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: "agent-a", Roles: []string{"agent"}})
}

func gatewayHandshake(agentID string, last uint64) *gatewayv1.PushEventsRequest {
	return &gatewayv1.PushEventsRequest{Payload: &gatewayv1.PushEventsRequest_Handshake{Handshake: &gatewayv1.AgentHandshake{AgentId: agentID, LastKnownSeq: last}}}
}

func gatewayTestEvent(seq uint64) *gatewayv1.Event {
	payload, _ := structpb.NewStruct(map[string]any{
		"specversion": "1.0", "type": "arcentra.step.completed", "data": map[string]any{"command": "untrusted raw content"},
	})
	return &gatewayv1.Event{EventId: fmt.Sprintf("evt-%d", seq), EventType: "outbox", Payload: payload,
		Meta: &gatewayv1.Meta{AgentId: "agent-a", Seq: seq, PipelineId: "pipeline-a", Timestamp: timestamppb.Now()}}
}

func gatewayBatch(events ...*gatewayv1.Event) *gatewayv1.PushEventsRequest {
	return &gatewayv1.PushEventsRequest{Payload: &gatewayv1.PushEventsRequest_Batch{Batch: &gatewayv1.EventBatch{BatchId: "batch", Events: events}}}
}

func TestGatewayEventsPersistBeforeACKAndReplayAfterLostResponse(t *testing.T) {
	db := gatewayServiceTestDB(t)
	svc := NewGatewayServiceImpl(repo.NewGatewayEventRepository(db))
	lost := &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{
		gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1), gatewayTestEvent(2)),
	}, closeErr: io.ErrClosedPipe}
	if err := svc.PushEvents(lost); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost response error = %v", err)
	}
	if lost.response != nil {
		t.Fatal("failed response was unexpectedly acknowledged")
	}
	var count int64
	if err := db.WithContext(context.Background()).Model(&model.GatewayEvent{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("events were not durable before response: count=%d err=%v", count, err)
	}
	// Each retry rebuilds Meta.Timestamp; this must still be an identical replay.
	replay := &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{
		gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1), gatewayTestEvent(2), gatewayTestEvent(3)),
	}}
	if err := svc.PushEvents(replay); err != nil {
		t.Fatal(err)
	}
	if replay.response == nil || replay.response.Ack.Status != gatewayv1.IngestAck_STATUS_ACCEPTED || replay.response.Ack.LastAcceptedSeq != 3 || replay.response.Ack.ExpectedSeq != 4 {
		t.Fatalf("unexpected replay ack: %v", replay.response)
	}
	if err := db.WithContext(context.Background()).Model(&model.GatewayEvent{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("duplicate replay persisted duplicates: count=%d err=%v", count, err)
	}
	var stored model.GatewayEvent
	if err := db.WithContext(context.Background()).Select("agent_id", "payload", "event_type").Where("agent_id = ? AND seq = ?", "agent-a", 1).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Payload, "untrusted raw content") || !strings.Contains(stored.Payload, "arcentra.step.completed") || stored.EventType != "outbox" {
		t.Fatalf("raw CloudEvent not retained: %#v", stored)
	}
	resume := &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 3)}}
	if err := svc.PushEvents(resume); err != nil || resume.response.Ack.LastAcceptedSeq != 3 {
		t.Fatalf("handshake did not read durable checkpoint: %v, %v", resume.response, err)
	}
}

func TestGatewayEventsRejectInvalidStreamsWithoutACK(t *testing.T) {
	spoofed := gatewayTestEvent(1)
	spoofed.Meta.AgentId = "victim"
	missingMeta := gatewayTestEvent(1)
	missingMeta.Meta = nil
	missingPayload := gatewayTestEvent(1)
	missingPayload.Payload = nil
	invalidTime := gatewayTestEvent(1)
	invalidTime.Meta.Timestamp = &timestamppb.Timestamp{Seconds: 253402300800}
	oversized := gatewayTestEvent(1)
	oversized.Payload, _ = structpb.NewStruct(map[string]any{"data": strings.Repeat("x", maxGatewayPayloadBytes)})
	for _, tt := range []struct {
		name     string
		ctx      context.Context
		requests []*gatewayv1.PushEventsRequest
		code     codes.Code
	}{
		{"unauthenticated", context.Background(), nil, codes.Unauthenticated},
		{"empty stream", gatewayTestContext(), nil, codes.InvalidArgument},
		{"spoofed handshake", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("victim", 0)}, codes.PermissionDenied},
		{"spoofed event", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1), spoofed)}, codes.PermissionDenied},
		{"client checkpoint ahead", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 999)}, codes.FailedPrecondition},
		{"missing handshake", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayBatch(gatewayTestEvent(1))}, codes.InvalidArgument},
		{"repeated handshake", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayHandshake("agent-a", 0)}, codes.InvalidArgument},
		{"nil handshake", gatewayTestContext(), []*gatewayv1.PushEventsRequest{{Payload: &gatewayv1.PushEventsRequest_Handshake{}}}, codes.InvalidArgument},
		{"nil request", gatewayTestContext(), []*gatewayv1.PushEventsRequest{nil}, codes.InvalidArgument},
		{"missing payload", gatewayTestContext(), []*gatewayv1.PushEventsRequest{{}}, codes.InvalidArgument},
		{"nil event", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(nil)}, codes.InvalidArgument},
		{"nil metadata", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(missingMeta)}, codes.InvalidArgument},
		{"nil event payload", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(missingPayload)}, codes.InvalidArgument},
		{"invalid timestamp", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(invalidTime)}, codes.InvalidArgument},
		{"empty batch", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch()}, codes.InvalidArgument},
		{"sequence gap", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1), gatewayTestEvent(3))}, codes.FailedPrecondition},
		{"zero sequence", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(0))}, codes.InvalidArgument},
		{"oversized event", gatewayTestContext(), []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(oversized)}, codes.ResourceExhausted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := gatewayServiceTestDB(t)
			svc := NewGatewayServiceImpl(repo.NewGatewayEventRepository(db))
			stream := &gatewayEventTestStream{ctx: tt.ctx, requests: tt.requests}
			if err := svc.PushEvents(stream); status.Code(err) != tt.code {
				t.Fatalf("error = %v, want %v", err, tt.code)
			}
			if stream.response != nil {
				t.Fatalf("rejected stream was acknowledged: %v", stream.response)
			}
			var count int64
			db.WithContext(context.Background()).Model(&model.GatewayEvent{}).Count(&count)
			if count != 0 {
				t.Fatalf("rejected batch stored %d events", count)
			}
		})
	}
}

func TestGatewayEventsDatabaseFailureDoesNotACK(t *testing.T) {
	db := gatewayServiceTestDB(t)
	if err := db.Callback().Create().Before("gorm:create").Register("test:reject_event", func(tx *gorm.DB) {
		if tx.Statement.Table == "gateway_events" {
			tx.AddError(errors.New("database write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayServiceImpl(repo.NewGatewayEventRepository(db))
	stream := &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1))}}
	if err := svc.PushEvents(stream); status.Code(err) != codes.Unavailable || stream.response != nil {
		t.Fatalf("storage error = %v, response = %v", err, stream.response)
	}
	if seq, err := repo.NewGatewayEventRepository(db).LastAcceptedSeq(context.Background(), "agent-a"); err != nil || seq != 0 {
		t.Fatalf("failed storage advanced checkpoint: %d, %v", seq, err)
	}
	stream = &gatewayEventTestStream{ctx: gatewayTestContext()}
	if err := NewGatewayServiceImpl(nil).PushEvents(stream); status.Code(err) != codes.Unavailable || stream.response != nil {
		t.Fatalf("missing repository error = %v, response = %v", err, stream.response)
	}
}

func TestGatewayEventsConflictingReplayDoesNotACK(t *testing.T) {
	db := gatewayServiceTestDB(t)
	svc := NewGatewayServiceImpl(repo.NewGatewayEventRepository(db))
	stream := &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1))}}
	if err := svc.PushEvents(stream); err != nil {
		t.Fatal(err)
	}
	changed := gatewayTestEvent(1)
	changed.Payload, _ = structpb.NewStruct(map[string]any{"changed": true})
	stream = &gatewayEventTestStream{ctx: gatewayTestContext(), requests: []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(changed)}}
	if err := svc.PushEvents(stream); status.Code(err) != codes.AlreadyExists || stream.response != nil {
		t.Fatalf("conflicting replay = %v, response = %v", err, stream.response)
	}
}

func TestGatewayLogsUnsupportedNeverACK(t *testing.T) {
	for _, tt := range []struct {
		ctx  context.Context
		code codes.Code
	}{
		{gatewayTestContext(), codes.Unimplemented}, {context.Background(), codes.Unauthenticated},
	} {
		stream := &gatewayLogTestStream{ctx: tt.ctx}
		if err := NewGatewayServiceImpl(nil).PushLogs(stream); status.Code(err) != tt.code || stream.response != nil {
			t.Fatalf("unsupported log ingestion error = %v, response = %v", err, stream.response)
		}
	}
}

type gatewayAuthenticatedStream struct{ grpc.ServerStream }

func (s gatewayAuthenticatedStream) Context() context.Context {
	return agentidentity.WithIdentity(s.ServerStream.Context(), agentidentity.Identity{ID: "agent-a", Roles: []string{"agent"}})
}

func TestGatewayEventsGRPCTransport(t *testing.T) {
	db := gatewayServiceTestDB(t)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, gatewayAuthenticatedStream{ss})
	}))
	gatewayv1.RegisterGatewayServiceServer(server, NewGatewayServiceImpl(repo.NewGatewayEventRepository(db)))
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///gateway-test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := gatewayv1.NewGatewayServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 2 {
		stream, err := client.PushEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range []*gatewayv1.PushEventsRequest{gatewayHandshake("agent-a", 0), gatewayBatch(gatewayTestEvent(1))} {
			if err := stream.Send(request); err != nil {
				t.Fatal(err)
			}
		}
		response, err := stream.CloseAndRecv()
		if err != nil || response.GetAck().GetLastAcceptedSeq() != 1 {
			t.Fatalf("gRPC replay = %v, %v", response, err)
		}
	}
	stream, err := client.PushEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(gatewayHandshake("victim", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.CloseAndRecv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("gRPC spoofing returned %v", err)
	}
}
