package taskqueue

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPersistedClaimResponseLossDoesNotDropOrRepeatExecution(t *testing.T) {
	for _, lostKind := range []string{"job", "step"} {
		t.Run(lostKind, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "claims.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			sqlDB.SetMaxOpenConns(1)
			if err := db.WithContext(ctx).AutoMigrate(&model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}); err != nil {
				t.Fatal(err)
			}
			jobRepo, stepRepo := repo.NewJobRunRepo(workerTestDatabase{db}), repo.NewStepRunRepo(workerTestDatabase{db})
			agentID, jobID := "claim-agent", "lost-claim-"+lostKind
			if err := jobRepo.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "claim-run", AgentID: agentID, Status: 2}); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"first", "second"} {
				if err := stepRepo.Create(ctx, &model.StepRun{StepRunID: jobID + "-" + suffix, JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "claim-run", AgentID: agentID, Status: 2}); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			claims := map[string][]string{}
			lost := false
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if values := md.Get("authorization"); len(values) != 1 || values[0] != "bearer claim-test-token" {
					return nil, status.Error(codes.Unauthenticated, "missing test token")
				}
				var kind, runID string
				var state int32
				switch value := req.(type) {
				case *agentv1.ReportJobRunStatusRequest:
					kind, runID, state = "job", value.JobRunId, value.Status
				case *agentv1.ReportStepRunStatusRequest:
					kind, runID, state = "step", value.StepRunId, int32(value.Status)
				}
				claimValues := md.Get(executionClaimMetadataKey)
				if len(claimValues) != 1 {
					return nil, status.Error(codes.InvalidArgument, "missing claim metadata")
				}
				if _, err := uuid.Parse(claimValues[0]); err != nil {
					return nil, status.Error(codes.InvalidArgument, "invalid claim UUID")
				}
				mu.Lock()
				claims[kind+"/"+runID] = append(claims[kind+"/"+runID], claimValues[0])
				mu.Unlock()
				response, err := handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID}), req)
				if err != nil {
					return response, err
				}
				mu.Lock()
				defer mu.Unlock()
				if !lost && state == 3 && kind == lostKind && (kind == "job" || runID == jobID+"-second") {
					lost = true
					// 状态已通过真实仓储写入数据库，但客户端收不到成功响应
					return nil, status.Error(codes.Unavailable, "claim response lost after commit")
				}
				return response, nil
			}))
			agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, stepRepo, nil, jobRepo), nil))
			safe.Go(func() { _ = server.Serve(listener) })
			defer server.Stop()
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "claim-test-token", ReadWriteTimeout: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: agentID, WorkspaceDir: t.TempDir()}}
			payload := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "claim-run", JobRunID: jobID, JobName: "build", AgentID: agentID, Steps: []taskpayload.StepPayload{
				{Name: "first", StepRunID: jobID + "-first", Args: map[string]any{"run": "printf x >> first"}},
				{Name: "second", StepRunID: jobID + "-second", Args: map[string]any{"run": "printf x >> second"}},
			}}
			err = executeJobRun(ctx, conf, client, nil, payload)
			if lostKind == "job" {
				if status.Code(err) != codes.Unavailable {
					t.Fatalf("expected retryable lost claim response: %v", err)
				}
				job, loadErr := jobRepo.GetByJobRunID(ctx, jobID)
				if loadErr != nil || job.Status != 3 || job.ClaimID == "" {
					t.Fatalf("lost response did not follow a committed claim: %+v %v", job, loadErr)
				}
				if _, err := os.Stat(filepath.Join(conf.Agent.WorkspaceDir, "claim-run", jobID, "first")); !os.IsNotExist(err) {
					t.Fatal("job executed before claim confirmation")
				}
				err = executeJobRun(ctx, conf, client, nil, payload)
			}
			if err != nil {
				t.Fatalf("same-claim retry failed: %v", err)
			}
			for _, name := range []string{"first", "second"} {
				if got := artifactTestRead(t, filepath.Join(conf.Agent.WorkspaceDir, "claim-run", jobID), name); got != "x" {
					t.Fatalf("%s executed more than once: %s", name, got)
				}
				if _, ok := executionClaims.Load("step/" + jobID + "-" + name); ok {
					t.Fatal("terminal step retained its execution claim")
				}
			}
			if _, ok := executionClaims.Load("job/" + jobID); ok {
				t.Fatal("terminal job retained its execution claim")
			}
			mu.Lock()
			for key, values := range claims {
				for _, claim := range values {
					if claim != values[0] {
						t.Errorf("claim changed before terminal delivery: %s %v", key, values)
					}
				}
			}
			retriedKey := "job/" + jobID
			if lostKind == "step" {
				retriedKey = "step/" + jobID + "-second"
			}
			if len(claims[retriedKey]) != 3 {
				t.Errorf("claim was not retried once and completed: %v", claims[retriedKey])
			}
			mu.Unlock()
			if err := executeJobRun(ctx, conf, client, nil, payload); status.Code(err) != codes.FailedPrecondition && status.Code(err) != codes.PermissionDenied {
				t.Fatalf("completed job replay was not rejected: %v", err)
			}
			if got := artifactTestRead(t, filepath.Join(conf.Agent.WorkspaceDir, "claim-run", jobID), "first"); got != "x" {
				t.Fatal("completed job executed again")
			}
			if _, ok := executionClaims.Load("job/" + jobID); ok {
				t.Fatal("definitively rejected initial claim was retained")
			}
		})
	}
}

type unavailableStepClaimClient struct {
	agentv1.AgentServiceClient
	reports []*agentv1.ReportStepRunStatusRequest
	claims  []string
}

func (c *unavailableStepClaimClient) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	c.reports = append(c.reports, req)
	md, _ := metadata.FromOutgoingContext(ctx)
	c.claims = append(c.claims, md.Get(executionClaimMetadataKey)[0])
	if int(req.Status) == 3 {
		return nil, status.Error(codes.Unavailable, "claim status unavailable")
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}

func TestStepClaimRetryHonorsTimeoutAndReportsTerminalWithSameClaim(t *testing.T) {
	client := &unavailableStepClaimClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	payload := &taskpayload.StepRunTaskPayload{PipelineRunID: "run", JobID: "job", StepRunID: "timed-out-claim", Timeout: "20ms", Args: map[string]any{"run": "touch unexpected"}}
	err := executeStepRunViaExecutor(context.Background(), conf, wrapper, nil, payload)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("claim retry ignored deadline: %v", err)
	}
	if len(client.reports) != 2 || int(client.reports[0].Status) != 3 || int(client.reports[1].Status) != 7 {
		t.Fatalf("claim timeout was not reported: %+v", client.reports)
	}
	if client.claims[0] == "" || client.claims[0] != client.claims[1] {
		t.Fatalf("claim changed during timeout: %v", client.claims)
	}
	if _, ok := executionClaims.Load("step/" + payload.StepRunID); ok {
		t.Fatal("acknowledged timeout retained execution claim")
	}
	if _, err := os.Stat(filepath.Join(conf.Agent.WorkspaceDir, "run", "job", "unexpected")); !os.IsNotExist(err) {
		t.Fatal("step executed without confirmed claim")
	}
}
