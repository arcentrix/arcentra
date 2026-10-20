package taskqueue

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type workerTestDatabase struct{ db *gorm.DB }

func (d workerTestDatabase) Database() *gorm.DB { return d.db }

type capturedLogs struct {
	mu      sync.Mutex
	entries []*logstream.BuildLogMessage
}

func (p *capturedLogs) Publish(_ context.Context, msg *logstream.BuildLogMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := *msg
	p.entries = append(p.entries, &copy)
	return nil
}

func TestJobExecutionControlPlaneContract(t *testing.T) {
	for _, tc := range []struct {
		name, command, timeout         string
		cancel, continued, condition   bool
		wantJob, wantFirst, wantSecond int
	}{
		{name: "success", command: `printf '%s' "$GREETING" > result.txt; printf 'hello'; printf 'warning' >&2`, wantJob: 4, wantFirst: 4, wantSecond: 4},
		{name: "failure", command: "exit 9", wantJob: 5, wantFirst: 5, wantSecond: 8},
		{name: "continued_failure", command: "exit 9", continued: true, wantJob: 4, wantFirst: 5, wantSecond: 4},
		{name: "timeout", command: "sleep 30 & wait", timeout: "100ms", wantJob: 7, wantFirst: 7, wantSecond: 8},
		{name: "cancel", command: "printf 'started'; sleep 30 & wait", cancel: true, wantJob: 6, wantFirst: 6, wantSecond: 6},
		{name: "condition", command: "exit 9", condition: true, wantJob: 4, wantFirst: 8, wantSecond: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runs.db")), &gorm.Config{})
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
			agentID, jobID := "test-agent", "job-"+tc.name
			if err := jobRepo.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: agentID, Status: 2}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := stepRepo.Create(ctx, &model.StepRun{StepRunID: fmt.Sprintf("%s-step-%d", jobID, i), JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", AgentID: agentID, Status: 2}); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if values := md.Get("authorization"); len(values) != 1 || values[0] != "bearer test-token" {
					return nil, status.Error(codes.Unauthenticated, "missing agent token")
				}
				return handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID}), req)
			}))
			agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, stepRepo, nil, jobRepo), nil))
			safe.Go(func() { _ = server.Serve(listener) })
			defer server.Stop()
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "old-token", ReadWriteTimeout: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.UpdateToken("test-token")
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: agentID, WorkspaceDir: t.TempDir()}}
			logs := &capturedLogs{}
			manager := executor.NewExecutorManager()
			manager.Register(executor.NewShellExecutor())
			manager.SetLogPublisher(logs)
			payload := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: jobID, AgentID: agentID, JobName: "build", Env: map[string]string{"GREETING": "from-job"}, Variables: map[string]string{"enabled": "no"}, Steps: []taskpayload.StepPayload{
				{Name: "first", StepRunID: jobID + "-step-0", Args: map[string]any{"run": tc.command}, Timeout: tc.timeout, ContinueOnError: tc.continued},
				{Name: "second", StepRunID: jobID + "-step-1", Args: map[string]any{"run": "printf 'second'"}},
			}}
			if tc.condition {
				payload.Steps[0].When = `variables.enabled == "yes"`
			}
			done := make(chan error, 1)
			started := time.Now()
			safe.Go(func() { done <- executeJobRun(ctx, conf, client, manager, payload) })
			if tc.cancel {
				deadline := time.Now().Add(3 * time.Second)
				for {
					item, err := stepRepo.GetByStepRunID(ctx, jobID+"-step-0")
					if err != nil {
						t.Fatal(err)
					}
					if item.Status == 3 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("step never started")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !CancelJobRun(jobID) {
					t.Fatal("job cancellation was not delivered")
				}
			}
			select {
			case err := <-done:
				if tc.wantJob == 4 && err != nil {
					t.Fatalf("job failed: %v", err)
				}
				if tc.wantJob != 4 && err == nil {
					t.Fatal("expected execution failure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("job did not stop promptly")
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("execution took %s", elapsed)
			}
			job, err := jobRepo.GetByJobRunID(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != tc.wantJob {
				t.Fatalf("job status=%d want=%d error=%s", job.Status, tc.wantJob, job.ErrorMessage)
			}
			if job.StartTime == nil || job.EndTime == nil {
				t.Fatal("job timing was not persisted")
			}
			for i, expected := range []int{tc.wantFirst, tc.wantSecond} {
				step, err := stepRepo.GetByStepRunID(ctx, fmt.Sprintf("%s-step-%d", jobID, i))
				if err != nil {
					t.Fatal(err)
				}
				if step.Status != expected {
					t.Errorf("step %d status=%d want=%d error=%s", i, step.Status, expected, step.ErrorMessage)
				}
				if step.EndTime == nil {
					t.Errorf("step %d has no terminal timestamp", i)
				}
			}
			if tc.name == "success" {
				bytes, err := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", jobID, "result.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if string(bytes) != "from-job" {
					t.Fatalf("environment not preserved: %q", bytes)
				}
				logs.mu.Lock()
				defer logs.mu.Unlock()
				content := ""
				for _, entry := range logs.entries {
					if !strings.HasPrefix(entry.StepRunID, jobID+"-step-") || entry.AgentID != agentID {
						t.Errorf("log lost execution identity: %+v", entry)
					}
					content += entry.Content
				}
				if !strings.Contains(content, "hello") || !strings.Contains(content, "warning") {
					t.Fatalf("missing output logs: %q", content)
				}
				if err := executeJobRun(ctx, conf, client, manager, payload); err == nil {
					t.Fatal("completed job was accepted for execution again")
				}
			}
		})
	}
}

func TestResultToStepRunStatusTimeout(t *testing.T) {
	status, _, _ := resultToStepRunStatus(context.Background(), nil, fmt.Errorf("shell: %w", context.DeadlineExceeded))
	if int(status) != 7 {
		t.Fatalf("timeout mapped to %d", status)
	}
}
