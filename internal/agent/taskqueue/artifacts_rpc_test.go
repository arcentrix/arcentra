package taskqueue

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}, &model.PipelineRun{RunID: "rpc-run", PipelineID: "pipeline"}} {
		if err := db.WithContext(ctx).Create(entity).Error; err != nil {
			t.Fatal(err)
		}
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
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, stepRepo, nil, jobRepo), artifactRPCStorageRepo{}))
	safe.Go(func() { _ = server.Serve(listener) })
	defer server.Stop()
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "artifact-test-token", ReadWriteTimeout: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	st := &memoryArtifactStorage{}
	SetStorage(st, artifactRPCReference())
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
	if stored.Status != 4 || uris["dist/output.txt"] != fmt.Sprintf("artifacts/rpc-run/rpc-producer/rpc-producer-step/%x/dist/output.txt", sha256.Sum256([]byte("persisted artifact"))) {
		t.Fatalf("artifact status/URIs not persisted: %+v", stored)
	}
	consumer := &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "rpc-run", JobRunID: "rpc-consumer", JobName: "consume", AgentID: agentID, ArtifactURIs: map[string]string{"build/dist/output.txt": uris["dist/output.txt"]}, ArtifactReferences: map[string]taskpayload.ArtifactReference{"build/dist/output.txt": artifactRPCReference()}, Steps: []taskpayload.StepPayload{
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

type artifactRPCStorageRepo struct{ repo.IStorageRepository }

func (artifactRPCStorageRepo) Get(_ context.Context, id string) (*model.StorageConfig, error) {
	if id != "rpc-store" {
		return nil, nil
	}
	return &model.StorageConfig{StorageID: "rpc-store", StorageType: "s3", IsEnabled: 1, Config: []byte(`{"endpoint":"https://storage.invalid","bucket":"artifacts","region":"test","basePath":"root","useTLS":true}`)}, nil
}
func artifactRPCReference() taskpayload.ArtifactReference {
	return taskpayload.ArtifactReference{StorageID: "rpc-store", Fingerprint: artifactutil.StorageFingerprint("s3", "https://storage.invalid", "artifacts", "test", "root", true)}
}

func TestRemoteArtifactRPCIdentityOwnershipAndAtomicPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "artifact-security.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []any{&model.Project{ProjectID: "security-project"}, &model.Pipeline{PipelineID: "security-pipeline", ProjectID: "security-project"}, &model.PipelineRun{RunID: "security-run", PipelineID: "security-pipeline"}} {
		if err := db.WithContext(ctx).Create(entity).Error; err != nil {
			t.Fatal(err)
		}
	}
	jobs, steps := repo.NewJobRunRepo(workerTestDatabase{db}), repo.NewStepRunRepo(workerTestDatabase{db})
	const agentID = "security-agent"
	createJob := func(id string) {
		t.Helper()
		if err := jobs.Create(ctx, &model.JobRun{JobRunID: id, PipelineID: "security-pipeline", PipelineRunID: "security-run", AgentID: agentID, Status: 2}); err != nil {
			t.Fatal(err)
		}
		if err := steps.Create(ctx, &model.StepRun{StepRunID: id + "-step", JobRunID: id, PipelineID: "security-pipeline", PipelineRunID: "security-run", AgentID: agentID, Status: 4}); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "bearer security-token" {
			return nil, status.Error(codes.Unauthenticated, "test token required")
		}
		return handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: agentID}), req)
	}))
	agentv1.RegisterAgentServiceServer(server, controlservice.NewAgentServiceImpl(controlservice.NewAgentService(nil, steps, nil, jobs), artifactRPCStorageRepo{}))
	safe.Go(func() { _ = server.Serve(listener) })
	defer server.Stop()
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: listener.Addr().String(), Token: "security-token", ReadWriteTimeout: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func(req *agentv1.ReportJobRunStatusRequest) error {
		t.Helper()
		ctx, cancel := client.WithTimeoutAndAuth(context.Background())
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "x-arcentra-claim-id", req.JobRunId+"-claim")
		response, err := client.AgentClient.ReportJobRunStatus(ctx, req)
		if err == nil && (response == nil || !response.Success) {
			return fmt.Errorf("report rejected")
		}
		return err
	}
	ref := artifactRPCReference()
	claim := func(id string) *agentv1.ReportJobRunStatusRequest {
		return &agentv1.ReportJobRunStatusRequest{AgentId: agentID, JobRunId: id, Status: 3, ArtifactStorageId: ref.StorageID, ArtifactStorageFingerprint: ref.Fingerprint}
	}
	report := func(id string) *agentv1.ReportJobRunStatusRequest {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("content")))
		key := fmt.Sprintf("artifacts/security-run/%s/%s-step/%s/output.txt", id, id, digest)
		return &agentv1.ReportJobRunStatusRequest{AgentId: agentID, JobRunId: id, Status: 4, ArtifactStorageId: ref.StorageID, ArtifactStorageFingerprint: ref.Fingerprint, ArtifactUris: map[string]string{"output.txt": key}, Artifacts: []*agentv1.ArtifactResult{{StepRunId: id + "-step", Name: "output.txt", StorageKey: key, Size: 7, MediaType: "application/octet-stream", Sha256: digest, ExpiresAt: 1900000000}}}
	}
	assertUnchanged := func(id string) {
		t.Helper()
		stored, err := jobs.GetByJobRunID(ctx, id)
		if err != nil || stored.Status != 3 || stored.ArtifactURIs != "" {
			t.Fatalf("failed metadata changed job: %+v %v", stored, err)
		}
		var count int64
		if err := db.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("job_run_id = ?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partial metadata committed: %d %v", count, err)
		}
	}
	createJob("foreign-job")
	for _, test := range []struct {
		name   string
		mutate func(*agentv1.ReportJobRunStatusRequest)
		code   codes.Code
	}{
		{"forged_key", func(r *agentv1.ReportJobRunStatusRequest) {
			r.Artifacts[0].StorageKey = "artifacts/security-run/foreign-job/secret"
		}, codes.InvalidArgument},
		{"foreign_step", func(r *agentv1.ReportJobRunStatusRequest) {
			r.Artifacts[0].StepRunId = "foreign-job-step"
			r.Artifacts[0].StorageKey = fmt.Sprintf("artifacts/security-run/%s/foreign-job-step/%s/output.txt", r.JobRunId, r.Artifacts[0].Sha256)
			r.ArtifactUris["output.txt"] = r.Artifacts[0].StorageKey
		}, codes.Internal},
		{"changed_store", func(r *agentv1.ReportJobRunStatusRequest) { r.ArtifactStorageId = "other-store" }, codes.PermissionDenied},
		{"wrong_agent", func(r *agentv1.ReportJobRunStatusRequest) { r.AgentId = "other-agent" }, codes.PermissionDenied},
		{"bad_digest", func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].Sha256 = "invalid" }, codes.InvalidArgument},
		{"missing_metadata", func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts = nil }, codes.FailedPrecondition},
		{"oversized_report", func(r *agentv1.ReportJobRunStatusRequest) {
			for len(r.Artifacts) <= 1000 {
				r.Artifacts = append(r.Artifacts, r.Artifacts[0])
			}
		}, codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "security-" + test.name
			createJob(id)
			if err := call(claim(id)); err != nil {
				t.Fatal(err)
			}
			req := report(id)
			test.mutate(req)
			if err := call(req); status.Code(err) != test.code {
				t.Fatalf("unexpected report result: %v", err)
			}
			assertUnchanged(id)
		})
	}
	t.Run("changed_config_rejects_claim", func(t *testing.T) {
		id := "changed-config"
		createJob(id)
		req := claim(id)
		req.ArtifactStorageFingerprint = fmt.Sprintf("%064d", 1)
		if err := call(req); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("changed location claim accepted: %v", err)
		}
		stored, _ := jobs.GetByJobRunID(ctx, id)
		if stored.Status != 2 {
			t.Fatal("failed claim changed status")
		}
	})
	t.Run("transaction_retry_and_immutable_callback", func(t *testing.T) {
		id := "atomic-metadata"
		createJob(id)
		if err := call(claim(id)); err != nil {
			t.Fatal(err)
		}
		callbackName := "test-artifact-persistence-failure"
		if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "project_artifact" {
				tx.AddError(fmt.Errorf("simulated metadata write failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		req := report(id)
		if err := call(req); status.Code(err) != codes.Internal {
			t.Fatalf("write failure was acknowledged: %v", err)
		}
		assertUnchanged(id)
		if err := db.Callback().Create().Remove(callbackName); err != nil {
			t.Fatal(err)
		}
		if err := call(req); err != nil {
			t.Fatal(err)
		}
		if err := call(req); err != nil {
			t.Fatalf("identical retry rejected: %v", err)
		}
		var metadataRow model.ProjectArtifact
		if err := db.WithContext(ctx).Where("job_run_id = ?", id).First(&metadataRow).Error; err != nil {
			t.Fatal(err)
		}
		if metadataRow.ProjectID != "security-project" || metadataRow.StorageID != ref.StorageID || metadataRow.StorageFingerprint != ref.Fingerprint || metadataRow.Size != 7 || metadataRow.SHA256 != req.Artifacts[0].Sha256 || metadataRow.ExpiresAt == nil || metadataRow.ExpiresAt.Unix() != 1900000000 {
			t.Fatalf("metadata lost: %+v", metadataRow)
		}
		req.Artifacts[0].Size = 8
		if err := call(req); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("conflicting terminal callback accepted: %v", err)
		}
		req = report(id)
		req.Artifacts[0].Name = "late.txt"
		req.Artifacts[0].StorageKey = strings.TrimSuffix(req.Artifacts[0].StorageKey, "output.txt") + "late.txt"
		req.ArtifactUris = map[string]string{"late.txt": req.Artifacts[0].StorageKey}
		if err := call(req); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("late metadata added: %v", err)
		}
	})
	t.Run("legacy_report_is_not_registered", func(t *testing.T) {
		id := "legacy-artifacts"
		createJob(id)
		req := &agentv1.ReportJobRunStatusRequest{AgentId: agentID, JobRunId: id, Status: 3}
		if err := call(req); err != nil {
			t.Fatal(err)
		}
		req.Status = 4
		req.ArtifactUris = map[string]string{"old.txt": "artifacts/security-run/legacy-artifacts/old.txt"}
		if err := call(req); err != nil {
			t.Fatal(err)
		}
		stored, _ := jobs.GetByJobRunID(ctx, id)
		if stored.Status != 4 || stored.ArtifactReferences != "" || stored.ArtifactStorageID != "" {
			t.Fatalf("legacy artifact guessed location: %+v", stored)
		}
		var count int64
		if err := db.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("job_run_id = ?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("legacy artifact registered: %d %v", count, err)
		}
	})
}

func TestBoundedArtifactReportFitsDefaultGRPCLimit(t *testing.T) {
	id := strings.Repeat("i", 64)
	digest := strings.Repeat("a", 64)
	report := &agentv1.ReportJobRunStatusRequest{AgentId: id, JobRunId: id, Status: 4, ArtifactStorageId: id, ArtifactStorageFingerprint: digest, ArtifactUris: make(map[string]string)}
	for index := 0; index < 1000; index++ {
		name := fmt.Sprintf("%04d/", index) + strings.Repeat("n", 507)
		key := fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", id, id, id, digest, name)
		report.ArtifactUris[name] = key
		report.Artifacts = append(report.Artifacts, &agentv1.ArtifactResult{StepRunId: id, Name: name, StorageKey: key, Size: 1 << 40, MediaType: "application/octet-stream", ExpiresAt: 1900000000, Sha256: digest})
	}
	if size := proto.Size(report); size >= 4*1024*1024 {
		t.Fatalf("valid bounded report exceeds default gRPC message size: %d", size)
	}
}
