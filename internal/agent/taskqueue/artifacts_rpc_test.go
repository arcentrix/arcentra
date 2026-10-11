package taskqueue

import (
	"context"
	"net"
	"path/filepath"
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
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestArtifactURIsRoundTripThroughControlPlaneRPC(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "artifacts.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	jobRepo, stepRepo := repo.NewJobRunRepo(workerTestDatabase{db}), repo.NewStepRunRepo(workerTestDatabase{db})
	const agentID = "artifact-agent"
	for _, jobID := range []string{"rpc-producer", "rpc-consumer"} {
		if err := jobRepo.Create(ctx, &model.JobRun{JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "rpc-run", AgentID: agentID, Status: 2}); err != nil {
			t.Fatal(err)
		}
		if err := stepRepo.Create(ctx, &model.StepRun{StepRunID: jobID + "-step", JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "rpc-run", AgentID: agentID, Status: 2}); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if values := md.Get("authorization"); len(values) != 1 || values[0] != "bearer artifact-test-token" {
			return nil, status.Error(codes.Unauthenticated, "missing test token")
		}
		return handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID}), req)
	}))
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, stepRepo, nil, jobRepo), nil))
	safe.Go(func() { _ = server.Serve(listener) })
	defer server.Stop()
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "artifact-test-token", ReadWriteTimeout: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	st := &memoryArtifactStorage{}
	SetStorage(st)
	t.Cleanup(func() { SetStorage(nil) })
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: agentID, WorkspaceDir: t.TempDir()}}
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "dist/output.txt", "persisted artifact")
	producer := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "rpc-run", JobRunID: "rpc-producer", JobName: "build", AgentID: agentID, Workspace: workspace, Steps: []taskpayload.StepPayload{
		{Name: "upload", StepRunID: "rpc-producer-step", Uses: "builtin:artifacts", Action: "upload", Args: map[string]any{"paths": []string{"dist"}}},
	}}
	if err := executeJobRun(ctx, conf, client, nil, producer); err != nil {
		t.Fatal(err)
	}
	stored, err := jobRepo.GetByJobRunID(ctx, producer.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	var uris map[string]string
	if err := sonic.UnmarshalString(stored.ArtifactURIs, &uris); err != nil {
		t.Fatal(err)
	}
	if stored.Status != 4 || uris["dist/output.txt"] != "artifacts/rpc-run/rpc-producer/dist/output.txt" {
		t.Fatalf("artifact status/URIs not persisted: %+v", stored)
	}
	consumer := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "rpc-run", JobRunID: "rpc-consumer", JobName: "consume", AgentID: agentID, ArtifactURIs: map[string]string{"build/dist/output.txt": uris["dist/output.txt"]}, Steps: []taskpayload.StepPayload{
		{Name: "download", StepRunID: "rpc-consumer-step", Uses: "builtin:artifacts", Action: "download", Args: map[string]any{"job": "build", "paths": []string{"dist/*.txt"}}},
	}}
	if err := executeJobRun(ctx, conf, client, nil, consumer); err != nil {
		t.Fatal(err)
	}
	if got := artifactTestRead(t, filepath.Join(conf.Agent.WorkspaceDir, "rpc-run", "rpc-consumer"), "dist/output.txt"); got != "persisted artifact" {
		t.Fatal(got)
	}
	for _, jobID := range []string{producer.JobRunID, consumer.JobRunID} {
		job, err := jobRepo.GetByJobRunID(ctx, jobID)
		if err != nil || job.Status != 4 {
			t.Fatalf("terminal job state missing: %+v %v", job, err)
		}
		step, err := stepRepo.GetByStepRunID(ctx, jobID+"-step")
		if err != nil || step.Status != 4 {
			t.Fatalf("terminal artifact step state missing: %+v %v", step, err)
		}
	}
}
