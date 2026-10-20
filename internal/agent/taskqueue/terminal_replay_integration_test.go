package taskqueue

import (
	"context"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDurableTerminalReplayAfterCommittedAckLossAndRestart(t *testing.T) {
	for _, lostKind := range []string{"step", "job", "queue_ack"} {
		t.Run(lostKind, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "terminal.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			sqlDB.SetMaxOpenConns(1)
			setupWorkerRunFixture(t, ctx, db, "run")
			jobRepo, stepRepo := repo.NewJobRunRepo(workerTestDatabase{db}), repo.NewStepRunRepo(workerTestDatabase{db})
			const agentID = "agent"
			jobID := "durable-" + lostKind
			stepID := jobID + "-step"
			if err := jobRepo.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: agentID, Status: 2}); err != nil {
				t.Fatal(err)
			}
			if err := stepRepo.Create(ctx, &model.StepRun{StepRunID: stepID, JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: agentID, Status: 2}); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			failing := lostKind != "queue_ack"
			terminalRequests := make(map[string][]terminalRecord)
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if values := md.Get("authorization"); len(values) != 1 || values[0] != "bearer test-private-auth-token" {
					return nil, status.Error(codes.Unauthenticated, "missing expected auth")
				}
				var kind, runID string
				var state int32
				var payload []byte
				switch req := request.(type) {
				case *agentv1.ReportJobRunStatusRequest:
					kind, runID, state = "job", req.JobRunId, req.Status
					payload, _ = proto.MarshalOptions{Deterministic: true}.Marshal(req)
				case *agentv1.ReportStepRunStatusRequest:
					kind, runID, state = "step", req.StepRunId, int32(req.Status)
					payload, _ = proto.MarshalOptions{Deterministic: true}.Marshal(req)
				}
				response, err := handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID}), request)
				if err != nil {
					return response, err
				}
				if state >= 4 {
					claims := md.Get(executionClaimMetadataKey)
					if len(claims) != 1 {
						return nil, status.Error(codes.InvalidArgument, "missing claim")
					}
					mu.Lock()
					defer mu.Unlock()
					terminalRequests[kind] = append(terminalRequests[kind], terminalRecord{Kind: kind, RunID: runID, Payload: payload, ClaimID: claims[0]})
					if failing && kind == lostKind {
						return nil, status.Error(codes.Unavailable, "terminal committed but acknowledgement lost")
					}
				}
				return response, nil
			}))
			agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, stepRepo, nil, jobRepo), nil))
			safe.Go(func() { _ = server.Serve(listener) })
			defer server.Stop()
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "test-private-auth-token", ReadWriteTimeout: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: agentID, WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: listener.Addr().String(), Token: "test-private-auth-token"}}
			directory := filepath.Join(conf.Agent.WorkspaceDir, "reports")
			outbox, err := newTerminalOutbox(ctx, directory, agentID, conf.Grpc.ServerAddr)
			if err != nil {
				t.Fatal(err)
			}
			workerCtx := withTerminalOutbox(ctx, outbox)
			payload := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: jobID, AgentID: agentID, Steps: []taskpayload.StepPayload{{StepRunID: stepID, Args: map[string]any{"run": "printf x >> executions"}}}}
			err = executeJobRun(workerCtx, conf, client, nil, payload)
			if lostKind == "queue_ack" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if status.Code(err) != codes.Unavailable || acknowledgeCompleted(err) == nil {
					t.Fatalf("lost terminal ACK incorrectly confirmed task: %v", err)
				}
				if !outbox.has("job", jobID) {
					t.Fatal("job receipt was not durable while a terminal report was pending")
				}
			}
			job, err := jobRepo.GetByJobRunID(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			wantJobState := 4
			if lostKind == "step" {
				wantJobState = 3
			}
			if job.Status != wantJobState {
				t.Fatalf("job terminal overtook step: %+v", job)
			}
			step, err := stepRepo.GetByStepRunID(ctx, stepID)
			if err != nil || step.Status != 4 || step.TerminalReportDigest == "" {
				t.Fatalf("step terminal was not committed: %+v %v", step, err)
			}
			// 模拟进程退出：丢弃所有内存收据及认领缓存，只保留磁盘和真实控制面数据库
			pendingJobReports.Delete(receiptKey(workerCtx, "job", jobID))
			pendingStepReports.Delete(receiptKey(workerCtx, "step", stepID))
			completeExecutionClaim(workerCtx, "job", jobID)
			completeExecutionClaim(workerCtx, "step", stepID)
			if err := outbox.close(); err != nil {
				t.Fatal(err)
			}
			outbox, err = newTerminalOutbox(ctx, directory, agentID, conf.Grpc.ServerAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer outbox.close()
			workerCtx = withTerminalOutbox(ctx, outbox)
			mu.Lock()
			failing = false
			mu.Unlock()
			if err := outbox.replayBound(ctx, client, conf); err != nil {
				t.Fatal(err)
			}
			job, err = jobRepo.GetByJobRunID(ctx, jobID)
			if err != nil || job.Status != 4 || job.TerminalReportDigest == "" {
				t.Fatalf("job replay failed: %+v %v", job, err)
			}
			// 模拟 Kafka 尚未提交消费偏移；服务端拒绝再次取得执行权，不能再次执行 Shell
			if err := executeJobRun(workerCtx, conf, client, nil, payload); acknowledgeCompleted(err) != nil {
				t.Fatalf("completed task redelivery not rejected safely: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", jobID, "executions"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "x" {
				t.Fatalf("command reran after restart: %q", data)
			}
			if outbox.has("job", jobID) || outbox.has("step", stepID) {
				t.Fatal("confirmed replay was not durably removed")
			}
			mu.Lock()
			defer mu.Unlock()
			for kind, requests := range terminalRequests {
				for _, request := range requests[1:] {
					if request.ClaimID != requests[0].ClaimID || string(request.Payload) != string(requests[0].Payload) {
						t.Fatalf("%s retry changed payload or claim", kind)
					}
				}
			}
		})
	}
}
