package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/arcentrix/arcentra/pkg/serde"
	"maps"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
)

type verifiedPipelineActorKey struct{}

// WithVerifiedPipelineActor 仅由认证 HTTP 入口传入真实人工触发者，不接受请求字段替代
func WithVerifiedPipelineActor(ctx context.Context, actorID string) context.Context {
	return context.WithValue(ctx, verifiedPipelineActorKey{}, strings.TrimSpace(actorID))
}
func verifiedPipelineActor(ctx context.Context) string {
	actor, _ := ctx.Value(verifiedPipelineActorKey{}).(string)
	return actor
}

// PipelineDeploymentReview 组合不可变 DSL 计划及已配置的精确发布目标
type PipelineDeploymentReview struct {
	*deployment.PipelineDeploymentPlan
	Target *DeploymentEnvironmentView `json:"target"`
}

func deploymentReviewHash(canonical, fingerprint string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical+"\n"+fingerprint)))
}

// GetPipelineDeploymentReview 为 HTTP 审阅返回不含端点或凭据的目标快照
func (s *PipelineServiceImpl) GetPipelineDeploymentReview(ctx context.Context, pipelineID string, spec *pipelinev1.Spec) (*PipelineDeploymentReview, string, error) {
	plan, err := deployment.DecodePipelinePlan(spec)
	if err != nil || plan == nil {
		return nil, "", err
	}
	review := &PipelineDeploymentReview{PipelineDeploymentPlan: plan}
	if s.deployment == nil {
		return review, "", fmt.Errorf("pipeline deployments unavailable")
	}
	pipeline, err := s.pipelineRepo.Get(ctx, pipelineID)
	if err != nil {
		return nil, "", err
	}
	target, err := s.deployment.Environment(ctx, pipeline.ProjectID, plan.EnvironmentID)
	if err != nil {
		return review, "", err
	}
	review.Target = target
	fingerprint, err := s.deployment.PipelineDeploymentTargetFingerprint(ctx, pipeline.ProjectID, plan.EnvironmentID)
	if err != nil {
		return review, "", err
	}
	canonical, _, err := plan.Canonical()
	if err != nil {
		return nil, "", err
	}
	return &PipelineDeploymentReview{PipelineDeploymentPlan: plan, Target: target}, deploymentReviewHash(canonical, fingerprint), nil
}

func (s *PipelineServiceImpl) authorizeDeploymentTrigger(ctx context.Context, req *pipelinev1.TriggerPipelineRequest, pipeline *model.Pipeline, headSHA string, parsed *pipelinev1.Spec, includes bool) (*model.PipelineRun, int, error) {
	plan, err := deployment.DecodePipelinePlan(parsed)
	if err != nil {
		return nil, 400, err
	}
	if plan == nil {
		return nil, 0, nil
	}
	if includes {
		return nil, 400, fmt.Errorf("deployment-bearing definitions cannot use includes; review a self-contained definition")
	}
	actor := verifiedPipelineActor(ctx)
	if actor == "" || req.GetTriggeredBy() != actor {
		return nil, 403, fmt.Errorf("deployment requires a verified manual actor")
	}
	if !req.GetAllowDeployments() {
		return nil, 400, fmt.Errorf("explicit allowDeployments opt-in is required")
	}
	if !commitPattern.MatchString(headSHA) || req.GetExpectedDefinitionCommitSha() != headSHA {
		return nil, 409, fmt.Errorf("definition changed or reviewed revision missing; review the current definition before deployment")
	}
	if s.deployment == nil {
		return nil, 503, fmt.Errorf("pipeline deployments unavailable")
	}
	if err := s.deployment.AuthorizePipelineDeployment(ctx, actor, pipeline.ProjectID, pipeline.PipelineID, plan.EnvironmentID); err != nil {
		return nil, 403, fmt.Errorf("deployment permission denied: %w", err)
	}
	fingerprint, err := s.deployment.PipelineDeploymentTargetFingerprint(ctx, pipeline.ProjectID, plan.EnvironmentID)
	if err != nil {
		return nil, 409, err
	}
	canonical, hash, err := plan.Canonical()
	if err != nil {
		return nil, 400, err
	}
	if req.GetExpectedDeploymentPlanHash() != deploymentReviewHash(canonical, fingerprint) {
		return nil, 409, fmt.Errorf("deployment plan or target changed; review the current definition and target")
	}
	return &model.PipelineRun{AllowDeployments: true, DeploymentActorID: actor, DeploymentPlan: canonical, DeploymentPlanHash: hash, DeploymentTargetFingerprint: fingerprint}, 0, nil
}

func validateDeploymentDuplicate(ctx context.Context, request *pipelinev1.TriggerPipelineRequest, existing *model.PipelineRun) error {
	if existing.DeploymentPlan == "" && !existing.AllowDeployments {
		return nil
	}
	actor := verifiedPipelineActor(ctx)
	if (!maps.Equal(request.GetVariables(), serde.UnmarshalStringMap(existing.Env))) || actor == "" || actor != existing.DeploymentActorID || request.GetTriggeredBy() != actor || !request.GetAllowDeployments() || request.GetExpectedDefinitionCommitSha() != existing.DefinitionCommitSha || request.GetExpectedDeploymentPlanHash() != deploymentReviewHash(existing.DeploymentPlan, existing.DeploymentTargetFingerprint) {
		return fmt.Errorf("requestId belongs to a different deployment actor, consent, definition, target, or variables")
	}
	return nil
}
