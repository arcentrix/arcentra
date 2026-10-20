package taskqueue

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAgentRetryRealProcessRPCAndSQLite(t *testing.T) {
	for _, name := range []string{"failed_step", "whole_job", "failed_step_ack_loss"} {
		t.Run(name, func(t *testing.T) {
			mode := strings.TrimSuffix(name, "_ack_loss")
			lostAck := strings.HasSuffix(name, "_ack_loss")
			var lose atomic.Bool
			lose.Store(lostAck)
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "attempts.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			defer sqlDB.Close()
			sqlDB.SetMaxOpenConns(1)
			if err = db.WithContext(ctx).AutoMigrate(&model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}); err != nil {
				t.Fatal(err)
			}
			jobs, steps := repo.NewJobRunRepo(workerTestDatabase{db}), repo.NewStepRunRepo(workerTestDatabase{db})
			jobID := "retry-" + name
			if err = jobs.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 2, TotalSteps: 3}); err != nil {
				t.Fatal(err)
			}
			payload := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: jobID, JobName: "build", AgentID: "agent", Retry: &pipelinev1.Retry{Mode: mode, MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}}
			commands := []string{"printf x >> first; echo first", "printf x >> flaky; echo flaky; if [ ! -f failed ]; then touch failed; exit 9; fi", "printf x >> last; echo last"}
			for i, name := range []string{"first", "flaky", "last"} {
				step := taskpayload.StepPayload{Name: name, StepIndex: int32(i), Uses: "shell", Args: map[string]any{"run": commands[i]}}
				policyJSON, _ := sonic.MarshalString(payload.Retry)
				previous := ""
				for attempt := 1; attempt <= 3; attempt++ {
					id := fmt.Sprintf("%s-%s-%d", jobID, name, attempt)
					step.AttemptRunIDs = append(step.AttemptRunIDs, id)
					if attempt == 1 {
						step.StepRunID = id
					}
					if err = steps.Create(ctx, &model.StepRun{StepRunID: id, JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Name: name, StepIndex: i, Attempt: attempt, RetryMode: mode, RetryPolicy: policyJSON, PreviousStepRunID: previous, Status: 2}); err != nil {
						t.Fatal(err)
					}
					previous = id
				}
				payload.Steps = append(payload.Steps, step)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				result, err := handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "agent"}), req)
				if report, ok := req.(*agentv1.ReportStepRunStatusRequest); ok && int(report.Status) == 5 && err == nil && lose.CompareAndSwap(true, false) {
					return nil, grpcstatus.Error(codes.Unavailable, "failed attempt committed but ACK lost")
				}
				return result, err
			}))
			agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, steps, nil, jobs), nil))
			safe.Go(func() { _ = server.Serve(listener) })
			defer server.Stop()
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), ReadWriteTimeout: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: listener.Addr().String()}}
			box, err := newTerminalOutbox(ctx, filepath.Join(t.TempDir(), "reports"), "agent", conf.Grpc.ServerAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer box.close()
			workerCtx := withTerminalOutbox(ctx, box)
			logs := &capturedLogs{}
			manager := executor.NewExecutorManager()
			manager.Register(executor.NewShellExecutor())
			manager.SetLogPublisher(logs)
			if err = executeJobRun(workerCtx, conf, client, manager, payload); (err != nil) != lostAck {
				t.Fatalf("unexpected execution result: %v", err)
			}
			wantFirst := "x"
			if mode == "whole_job" {
				wantFirst = "xx"
			}
			wantFlaky, wantLast := "xx", "x"
			if lostAck {
				wantFlaky, wantLast = "x", ""
			}
			for name, want := range map[string]string{"first": wantFirst, "flaky": wantFlaky, "last": wantLast} {
				got, err := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", jobID, name))
				if want == "" {
					if !os.IsNotExist(err) {
						t.Fatalf("later step ran after failed-attempt ACK loss: %v", err)
					}
					continue
				}
				if err != nil || string(got) != want {
					t.Fatalf("%s=%q %v want %q", name, got, err, want)
				}
			}
			for _, step := range payload.Steps {
				for i, id := range step.AttemptRunIDs {
					item, err := steps.GetByStepRunID(ctx, id)
					if err != nil || item.Status < 4 || item.TerminalReportDigest == "" {
						t.Fatalf("attempt is unfinished: %+v %v", item, err)
					}
					if step.Name == "flaky" && i == 0 && (item.Status != 5 || item.ExitCode == nil || *item.ExitCode != 9) {
						t.Fatalf("original failure overwritten: %+v", item)
					}
				}
			}
			job, err := jobs.GetByJobRunID(ctx, jobID)
			wantStatus := 4
			if lostAck {
				wantStatus = 5
			}
			if err != nil || job.Status != wantStatus {
				t.Fatalf("job not successful %+v %v", job, err)
			}
			logged := map[string]bool{}
			for _, entry := range logs.entries {
				logged[entry.StepRunID] = true
			}
			if !logged[payload.Steps[1].AttemptRunIDs[0]] || !lostAck && !logged[payload.Steps[1].AttemptRunIDs[1]] {
				t.Fatalf("attempt log identities lost: %+v", logged)
			}
			// Queue redelivery after terminal completion must never execute attempts again.
			if err = executeJobRun(workerCtx, conf, client, manager, payload); acknowledgeCompleted(err) != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", jobID, "flaky"))
			if string(got) != wantFlaky {
				t.Fatalf("redelivery reran failure: %q", got)
			}
		})
	}
}
