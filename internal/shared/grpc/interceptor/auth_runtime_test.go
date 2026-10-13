package interceptor

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"testing"
)

func TestInspectJobExecutionAuthentication(t *testing.T) {
	previous := tokenVerifier
	SetTokenVerifier(executionTokenVerifier{})
	t.Cleanup(func() { SetTokenVerifier(previous) })
	check := AuthUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_InspectJobExecution_FullMethodName}
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }
	if _, err := check(context.Background(), &agentv1.InspectJobExecutionRequest{AgentId: "agent-a"}, info, handler); err == nil || called {
		t.Fatal("unauthenticated inspection allowed")
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if _, err := check(ctx, &agentv1.InspectJobExecutionRequest{AgentId: "agent-b"}, info, handler); err == nil || called {
		t.Fatal("foreign Agent inspection allowed")
	}
	if _, err := check(ctx, &agentv1.InspectJobExecutionRequest{AgentId: "agent-a"}, info, handler); err != nil || !called {
		t.Fatalf("original Agent inspection blocked: %v", err)
	}
}
