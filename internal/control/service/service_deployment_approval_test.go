package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

func deploymentApprovalFixture(t *testing.T) (*DeploymentService, *gorm.DB, PipelineDeploymentExecution, *releaseFakeAdapter, *ApprovalService) {
	t.Helper()
	s, db, execution, adapter := pipelineReleaseFixture(t)
	if err := db.AutoMigrate(&model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	binding := s.targets["staging"]
	binding.PipelineApproval = &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer", "user"}, MinApprovals: 1, TimeoutSeconds: 30}
	binding.Fingerprint = deploymentTargetFingerprint("staging", binding, "")
	s.targets["staging"] = binding
	run, err := s.pipelines.GetRun(context.Background(), execution.PipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cluster.ParsePipelinePlan(run.DeploymentPlan, run.DeploymentPlanHash)
	if err != nil {
		t.Fatal(err)
	}
	plan.ApprovalRequired = true
	data, hash, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", execution.PipelineRunID).Updates(map[string]any{"deployment_plan": data, "deployment_plan_hash": hash, "deployment_target_fingerprint": binding.Fingerprint}).Error; err != nil {
		t.Fatal(err)
	}
	decisions := repo.NewApprovalDecisionRepo(artifactServiceDatabase{db})
	authorize := func(context.Context, string, string, string, string) error { return nil }
	s.ConfigureDeploymentApprovals(decisions, authorize, authorize)
	approvals := NewApprovalService(s.approvals, decisions, nil)
	approvals.SetDecisionValidator(s.ValidateApprovalDecision)
	return s, db, execution, adapter, approvals
}

func prepareApprovedDeployment(t *testing.T, s *DeploymentService, approvals *ApprovalService, execution PipelineDeploymentExecution) *model.DeploymentRelease {
	t.Helper()
	entry, err := s.PreparePipelineRelease(context.Background(), execution)
	if err != nil || entry.ApprovalID == nil {
		t.Fatalf("prepare: %+v %v", entry, err)
	}
	if err := approvals.Approve(context.Background(), *entry.ApprovalID, "reviewer", "Reviewed immutable target and image"); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestDeploymentApprovalAtomicallyPinsExactEvidenceAndSurvivesServiceRestart(t *testing.T) {
	s, db, execution, adapter, approvals := deploymentApprovalFixture(t)
	ctx := context.Background()
	entry, err := s.PreparePipelineRelease(ctx, execution)
	if err != nil || entry.ApprovalID == nil {
		t.Fatalf("prepare: %+v %v", entry, err)
	}
	req, err := approvals.GetApproval(ctx, *entry.ApprovalID)
	if err != nil || req.DeploymentReleaseID == nil || *req.DeploymentReleaseID != entry.ReleaseID || req.CandidateApprovers != `["reviewer"]` || req.EvidenceHash != entry.ApprovalEvidenceHash || req.ExpiresAt == nil || len(entry.ApprovalTarget) == 0 {
		t.Fatalf("unbound approval: %+v %v", req, err)
	}
	if _, err := s.advance(ctx, entry.ProjectID, entry.ReleaseID, execution.ActorID, true, false); !errors.Is(err, ErrDeploymentApproval) {
		t.Fatalf("pending gate advanced: %v", err)
	}
	if adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("pending approval contacted cluster")
	}
	if err := approvals.Approve(ctx, req.ApprovalID, "user", "self nomination"); err == nil {
		t.Fatal("requester self approved")
	}
	restarted := NewDeploymentService(s.records, s.pipelines, s.artifacts, s.files, s.approvals, s.targets)
	restarted.ConfigurePipelineDeployments(s.pipelineJobs, s.pipelineSteps, s.pipelineAuthorize)
	restarted.ConfigureDeploymentApprovals(s.approvalDecisions, s.approvalAuthorize, s.approvalRejectAuthorize)
	same, err := restarted.PreparePipelineRelease(ctx, execution)
	if err != nil || same.ReleaseID != entry.ReleaseID || same.ApprovalID == nil || *same.ApprovalID != req.ApprovalID {
		t.Fatalf("restart replaced evidence: %+v %v", same, err)
	}
	again, _ := restarted.approvals.GetByApprovalID(ctx, req.ApprovalID)
	if !again.ExpiresAt.Equal(*req.ExpiresAt) || again.EvidenceHash != req.EvidenceHash {
		t.Fatal("restart renewed approval lifetime")
	}
	// JSON columns may be reserialized by MySQL. Whitespace/key order must not change evidence.
	var target map[string]any
	if sonic.Unmarshal(entry.ApprovalTarget, &target) != nil {
		t.Fatal("target snapshot not JSON")
	}
	reserialized := `{"container":"app","deployment":"web","environmentId":"` + entry.EnvironmentID + `","namespace":"staging","projectId":"project","targetRef":"staging"}`
	if err := db.Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Update("approval_target", reserialized).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", req.ApprovalID).Update("candidate_approvers", `[ "reviewer" ]`).Error; err != nil {
		t.Fatal(err)
	}
	if err := approvals.Approve(ctx, req.ApprovalID, "reviewer", "reviewed"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetRelease(ctx, entry.ProjectID, entry.ReleaseID)
	if err := s.verifyDeploymentApproval(ctx, current); err != nil {
		t.Fatalf("JSON normalization invalidated identical evidence: %v", err)
	}
	if adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("approval decision or service construction deployed")
	}
}

func TestDeploymentApprovalCreationFailureRollsBackReleaseStepAndReservation(t *testing.T) {
	s, db, execution, adapter, _ := deploymentApprovalFixture(t)
	if err := db.Callback().Create().Before("gorm:create").Register("fail_approval_insert", func(tx *gorm.DB) {
		if tx.Statement.Table == "approval_request" {
			tx.AddError(errors.New("injected approval write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove("fail_approval_insert")
	if _, err := s.PreparePipelineRelease(context.Background(), execution); err == nil {
		t.Fatal("approval persistence failure accepted")
	}
	var count int64
	db.Model(&model.DeploymentRelease{}).Count(&count)
	if count != 0 {
		t.Fatal("release escaped transaction rollback")
	}
	step, _ := s.pipelineSteps.GetByStepRunID(context.Background(), execution.StepRunID)
	if step.DeploymentReleaseID != "" {
		t.Fatal("step link escaped rollback")
	}
	environments, _, _ := s.records.ListEnvironments(context.Background(), execution.ProjectID, 0, 10)
	if len(environments) != 1 || environments[0].ActiveReleaseID != "" || adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("failed gate reserved environment or contacted cluster")
	}
}

func TestDeploymentApprovalInvalidationBlocksFirstApplyAndRecovery(t *testing.T) {
	for _, change := range []string{"revoked reviewer", "deleted vote", "tampered artifact", "changed target", "changed quorum", "changed candidates", "changed evidence", "cancelled run"} {
		t.Run(change, func(t *testing.T) {
			s, db, execution, adapter, approvals := deploymentApprovalFixture(t)
			entry := prepareApprovedDeployment(t, s, approvals, execution)
			switch change {
			case "revoked reviewer":
				s.approvalAuthorize = func(context.Context, string, string, string, string) error { return errors.New("revoked") }
			case "deleted vote":
				db.Where("approval_id = ?", *entry.ApprovalID).Delete(&model.ApprovalDecision{})
			case "tampered artifact":
				db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", entry.ArtifactID).Update("sha256", strings.Repeat("f", 64))
			case "changed target":
				binding := s.targets["staging"]
				binding.Fingerprint = strings.Repeat("c", 64)
				s.targets["staging"] = binding
			case "changed quorum":
				db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", *entry.ApprovalID).Update("required_approver_count", 2)
			case "changed candidates":
				db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", *entry.ApprovalID).Update("candidate_approvers", `["other"]`)
			case "changed evidence":
				db.Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Update("approval_evidence_hash", strings.Repeat("e", 64))
			case "cancelled run":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", execution.PipelineRunID).Update("status", model.PipelineStatusCancelled)
			}
			if _, err := s.advance(context.Background(), entry.ProjectID, entry.ReleaseID, execution.ActorID, true, false); err == nil {
				t.Fatal("invalid approval applied")
			}
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("invalid gate accessed cluster")
			}
			if change == "cancelled run" {
				return
			} // Explicit recovery of an already-applying release has separate authority, never resumes this run.
			db.Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Updates(map[string]any{"status": "applying", "workload_uid": "workload", "baseline_generation": 7})
			if _, err := s.RecoverAuthorized(context.Background(), entry.ProjectID, entry.ReleaseID, "operator"); err == nil {
				t.Fatal("invalid approval recovered a possible PATCH")
			}
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("invalid recovery accessed cluster")
			}
		})
	}
}

func TestDeploymentApprovalCancellationCannotResumeOrRewriteDecision(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmtBool(approved), func(t *testing.T) {
			s, _, execution, adapter, approvals := deploymentApprovalFixture(t)
			entry, err := s.PreparePipelineRelease(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			if approved {
				if err := approvals.Approve(context.Background(), *entry.ApprovalID, "reviewer", "reviewed"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Cancel(context.Background(), entry.ProjectID, entry.ReleaseID); err != nil {
				t.Fatal(err)
			}
			req, _ := approvals.GetApproval(context.Background(), *entry.ApprovalID)
			want := model.ApprovalStatusCanceled
			if approved {
				want = model.ApprovalStatusApproved
			}
			if req.Status != want {
				t.Fatalf("approval history rewritten: %d", req.Status)
			}
			s.advance(context.Background(), entry.ProjectID, entry.ReleaseID, execution.ActorID, true, false)
			s.RecoverAuthorized(context.Background(), entry.ProjectID, entry.ReleaseID, "operator")
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("cancelled release was resumed")
			}
			env, _ := s.records.GetEnvironment(context.Background(), entry.ProjectID, entry.EnvironmentID)
			if env.ActiveReleaseID != "" {
				t.Fatal("cancelled queued release retained reservation")
			}
		})
	}
}

func fmtBool(value bool) string {
	if value {
		return "approved"
	}
	return "pending"
}

func TestDeploymentApprovalExpiryPreventsRecoveryWithoutRenewingEvidence(t *testing.T) {
	s, db, execution, adapter, approvals := deploymentApprovalFixture(t)
	binding := s.targets["staging"]
	binding.PipelineApproval.TimeoutSeconds = 1
	binding.Fingerprint = deploymentTargetFingerprint("staging", binding, "")
	s.targets["staging"] = binding
	db.Model(&model.PipelineRun{}).Where("run_id = ?", execution.PipelineRunID).Update("deployment_target_fingerprint", binding.Fingerprint)
	entry := prepareApprovedDeployment(t, s, approvals, execution)
	req, _ := approvals.GetApproval(context.Background(), *entry.ApprovalID)
	time.Sleep(time.Until(*req.ExpiresAt) + time.Millisecond)
	if _, err := s.advance(context.Background(), entry.ProjectID, entry.ReleaseID, execution.ActorID, true, false); !errors.Is(err, ErrDeploymentApproval) {
		t.Fatalf("expired vote applied: %v", err)
	}
	db.Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Updates(map[string]any{"status": "applying", "workload_uid": "workload", "baseline_generation": 7})
	if _, err := s.RecoverAuthorized(context.Background(), entry.ProjectID, entry.ReleaseID, "operator"); !errors.Is(err, ErrDeploymentApproval) {
		t.Fatalf("expired vote recovered: %v", err)
	}
	if adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("expired vote accessed cluster")
	}
}

func TestDeploymentApprovalCannotBeOmittedOrBypassedThroughManualRelease(t *testing.T) {
	s, _, execution, _, _ := deploymentApprovalFixture(t)
	run, _ := s.pipelines.GetRun(context.Background(), execution.PipelineRunID)
	plan, _ := cluster.ParsePipelinePlan(run.DeploymentPlan, run.DeploymentPlanHash)
	plan.ApprovalRequired = false
	if err := s.ValidatePipelineApprovalPlan(context.Background(), execution.ProjectID, plan, execution.ActorID); err == nil {
		t.Fatal("operator gate omitted")
	}
	manual, _, input, _ := releaseFixture(t)
	binding := manual.targets["staging"]
	binding.PipelineApproval = &cluster.PipelineApprovalPolicy{Approvers: []string{"reviewer"}, MinApprovals: 1, TimeoutSeconds: 60}
	manual.targets["staging"] = binding
	if _, err := manual.CreateRelease(context.Background(), "project", "user", input); !errors.Is(err, ErrDeploymentApproval) {
		t.Fatalf("manual release bypassed mandatory gate: %v", err)
	}
}

func TestDeploymentApprovalRejectOnlyReviewerUsesRejectionPermission(t *testing.T) {
	s, _, execution, adapter, approvals := deploymentApprovalFixture(t)
	entry, err := s.PreparePipelineRelease(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	s.approvalAuthorize = func(context.Context, string, string, string, string) error {
		return errors.New("approval permission denied")
	}
	s.approvalRejectAuthorize = nil
	if err := approvals.Reject(context.Background(), *entry.ApprovalID, "reviewer", "reject"); err == nil {
		t.Fatal("missing rejection authorizer granted authority")
	}
	s.approvalRejectAuthorize = func(context.Context, string, string, string, string) error { return nil }
	if err := approvals.Approve(context.Background(), *entry.ApprovalID, "reviewer", "approve"); err == nil {
		t.Fatal("rejection authority granted approval")
	}
	if err := approvals.Reject(context.Background(), *entry.ApprovalID, "reviewer", "stop this release"); err != nil {
		t.Fatalf("reject-only reviewer denied: %v", err)
	}
	req, err := approvals.GetApproval(context.Background(), *entry.ApprovalID)
	if err != nil || req.Status != model.ApprovalStatusRejected || req.RejectedCount != 1 || req.ApprovedCount != 0 {
		t.Fatalf("rejection not persisted: %+v %v", req, err)
	}
	if adapter.inspect+adapter.apply+adapter.observe != 0 {
		t.Fatal("decision touched Kubernetes")
	}
}
