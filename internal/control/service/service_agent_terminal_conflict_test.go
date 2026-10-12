package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestTerminalConflictGRPCRejectsLateSuccessAndFailureWithoutMutation(t *testing.T) {
	for _, cause := range []string{"cancelled", "timeout", "offline_recovery", "legacy_original_claim", "different_receipt"} {
		for _, kind := range []string{"step", "job"} {
			for _, reported := range []int{4, 5} {
				t.Run(fmt.Sprintf("%s/%s/%d", cause, kind, reported), func(t *testing.T) {
					ctx := context.Background()
					db := terminalTestDB(t, filepath.Join(t.TempDir(), "conflict.db"), true)
					seedTerminalTestRun(t, db, 3)
					storage := artifactServiceDatabase{db}
					jobs := repo.NewJobRunRepo(storage).(*repo.JobRunRepo)
					steps := repo.NewStepRunRepo(storage).(*repo.StepRunRepo)
					request := terminalTestRequest(kind)
					switch req := request.(type) {
					case *agentv1.ReportStepRunStatusRequest:
						req.Status = steprunv1.StepRunStatus(reported)
					case *agentv1.ReportJobRunStatusRequest:
						req.Status = int32(reported)
					}
					authoritative := 5
					receipt := ""
					switch cause {
					case "cancelled":
						authoritative = 6
						if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6, "error_message": "controller cancelled"}); err != nil {
							t.Fatal(err)
						}
					case "timeout":
						authoritative = 7
						for _, entity := range []any{&model.JobRun{}, &model.StepRun{}} {
							if err := db.WithContext(ctx).Model(entity).Where("status = ?", 3).Update("status", 7).Error; err != nil {
								t.Fatal(err)
							}
						}
					case "offline_recovery":
						if _, err := repo.RecoverInterruptedRuns(ctx, db, []string{"run"}, true); err != nil {
							t.Fatal(err)
						}
					case "legacy_original_claim":
						for _, entity := range []any{&model.JobRun{}, &model.StepRun{}} {
							if err := db.WithContext(ctx).Model(entity).Where("status = ?", 3).Update("status", 5).Error; err != nil {
								t.Fatal(err)
							}
						}
					case "different_receipt":
						changed := proto.Clone(request)
						switch r := changed.(type) {
						case *agentv1.ReportStepRunStatusRequest:
							r.ErrorMessage = "different committed payload"
						case *agentv1.ReportJobRunStatusRequest:
							r.ErrorMessage = "different committed payload"
						}
						var err error
						receipt, err = terminalReportDigest(changed, "original-claim", "pipeline", "run", "job")
						if err != nil {
							t.Fatal(err)
						}
						authoritative = reported
						var entity any = &model.JobRun{}
						if kind == "step" {
							entity = &model.StepRun{}
						}
						if err := db.WithContext(ctx).Model(entity).Where("status = ?", 3).Updates(map[string]any{"status": reported, "terminal_report_digest": receipt}).Error; err != nil {
							t.Fatal(err)
						}
					}
					beforeJob, err := jobs.GetByJobRunID(ctx, "job")
					if err != nil {
						t.Fatal(err)
					}
					beforeStep, err := steps.GetByStepRunID(ctx, "step")
					if err != nil {
						t.Fatal(err)
					}
					client, _ := terminalTestClient(t, db, nil)
					for range 2 {
						err := callTerminalReport(client, request, "original-claim")
						info, ok := agentidentity.ParseTerminalConflict(err, request, "original-claim")
						if !ok || info.Kind != kind || info.ClaimSource != "execution_claim" || info.AuthoritativeStatus != authoritative || info.ReceiptDigest != receipt || info.PipelineID != "pipeline" || info.PipelineRunID != "run" || info.JobRunID != "job" || info.Attempt != 1 {
							t.Fatalf("missing exact immutable conflict: %+v %v", info, err)
						}
					}
					afterJob, err := jobs.GetByJobRunID(ctx, "job")
					if err != nil {
						t.Fatal(err)
					}
					afterStep, err := steps.GetByStepRunID(ctx, "step")
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(beforeJob, afterJob) || !reflect.DeepEqual(beforeStep, afterStep) {
						t.Fatal("conflict request mutated authoritative state or pinned an acceptance receipt")
					}
					var artifacts int64
					if err := db.WithContext(ctx).Model(&model.ProjectArtifact{}).Count(&artifacts).Error; err != nil || artifacts != 0 {
						t.Fatal("conflict created artifacts")
					}
				})
			}
		}
	}
}

func TestTerminalConflictGRPCVerifiesUnstartedCancellationAttempt(t *testing.T) {
	for _, brokenParent := range []bool{false, true} {
		t.Run(fmt.Sprint(brokenParent), func(t *testing.T) {
			ctx := context.Background()
			db := terminalTestDB(t, filepath.Join(t.TempDir(), "derived.db"), true)
			seedTerminalTestRun(t, db, 3)
			if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "unused", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 2, Attempt: 2, StepIndex: 1, PreviousStepRunID: "step"}).Error; err != nil {
				t.Fatal(err)
			}
			jobs := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
			if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6}); err != nil {
				t.Fatal(err)
			}
			if brokenParent {
				if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("claim_id", "changed-parent").Error; err != nil {
					t.Fatal(err)
				}
			}
			client, _ := terminalTestClient(t, db, nil)
			request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "unused", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS}
			claim := agentidentity.StepClaimID("original-claim", "unused")
			err := callTerminalReport(client, request, claim)
			info, ok := agentidentity.ParseTerminalConflict(err, request, claim)
			if brokenParent {
				if ok || status.Code(err) != codes.Internal {
					t.Fatalf("changed parent manufactured cancellation proof: %v", err)
				}
			} else if !ok || info.ClaimSource != "cancelled_parent_claim" || info.Attempt != 2 || info.StepIndex != 1 || info.PreviousStepRunID != "step" {
				t.Fatalf("unused attempt proof missing: %+v %v", info, err)
			}
			bad := callTerminalReport(client, request, "unrelated")
			if _, ok := agentidentity.ParseTerminalConflict(bad, request, "unrelated"); ok || status.Code(bad) != codes.PermissionDenied {
				t.Fatal("wrong derived claim manufactured proof")
			}
		})
	}
}

type failingTerminalJobValidator struct{ repo.IJobRunRepository }

func (f failingTerminalJobValidator) ValidateTerminalConflict(context.Context, *model.JobRun, string) error {
	return errors.New("injected database verification failure")
}

type failingTerminalStepValidator struct{ repo.IStepRunRepository }

func (f failingTerminalStepValidator) ValidateTerminalConflict(context.Context, *model.StepRun, string) error {
	return errors.New("injected database verification failure")
}

func TestTerminalConflictNeverClassifiesAuthClaimValidationOrDatabaseFailures(t *testing.T) {
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "invalid.db"), true)
	seedTerminalTestRun(t, db, 5)
	storage := artifactServiceDatabase{db}
	jobs := repo.NewJobRunRepo(storage)
	steps := repo.NewStepRunRepo(storage)
	for _, kind := range []string{"step", "job"} {
		for _, test := range []string{"missing_auth", "wrong_agent", "missing_claim", "wrong_claim", "duplicate_claim", "padded_claim", "padded_run", "nonterminal", "invalid_status", "empty_original_claim", "database_error", "missing_validator"} {
			t.Run(kind+"/"+test, func(t *testing.T) {
				jobRepo, stepRepo := jobs, steps
				if test == "database_error" {
					jobRepo = failingTerminalJobValidator{jobs}
					stepRepo = failingTerminalStepValidator{steps}
				}
				if test == "missing_validator" {
					jobRepo = struct{ repo.IJobRunRepository }{jobs}
					stepRepo = struct{ repo.IStepRunRepository }{steps}
				}
				service := NewAgentServiceImpl(NewAgentService(nil, stepRepo, nil, jobRepo), nil)
				request := terminalTestRequest(kind)
				claims := []string{"original-claim"}
				ctx := context.Background()
				if test != "missing_auth" {
					ctx = agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "agent"})
				}
				switch test {
				case "missing_claim":
					claims = nil
				case "wrong_claim":
					claims = []string{"wrong"}
				case "duplicate_claim":
					claims = []string{"original-claim", "original-claim"}
				case "padded_claim":
					claims = []string{" original-claim "}
				}
				if test == "empty_original_claim" {
					var entity any = &model.JobRun{}
					if kind == "step" {
						entity = &model.StepRun{}
					}
					if err := db.WithContext(ctx).Model(entity).Where("claim_id = ?", "original-claim").Update("claim_id", "").Error; err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_ = db.WithContext(context.Background()).Model(entity).Where("claim_id = ?", "").Update("claim_id", "original-claim").Error
					})
				}
				ctx = metadata.NewIncomingContext(ctx, metadata.MD{"x-arcentra-claim-id": claims})
				var err error
				switch req := request.(type) {
				case *agentv1.ReportJobRunStatusRequest:
					if test == "wrong_agent" {
						req.AgentId = "other"
					}
					if test == "padded_run" {
						req.JobRunId = " job "
					}
					if test == "nonterminal" {
						req.Status = 3
					}
					if test == "invalid_status" {
						req.Status = 99
					}
					_, err = service.ReportJobRunStatus(ctx, req)
				case *agentv1.ReportStepRunStatusRequest:
					if test == "wrong_agent" {
						req.AgentId = "other"
					}
					if test == "padded_run" {
						req.StepRunId = " step "
					}
					if test == "nonterminal" {
						req.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING
					}
					if test == "invalid_status" {
						req.Status = 99
					}
					_, err = service.ReportStepRunStatus(ctx, req)
				}
				if err == nil {
					t.Fatal("invalid request acknowledged")
				}
				if _, ok := agentidentity.ParseTerminalConflict(err, request, "original-claim"); ok {
					t.Fatalf("unverified rejection became nonretryable conflict: %v", err)
				}
				if test == "database_error" && status.Code(err) != codes.Internal {
					t.Fatalf("database error was not retryable: %v", err)
				}
			})
		}
	}
}

// 查询后发生的身份竞态必须保留普通错误，不能隔离原认领
func TestTerminalConflictRepositoryRecheckRejectsConcurrentIdentityChange(t *testing.T) {
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "raced.db"), true)
	seedTerminalTestRun(t, db, 5)
	ctx := agentidentity.WithIdentity(metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-arcentra-claim-id", "original-claim")), agentidentity.Identity{ID: "agent"})
	storage := artifactServiceDatabase{db}
	stepRepo := repo.NewStepRunRepo(storage).(*repo.StepRunRepo)
	stale, err := stepRepo.GetByStepRunID(ctx, "step")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("attempt", 2).Error; err != nil {
		t.Fatal(err)
	}
	request := terminalTestRequest("step")
	err = terminalStepConflict(ctx, stepRepo, stale, request, "original-claim")
	if _, ok := agentidentity.ParseTerminalConflict(err, request, "original-claim"); ok || status.Code(err) != codes.Internal {
		t.Fatalf("stale attempt became proof: %v", err)
	}
}

func TestTerminalConflictGRPCNeverClassifiesVerificationCommitFailure(t *testing.T) {
	for _, kind := range []string{"job", "step"} {
		t.Run(kind, func(t *testing.T) {
			db := terminalTestDB(t, filepath.Join(t.TempDir(), "verification-commit.db"), true)
			seedTerminalTestRun(t, db, 5)
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			pool := &terminalCommitFailPool{DB: sqlDB}
			db.Config.ConnPool, db.Statement.ConnPool = pool, pool
			client, _ := terminalTestClient(t, db, nil)
			request := terminalTestRequest(kind)
			pool.fail.Store(true)
			err = callTerminalReport(client, request, "original-claim")
			if _, ok := agentidentity.ParseTerminalConflict(err, request, "original-claim"); ok || status.Code(err) != codes.Internal {
				t.Fatalf("failed verification transaction produced terminal authority: %v", err)
			}
			err = callTerminalReport(client, request, "original-claim")
			if _, ok := agentidentity.ParseTerminalConflict(err, request, "original-claim"); !ok {
				t.Fatalf("verified retry lost conflict: %v", err)
			}
		})
	}
}

func TestTerminalConflictUnstartedTimeoutAndRecoveryRequireTerminalParentProof(t *testing.T) {
	for _, cause := range []string{"timeout", "recovery"} {
		for _, broken := range []string{"", "empty_parent_claim", "active_parent", "wrong_parent_agent", "wrong_parent_pipeline", "wrong_parent_run", "missing_parent", "wrong_derived_claim"} {
			t.Run(cause+"/"+broken, func(t *testing.T) {
				ctx := context.Background()
				db := terminalTestDB(t, filepath.Join(t.TempDir(), "unstarted-terminal.db"), true)
				seedTerminalTestRun(t, db, 3)
				if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "unused", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 2, Attempt: 2, StepIndex: 1, PreviousStepRunID: "step"}).Error; err != nil {
					t.Fatal(err)
				}
				if cause == "recovery" {
					if _, err := repo.RecoverInterruptedRuns(ctx, db, []string{"run"}, true); err != nil {
						t.Fatal(err)
					}
				} else {
					for _, entity := range []any{&model.JobRun{}, &model.StepRun{}} {
						if err := db.WithContext(ctx).Model(entity).Where("status IN ?", []int{2, 3}).Update("status", 7).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				parentPatch := map[string]any{}
				switch broken {
				case "empty_parent_claim":
					parentPatch["claim_id"] = ""
				case "active_parent":
					parentPatch["status"] = 3
				case "wrong_parent_agent":
					parentPatch["agent_id"] = "other"
				case "wrong_parent_pipeline":
					parentPatch["pipeline_id"] = "other"
				case "wrong_parent_run":
					parentPatch["pipeline_run_id"] = "other"
				case "missing_parent":
					parentPatch["job_run_id"] = "other"
				}
				if len(parentPatch) > 0 {
					if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(parentPatch).Error; err != nil {
						t.Fatal(err)
					}
				}
				request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "unused", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, ErrorMessage: "unused retry attempt"}
				claim := agentidentity.StepClaimID("original-claim", "unused")
				if broken == "wrong_derived_claim" {
					claim = agentidentity.StepClaimID("other-parent", "unused")
				}
				steps := repo.NewStepRunRepo(artifactServiceDatabase{db})
				before, err := steps.GetByStepRunID(ctx, "unused")
				if err != nil {
					t.Fatal(err)
				}
				client, _ := terminalTestClient(t, db, nil)
				err = callTerminalReport(client, request, claim)
				info, ok := agentidentity.ParseTerminalConflict(err, request, claim)
				if broken != "" {
					if ok || err == nil {
						t.Fatalf("unproven parent yielded terminal evidence: %+v %v", info, err)
					}
				} else if !ok || info.ClaimSource != "terminal_parent_claim" || info.Attempt != 2 || info.PreviousStepRunID != "step" {
					t.Fatalf("unused terminal proof missing: %+v %v", info, err)
				}
				after, err := steps.GetByStepRunID(ctx, "unused")
				if err != nil || !reflect.DeepEqual(before, after) || after.ClaimID != "" || after.TerminalReportDigest != "" {
					t.Fatalf("parent-derived refusal manufactured execution or ACK: %+v %v", after, err)
				}
			})
		}
	}
}
