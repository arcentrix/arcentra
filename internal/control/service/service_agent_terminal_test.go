// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/safe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func terminalTestDB(t *testing.T, path string, migrate bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if migrate {
		if err := db.WithContext(context.Background()).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedTerminalTestRun(t *testing.T, db *gorm.DB, current int) {
	t.Helper()
	claim := ""
	if current >= 3 {
		claim = "original-claim"
	}
	for _, value := range []any{
		&model.Project{ProjectID: "project"},
		&model.Pipeline{ProjectID: "project", PipelineID: "pipeline"},
		&model.PipelineRun{RunID: "run", PipelineID: "pipeline", ProjectID: "project", Status: model.PipelineStatusRunning},
		&model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: claim, Status: current},
		&model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: claim, Status: current},
	} {
		if err := db.WithContext(context.Background()).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func terminalTestClient(t *testing.T, db *gorm.DB, after func(proto.Message) error) (agentv1.AgentServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		response, err := handler(agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "agent"}), req)
		if err == nil && after != nil {
			err = after(req.(proto.Message))
		}
		if err != nil {
			return nil, err
		}
		return response, nil
	}))
	storage := artifactServiceDatabase{db}
	agentv1.RegisterAgentServiceServer(server, NewAgentServiceImpl(NewAgentService(nil, repo.NewStepRunRepo(storage), nil, repo.NewJobRunRepo(storage)), nil))
	safe.Go(func() { _ = server.Serve(listener) })
	conn, err := grpc.NewClient("passthrough:///terminal-test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	closeServer := func() { _ = conn.Close(); server.Stop(); _ = listener.Close() }
	t.Cleanup(closeServer)
	return agentv1.NewAgentServiceClient(conn), closeServer
}

func callTerminalReport(client agentv1.AgentServiceClient, request proto.Message, claim string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if claim != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-arcentra-claim-id", claim)
	}
	switch req := request.(type) {
	case *agentv1.ReportJobRunStatusRequest:
		response, err := client.ReportJobRunStatus(ctx, req)
		if err == nil && !response.GetSuccess() {
			return errors.New("job report not acknowledged")
		}
		return err
	case *agentv1.ReportStepRunStatusRequest:
		response, err := client.ReportStepRunStatus(ctx, req)
		if err == nil && !response.GetSuccess() {
			return errors.New("step report not acknowledged")
		}
		return err
	}
	return errors.New("unexpected test request")
}

func terminalTestRequest(kind string) proto.Message {
	if kind == "step" {
		return &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, ExitCode: -2147483648, ErrorMessage: "original result", StartTime: 2200000000, EndTime: 2200000005, Metrics: map[string]string{"workspace": "/work", "count": "9007199254740993"}}
	}
	return &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 5, ErrorMessage: "original result", StartTime: 2200000000, EndTime: 2200000005}
}

func TestTerminalReportGRPCLostAckReopensDatabaseAndRejectsConflicts(t *testing.T) {
	for _, kind := range []string{"job", "step"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reports.db")
			db := terminalTestDB(t, path, true)
			seedTerminalTestRun(t, db, 2)
			if kind != "step" {
				if err := db.WithContext(context.Background()).Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("status", 4).Error; err != nil {
					t.Fatal(err)
				}
			}
			request := terminalTestRequest(kind)
			client, stop := terminalTestClient(t, db, func(proto.Message) error {
				return status.Error(codes.Unavailable, "response lost after database commit")
			})
			if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.Unavailable {
				t.Fatalf("lost acknowledgment: %v", err)
			}
			stop()
			sqlDB, _ := db.DB()
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			// 重新打开磁盘数据库与 gRPC 服务，不能依赖原进程内存确认结果
			db = terminalTestDB(t, path, false)
			client, _ = terminalTestClient(t, db, nil)
			var committedAt time.Time
			if err := db.WithContext(context.Background()).Table(kind+"_run").Select("updated_at").Where(kind+"_run_id = ?", kind).Scan(&committedAt).Error; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := callTerminalReport(client, proto.Clone(request), "original-claim"); err != nil {
					t.Fatalf("identical replay: %v", err)
				}
			}
			var afterReplay time.Time
			if err := db.WithContext(context.Background()).Table(kind+"_run").Select("updated_at").Where(kind+"_run_id = ?", kind).Scan(&afterReplay).Error; err != nil {
				t.Fatal(err)
			}
			if !afterReplay.Equal(committedAt) {
				t.Fatal("identical replay rewrote terminal record")
			}
			for _, claim := range []string{"stale-claim", ""} {
				if err := callTerminalReport(client, request, claim); err == nil {
					t.Fatalf("different or absent claim acknowledged: %q", claim)
				}
			}
			for _, field := range []string{"error_message", "start_time", "end_time", "agent_id", "status", "run", "details"} {
				changed := proto.Clone(request)
				want := codes.FailedPrecondition
				switch value := changed.(type) {
				case *agentv1.ReportJobRunStatusRequest:
					switch field {
					case "error_message":
						value.ErrorMessage = "different"
					case "start_time":
						value.StartTime++
					case "end_time":
						value.EndTime++
					case "agent_id":
						value.AgentId = "other"
						want = codes.PermissionDenied
					case "status":
						value.Status = 4
					case "run":
						value.JobRunId = "unknown"
						want = codes.NotFound
					case "details":
						value.ArtifactUris = map[string]string{"late": "artifacts/run/job/late"}
					}
				case *agentv1.ReportStepRunStatusRequest:
					switch field {
					case "error_message":
						value.ErrorMessage = "different"
					case "start_time":
						value.StartTime++
					case "end_time":
						value.EndTime++
					case "agent_id":
						value.AgentId = "other"
						want = codes.PermissionDenied
					case "status":
						value.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS
					case "run":
						value.StepRunId = "unknown"
						want = codes.NotFound
					case "details":
						value.ExitCode = 2147483647
					}
				}
				if err := callTerminalReport(client, changed, "original-claim"); status.Code(err) != want {
					t.Errorf("changed %s: %v, want %s", field, err, want)
				}
			}
			if kind == "step" {
				var row model.StepRun
				if err := db.WithContext(context.Background()).Where("step_run_id = ?", "step").First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.ClaimID != "original-claim" || len(row.TerminalReportDigest) != 64 || row.Status != 5 || row.ErrorMessage != "original result" || row.ExitCode == nil || *row.ExitCode != -2147483648 || row.StartTime.Unix() != 2200000000 || row.EndTime.Unix() != 2200000005 || row.Duration != 5000 {
					t.Fatalf("step identity or numeric outcome changed: %+v", row)
				}
			} else {
				var row model.JobRun
				if err := db.WithContext(context.Background()).Where("job_run_id = ?", "job").First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.ClaimID != "original-claim" || len(row.TerminalReportDigest) != 64 || row.Status != 5 || row.ErrorMessage != "original result" || row.StartTime.Unix() != 2200000000 || row.EndTime.Unix() != 2200000005 || row.Duration != 5000 {
					t.Fatalf("job identity or timestamps changed: %+v", row)
				}
			}
		})
	}
}

func TestTerminalReportCannotAcknowledgeOfflineRecoveryOrLegacyTerminal(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprint(recovered), func(t *testing.T) {
			db := terminalTestDB(t, filepath.Join(t.TempDir(), "recover.db"), true)
			initial := 5
			if recovered {
				initial = 3
			}
			seedTerminalTestRun(t, db, initial)
			if recovered {
				if _, err := repo.RecoverInterruptedRuns(context.Background(), db, []string{"run"}, true); err != nil {
					t.Fatal(err)
				}
			}
			client, _ := terminalTestClient(t, db, nil)
			for _, kind := range []string{"job", "step"} {
				request := terminalTestRequest(kind)
				if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.FailedPrecondition {
					t.Errorf("%s acknowledged uncommitted FAILED: %v", kind, err)
				}
				switch value := request.(type) {
				case *agentv1.ReportJobRunStatusRequest:
					value.Status = 3
				case *agentv1.ReportStepRunStatusRequest:
					value.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING
				}
				if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.FailedPrecondition {
					t.Errorf("%s revived terminal: %v", kind, err)
				}
			}
			var job model.JobRun
			if err := db.WithContext(context.Background()).Where("job_run_id = ?", "job").First(&job).Error; err != nil {
				t.Fatal(err)
			}
			if job.Status != 5 || job.TerminalReportDigest != "" || recovered && !strings.Contains(job.ErrorMessage, "reconciled offline") {
				t.Fatalf("recovery overwritten: %+v", job)
			}
		})
	}
}

type terminalCommitFailPool struct {
	*sql.DB
	fail atomic.Bool
}

func (p *terminalCommitFailPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &terminalCommitFailTx{Tx: tx, pool: p}, nil
}

type terminalCommitFailTx struct {
	*sql.Tx
	pool *terminalCommitFailPool
}

func (t *terminalCommitFailTx) Commit() error {
	if t.pool.fail.Swap(false) {
		_ = t.Tx.Rollback()
		return errors.New("injected transaction commit failure")
	}
	return t.Tx.Commit()
}

func TestTerminalReportGRPCNeverAcknowledgesFailedCommit(t *testing.T) {
	for _, kind := range []string{"job", "step", "artifacts"} {
		t.Run(kind, func(t *testing.T) {
			db := terminalTestDB(t, filepath.Join(t.TempDir(), "commit.db"), true)
			seedTerminalTestRun(t, db, 2)
			if kind != "step" {
				if err := db.WithContext(context.Background()).Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("status", 4).Error; err != nil {
					t.Fatal(err)
				}
			}
			request := terminalTestRequest(kind)
			if kind == "artifacts" {
				fingerprint, digest := strings.Repeat("a", 64), strings.Repeat("b", 64)
				if err := db.WithContext(context.Background()).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"artifact_storage_id": "store", "artifact_storage_fingerprint": fingerprint, "artifact_storage_type": "s3"}).Error; err != nil {
					t.Fatal(err)
				}
				value := request.(*agentv1.ReportJobRunStatusRequest)
				value.ArtifactStorageId, value.ArtifactStorageFingerprint = "store", fingerprint
				key := "artifacts/run/job/step/" + digest + "/result"
				value.ArtifactUris = map[string]string{"result": key}
				value.Artifacts = []*agentv1.ArtifactResult{{StepRunId: "step", Name: "result", StorageKey: key, Size: 1<<54 + 3, Sha256: digest, MediaType: "application/octet-stream", ExpiresAt: 2300000000}}
			}
			sqlDB, _ := db.DB()
			pool := &terminalCommitFailPool{DB: sqlDB}
			db.Config.ConnPool, db.Statement.ConnPool = pool, pool
			client, _ := terminalTestClient(t, db, nil)
			pool.fail.Store(true)
			if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.Internal {
				t.Fatalf("failed commit acknowledged: %v", err)
			}
			var state struct {
				Status                        int
				ClaimID, TerminalReportDigest string
			}
			table, column := "job_run", "job_run_id"
			if kind == "step" {
				table, column = "step_run", "step_run_id"
			}
			if err := db.WithContext(context.Background()).Table(table).Select("status", "claim_id", "terminal_report_digest").Where(column+" = ?", strings.TrimSuffix(table, "_run")).Scan(&state).Error; err != nil {
				t.Fatal(err)
			}
			if state.Status != 2 || state.ClaimID != "" || state.TerminalReportDigest != "" {
				t.Fatalf("failed commit was partially visible: %+v", state)
			}
			var count int64
			if err := db.WithContext(context.Background()).Model(&model.ProjectArtifact{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("artifact escaped rollback: %d %v", count, err)
			}
			for range 2 {
				if err := callTerminalReport(client, request, "original-claim"); err != nil {
					t.Fatalf("retry after commit failure: %v", err)
				}
			}
			if kind == "artifacts" {
				var artifact model.ProjectArtifact
				if err := db.WithContext(context.Background()).Where("job_run_id = ?", "job").First(&artifact).Error; err != nil {
					t.Fatal(err)
				}
				if artifact.Size != 1<<54+3 || artifact.ExpiresAt == nil || artifact.ExpiresAt.Unix() != 2300000000 {
					t.Fatalf("artifact integer precision lost: %+v", artifact)
				}
				// 清理历史制品后原回执仍成立，但不能靠重放重新登记已经删除的制品
				if err := db.WithContext(context.Background()).Where("artifact_id = ?", artifact.ArtifactID).Delete(&model.ProjectArtifact{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := callTerminalReport(client, request, "original-claim"); err != nil {
					t.Fatalf("committed receipt lost after artifact cleanup: %v", err)
				}
				if err := db.WithContext(context.Background()).Model(&model.ProjectArtifact{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("replay recreated cleaned artifact: %d %v", count, err)
				}
			}
		})
	}
}

func TestRetryClaimRecheckStopsAfterParentCancellationButTerminalReplayStillAcks(t *testing.T) {
	ctx := context.Background()
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "retry-claim.db"), true)
	seedTerminalTestRun(t, db, 2)
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 3).Error; err != nil {
		t.Fatal(err)
	}
	policy := `{"mode":"failed_step","max_attempts":3,"idempotent":true,"allowed_exit_codes":[9]}`
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "step").Updates(map[string]any{"attempt": 1, "retry_mode": "failed_step", "retry_policy": policy}).Error; err != nil {
		t.Fatal(err)
	}
	client, _ := terminalTestClient(t, db, nil)
	claim := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING}
	for range 2 {
		if err := callTerminalReport(client, claim, "original-claim"); err != nil {
			t.Fatalf("active original claim rejected: %v", err)
		}
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6).Error; err != nil {
		t.Fatal(err)
	}
	if err := callTerminalReport(client, claim, "original-claim"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("same-claim ACK bypassed parent fence: %v", err)
	}
	terminal := terminalTestRequest("step")
	for range 2 {
		if err := callTerminalReport(client, terminal, "original-claim"); err != nil {
			t.Fatalf("original terminal receipt cannot drain after cancellation: %v", err)
		}
	}
}
