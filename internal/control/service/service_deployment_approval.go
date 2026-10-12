package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/bytedance/sonic"
	"gorm.io/datatypes"
)

// ErrDeploymentApproval 表示审批证据未完成、过期或已失效，不能提交镜像更新
var ErrDeploymentApproval = errors.New("deployment approval is pending, expired, rejected, or no longer authorized")

// ConfigureDeploymentApprovals 注入持久化决策和每次重新查询的审批权限检查
func (s *DeploymentService) ConfigureDeploymentApprovals(decisions repo.IApprovalDecisionRepository, authorize, rejectAuthorize PipelineDeploymentAuthorizer) {
	s.approvalDecisions, s.approvalAuthorize, s.approvalRejectAuthorize = decisions, authorize, rejectAuthorize
}

func (s *DeploymentService) deploymentApprovalPolicy(ctx context.Context, project string, plan *cluster.PipelineDeploymentPlan, actor string) (*cluster.PipelineApprovalPolicy, error) {
	env, err := s.records.GetEnvironment(ctx, project, plan.EnvironmentID)
	if err != nil {
		return nil, ErrDeploymentApproval
	}
	policy := cluster.ClonePipelineApprovalPolicy(s.targets[env.TargetRef].PipelineApproval)
	if (policy != nil) != plan.ApprovalRequired || cluster.ValidatePipelineApprovalPolicy(policy) != nil {
		return nil, ErrDeploymentApproval
	}
	if policy == nil {
		return nil, nil
	}
	if s.approvals == nil || s.approvalDecisions == nil || s.approvalAuthorize == nil {
		return nil, ErrDeploymentApproval
	}
	policy.Approvers = slices.DeleteFunc(policy.Approvers, func(candidate string) bool { return candidate == actor })
	if policy.MinApprovals > len(policy.Approvers) {
		return nil, ErrDeploymentApproval
	}
	return policy, nil
}

// ValidatePipelineApprovalPlan 校验操作员门禁不可被 DSL 省略或覆盖，不访问集群
func (s *DeploymentService) ValidatePipelineApprovalPlan(ctx context.Context, project string, plan *cluster.PipelineDeploymentPlan, actor string) error {
	_, err := s.deploymentApprovalPolicy(ctx, project, plan, actor)
	return err
}

type deploymentApprovalTarget struct {
	ProjectID     string `json:"projectId"`
	EnvironmentID string `json:"environmentId"`
	TargetRef     string `json:"targetRef"`
	Namespace     string `json:"namespace"`
	Deployment    string `json:"deployment"`
	Container     string `json:"container"`
}

func approvalEvidence(entry *model.DeploymentRelease, req *model.ApprovalRequest) string {
	if entry == nil || req == nil || req.ExpiresAt == nil || entry.ExecutionStepRunID == nil {
		return ""
	}
	candidates, err := model.ApprovalCandidates(req)
	if err != nil {
		return ""
	}
	var target deploymentApprovalTarget
	var targetFields map[string]any
	if sonic.Unmarshal(entry.ApprovalTarget, &target) != nil || sonic.Unmarshal(entry.ApprovalTarget, &targetFields) != nil || len(targetFields) != 6 {
		return ""
	}
	values := []any{"pipeline-deployment-approval-v1", entry.ReleaseID, entry.ProjectID, entry.RequestID, entry.EnvironmentID, entry.PipelineID, entry.PipelineRunID, entry.ExecutionJobRunID, *entry.ExecutionStepRunID, entry.ProducerJobRunID, entry.ProducerStepRunID, entry.DeploymentPlanHash, entry.TargetFingerprint, entry.ArtifactID, entry.ArtifactSHA256, entry.CommitSHA, entry.Image, entry.CreatedBy, target, req.ApprovalID, req.JobName, req.StepName, req.Plugin, candidates, req.RequiredApproverCount, req.Mode, req.RequestedBy, req.ExpiresAt.UTC().UnixMilli()}
	data, err := sonic.Marshal(values)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (s *DeploymentService) prepareDeploymentApproval(ctx context.Context, entry *model.DeploymentRelease, plan *cluster.PipelineDeploymentPlan) error {
	policy, err := s.deploymentApprovalPolicy(ctx, entry.ProjectID, plan, entry.CreatedBy)
	if err != nil || policy == nil {
		return err
	}
	env, err := s.records.GetEnvironment(ctx, entry.ProjectID, entry.EnvironmentID)
	if err != nil {
		return err
	}
	target, err := sonic.Marshal(deploymentApprovalTarget{entry.ProjectID, env.EnvironmentID, env.TargetRef, env.Namespace, env.Deployment, env.Container})
	if err != nil {
		return err
	}
	candidates, err := sonic.MarshalString(policy.Approvers)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte("deployment-approval-v1\x00"+entry.RequestID)))
	expires := time.Now().UTC().Add(time.Duration(policy.TimeoutSeconds) * time.Second).Truncate(time.Millisecond)
	req := &model.ApprovalRequest{ApprovalID: id, Version: 1, PipelineRunID: entry.PipelineRunID, JobName: plan.JobName, StepName: plan.StepName, Plugin: "builtin:deployment", DeploymentReleaseID: &entry.ReleaseID, Status: model.ApprovalStatusPending, Mode: "any", RequiredApproverCount: policy.MinApprovals, Environment: entry.EnvironmentID, RequestedBy: entry.CreatedBy, CandidateApprovers: candidates, ExpiresAt: &expires}
	entry.ApprovalID, entry.ApprovalTarget = &id, datatypes.JSON(target)
	req.EvidenceHash = approvalEvidence(entry, req)
	if req.EvidenceHash == "" {
		return ErrDeploymentApproval
	}
	entry.ApprovalEvidenceHash, entry.ApprovalRequest = req.EvidenceHash, req
	entry.Reason = "Waiting for durable manual deployment approval; no Kubernetes request issued"
	return nil
}

func (s *DeploymentService) boundDeploymentApproval(ctx context.Context, entry *model.DeploymentRelease, plan *cluster.PipelineDeploymentPlan) (*model.ApprovalRequest, error) {
	policy, err := s.deploymentApprovalPolicy(ctx, entry.ProjectID, plan, entry.CreatedBy)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		if entry.ApprovalID != nil || entry.ApprovalEvidenceHash != "" || len(entry.ApprovalTarget) != 0 {
			return nil, ErrDeploymentApproval
		}
		return nil, nil
	}
	if entry.ApprovalID == nil {
		return nil, ErrDeploymentApproval
	}
	req, err := s.approvals.GetByApprovalID(ctx, *entry.ApprovalID)
	if err != nil || req == nil || req.DeploymentReleaseID == nil || *req.DeploymentReleaseID != entry.ReleaseID || req.PipelineRunID != entry.PipelineRunID || req.RequestedBy != entry.CreatedBy || req.Environment != entry.EnvironmentID || req.JobName != plan.JobName || req.StepName != plan.StepName || req.Plugin != "builtin:deployment" || req.EvidenceHash == "" || req.EvidenceHash != entry.ApprovalEvidenceHash || approvalEvidence(entry, req) != req.EvidenceHash || req.Mode != "any" || req.RequiredApproverCount != policy.MinApprovals {
		return nil, ErrDeploymentApproval
	}
	candidates, err := model.ApprovalCandidates(req)
	if err != nil || !slices.Equal(candidates, policy.Approvers) {
		return nil, ErrDeploymentApproval
	}
	env, err := s.records.GetEnvironment(ctx, entry.ProjectID, entry.EnvironmentID)
	if err != nil || env.ActiveReleaseID != entry.ReleaseID || s.targets[env.TargetRef].Fingerprint != entry.TargetFingerprint {
		return nil, ErrDeploymentApproval
	}
	expectedTarget := deploymentApprovalTarget{entry.ProjectID, env.EnvironmentID, env.TargetRef, env.Namespace, env.Deployment, env.Container}
	var persistedTarget deploymentApprovalTarget
	if sonic.Unmarshal(entry.ApprovalTarget, &persistedTarget) != nil || expectedTarget != persistedTarget {
		return nil, ErrDeploymentApproval
	}
	return req, nil
}

// ValidateApprovalDecision 在保存决策前核实活动部署、可信候选人和新鲜权限
func (s *DeploymentService) ValidateApprovalDecision(ctx context.Context, req *model.ApprovalRequest, actor, decision string) error {
	if req == nil || req.DeploymentReleaseID == nil || !model.IsApprovalCandidate(req, actor) {
		return ErrDeploymentApproval
	}
	run, err := s.pipelines.GetRun(ctx, req.PipelineRunID)
	if err != nil || run == nil {
		return ErrDeploymentApproval
	}
	pipeline, err := s.pipelines.Get(ctx, run.PipelineID)
	if err != nil || pipeline == nil {
		return ErrDeploymentApproval
	}
	entry, err := s.records.GetRelease(ctx, pipeline.ProjectID, *req.DeploymentReleaseID)
	if err != nil || entry == nil || entry.Status != "queued" {
		return ErrDeploymentApproval
	}
	execution, err := releaseExecution(entry)
	if err != nil {
		return err
	}
	_, plan, err := s.pipelineRunPlan(ctx, execution, true)
	if err != nil {
		return err
	}
	if err := s.authorizePipelineRelease(ctx, entry, entry.CreatedBy, true); err != nil {
		return err
	}
	current, err := s.boundDeploymentApproval(ctx, entry, plan)
	if err != nil || current == nil || current.ApprovalID != req.ApprovalID || current.Status != model.ApprovalStatusPending || current.ExpiresAt == nil || !current.ExpiresAt.After(time.Now()) {
		return ErrDeploymentApproval
	}
	authorize := s.approvalAuthorize
	switch decision {
	case model.ApprovalDecisionApprove:
	case model.ApprovalDecisionReject:
		authorize = s.approvalRejectAuthorize
	default:
		return ErrDeploymentApproval
	}
	if authorize == nil {
		return ErrDeploymentApproval
	}
	if err := authorize(ctx, actor, entry.ProjectID, entry.PipelineID, entry.EnvironmentID); err != nil {
		return ErrDeploymentApproval
	}
	return validDeploymentApprovalDeadline(ctx, current)
}

func (s *DeploymentService) verifyDeploymentApproval(ctx context.Context, entry *model.DeploymentRelease) error {
	execution, err := releaseExecution(entry)
	if err != nil {
		return err
	}
	_, plan, err := s.pipelineRunPlan(ctx, execution, false)
	if err != nil {
		return err
	}
	req, err := s.boundDeploymentApproval(ctx, entry, plan)
	if err != nil || req == nil {
		return err
	}
	if req.Status != model.ApprovalStatusApproved || req.ExpiresAt == nil || !req.ExpiresAt.After(time.Now()) {
		return ErrDeploymentApproval
	}
	// 先完成可能耗时的制品读取，再尽可能靠近 PATCH 复核当前授权
	artifact, image, err := s.readReleaseArtifact(ctx, entry.ProjectID, entry.PipelineID, entry.PipelineRunID, entry.ArtifactID)
	if err != nil || artifact.JobRunID != entry.ProducerJobRunID || artifact.StepRunID != entry.ProducerStepRunID || artifact.Name != plan.Artifact.Name || artifact.SHA256 != entry.ArtifactSHA256 || image != entry.Image {
		return ErrDeploymentApproval
	}
	decisions, err := s.approvalDecisions.ListByApproval(ctx, req.ApprovalID)
	if err != nil {
		return ErrDeploymentApproval
	}
	seen := map[string]bool{}
	for _, decision := range decisions {
		if decision.ApprovalID != req.ApprovalID || decision.Decision != model.ApprovalDecisionApprove || seen[decision.UserID] || !model.IsApprovalCandidate(req, decision.UserID) {
			return ErrDeploymentApproval
		}
		seen[decision.UserID] = true
		if err := s.approvalAuthorize(ctx, decision.UserID, entry.ProjectID, entry.PipelineID, entry.EnvironmentID); err != nil {
			return ErrDeploymentApproval
		}
	}
	if len(seen) < req.RequiredApproverCount || len(seen) != req.ApprovedCount || req.RejectedCount != 0 {
		return ErrDeploymentApproval
	}
	return validDeploymentApprovalDeadline(ctx, req)
}

// 最后一个可能耗时的读取或鉴权完成之后，再检查上下文与原始截止时间
func validDeploymentApprovalDeadline(ctx context.Context, req *model.ApprovalRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req == nil || req.ExpiresAt == nil || !req.ExpiresAt.After(time.Now()) {
		return ErrDeploymentApproval
	}
	return nil
}

func (s *DeploymentService) finalDeploymentApprovalDeadline(ctx context.Context, entry *model.DeploymentRelease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.ApprovalID == nil {
		return nil
	}
	if s.approvals == nil {
		return ErrDeploymentApproval
	}
	req, err := s.approvals.GetByApprovalID(ctx, *entry.ApprovalID)
	if err != nil || req == nil || req.Status != model.ApprovalStatusApproved || req.DeploymentReleaseID == nil || *req.DeploymentReleaseID != entry.ReleaseID || req.PipelineRunID != entry.PipelineRunID || req.EvidenceHash != entry.ApprovalEvidenceHash || approvalEvidence(entry, req) != req.EvidenceHash {
		return ErrDeploymentApproval
	}
	return validDeploymentApprovalDeadline(ctx, req)
}

func (s *DeploymentService) waitDeploymentApproval(ctx context.Context, entry *model.DeploymentRelease, plan *cluster.PipelineDeploymentPlan) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := s.boundDeploymentApproval(ctx, entry, plan)
		if err != nil || req == nil {
			return err
		}
		if req.ExpiresAt == nil || !req.ExpiresAt.After(time.Now()) {
			if _, err := s.approvals.Expire(ctx, req.ApprovalID); err != nil {
				return err
			}
			return ErrDeploymentApproval
		}
		switch req.Status {
		case model.ApprovalStatusApproved:
			return s.verifyDeploymentApproval(ctx, entry)
		case model.ApprovalStatusPending:
		default:
			return ErrDeploymentApproval
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
