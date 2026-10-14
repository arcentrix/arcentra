package interceptor

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"testing"
)

func TestJobInputsRPCRequiresAuthenticatedOriginalAgent(t *testing.T) {
	previous := tokenVerifier
	SetTokenVerifier(executionTokenVerifier{})
	t.Cleanup(func() { SetTokenVerifier(previous) })
	check := AuthUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_GetOrRecordJobInputs_FullMethodName}
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }
	if _, err := check(context.Background(), &agentv1.GetOrRecordJobInputsRequest{AgentId: "agent-a"}, info, handler); err == nil || called {
		t.Fatal("unauthenticated input request allowed")
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	if _, err := check(ctx, &agentv1.GetOrRecordJobInputsRequest{AgentId: "agent-b"}, info, handler); err == nil || called {
		t.Fatal("foreign input request allowed")
	}
	if _, err := check(ctx, &agentv1.GetOrRecordJobInputsRequest{AgentId: "agent-a"}, info, handler); err != nil || !called {
		t.Fatalf("original input RPC blocked: %v", err)
	}
}
