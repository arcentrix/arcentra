package taskqueue

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type rejectedInitialClaimClient struct {
	agentv1.AgentServiceClient
	code   codes.Code
	claims []string
}

func (c *rejectedInitialClaimClient) ReportJobRunStatus(ctx context.Context, req *agentv1.ReportJobRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	c.claims = append(c.claims, md.Get(executionClaimMetadataKey)[0])
	return nil, status.Error(c.code, "synthetic rejected claim")
}

func TestInitialClaimCleanupOnlyForDefinitiveRejection(t *testing.T) {
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.PermissionDenied, codes.InvalidArgument, codes.NotFound, codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.ResourceExhausted, codes.Unauthenticated, codes.Unknown} {
		t.Run(code.String(), func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			job := "rejected-claim-" + code.String()
			payload := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: job}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: root}}
			rpc := &rejectedInitialClaimClient{code: code}
			client := &agentgrpc.ClientWrapper{AgentClient: rpc}
			t.Cleanup(func() { completeExecutionClaim(ctx, "job", job) })
			definitive := code == codes.FailedPrecondition || code == codes.PermissionDenied || code == codes.InvalidArgument || code == codes.NotFound
			for i := 0; i < 2; i++ {
				err := executeJobRun(ctx, conf, client, nil, payload)
				if status.Code(err) != code {
					t.Fatalf("unexpected result %v", err)
				}
				_, retained := executionClaims.Load("job/" + job)
				if retained == definitive {
					t.Fatalf("retained=%v definitive=%v", retained, definitive)
				}
			}
			if !definitive && rpc.claims[0] != rpc.claims[1] {
				t.Fatal("uncertain claim was replaced")
			}
			if _, err := os.Stat(filepath.Join(root, "run", job)); !os.IsNotExist(err) {
				t.Fatal("rejected claim created workspace")
			}
		})
	}
}
