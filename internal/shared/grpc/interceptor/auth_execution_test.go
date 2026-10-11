package interceptor

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"testing"
)

type executionTokenVerifier struct{}

func (executionTokenVerifier) VerifyAgentToken(context.Context, string, string) (string, error) {
	return "agent-a", nil
}

func TestAgentAuthenticationBoundaries(t *testing.T) {
	previous := tokenVerifier
	SetTokenVerifier(executionTokenVerifier{})
	t.Cleanup(func() { SetTokenVerifier(previous) })
	interceptor := AuthUnaryInterceptor()
	called := false
	handler := func(ctx context.Context, _ any) (any, error) { called = true; return nil, nil }
	_, err := interceptor(context.Background(), &agentv1.RegisterRequest{}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_Register_FullMethodName}, handler)
	if err != nil || !called {
		t.Fatalf("registration must reach its own credential validator: %v", err)
	}
	called = false
	_, err = interceptor(context.Background(), &agentv1.HeartbeatRequest{AgentId: "agent-a"}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_Heartbeat_FullMethodName}, handler)
	if err == nil || called {
		t.Fatal("unauthenticated heartbeat must not mutate agent")
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token"))
	_, err = interceptor(ctx, &agentv1.ReportJobRunStatusRequest{AgentId: "agent-b"}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_ReportJobRunStatus_FullMethodName}, handler)
	if err == nil || called {
		t.Fatal("token cannot report another agent's identity")
	}
	_, err = interceptor(ctx, &agentv1.ReportJobRunStatusRequest{AgentId: "agent-a"}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_ReportJobRunStatus_FullMethodName}, handler)
	if err != nil || !called {
		t.Fatalf("valid agent rejected: %v", err)
	}
	for _, method := range []string{"/pipeline.v1.PipelineService/TriggerPipeline", "/steprun.v1.StepRunService/DeleteStepRun", "/agent.v1.AgentService/CancelJobRun", "/agent.v1.AgentService/UpdateLabels"} {
		called = false
		_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
		if err == nil || called {
			t.Fatalf("agent accessed privileged RPC %s", method)
		}
	}

}
