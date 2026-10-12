package service

import (
	"context"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"google.golang.org/grpc"
)

type capacityRegistrationClient struct {
	agentv1.AgentServiceClient
	request *agentv1.RegisterRequest
}

func (c *capacityRegistrationClient) Register(_ context.Context, req *agentv1.RegisterRequest, _ ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	c.request = req
	return &agentv1.RegisterResponse{Success: true}, nil
}
func TestRegistrationReportsOnlySupportedCapacity(t *testing.T) {
	for _, configured := range []int{0, 1, 5, -1} {
		client := &capacityRegistrationClient{}
		conf := &config.AgentConfig{Agent: config.AgentInfo{MaxConcurrentJobs: configured}}
		svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil)
		_, err := svc.Register(context.Background(), nil)
		if configured == 0 || configured == 1 {
			if err != nil || client.request == nil || client.request.MaxConcurrentStepRuns != 1 {
				t.Fatalf("capacity %d: request %+v, %v", configured, client.request, err)
			}
		} else if err == nil || client.request != nil {
			t.Fatalf("unsupported capacity was advertised: %d %+v %v", configured, client.request, err)
		}
	}
}
