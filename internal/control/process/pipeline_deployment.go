package process

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/bytedance/sonic"
)

// ConfigurePipelineDeployments 注入控制面发布服务，缺省拒绝部署步骤
func (e *Process) ConfigurePipelineDeployments(svc *service.DeploymentService) {
	e.deploymentService = svc
}

func (e *Process) validateDeploymentRun(run *model.PipelineRun, parsed *spec.Pipeline) (bool, error) {
	plan, err := deployment.DecodePipelinePlan(parsed)
	if err != nil {
		return false, err
	}
	if plan == nil {
		if run.DeploymentPlan != "" || run.DeploymentPlanHash != "" {
			return false, fmt.Errorf("deployment plan no longer matches pipeline")
		}
		return false, nil
	}
	if e.deploymentService == nil || !run.AllowDeployments || run.TriggerType != 1 || run.DeploymentActorID == "" || run.TriggeredBy != run.DeploymentActorID || run.DefinitionCommitSha == "" {
		return false, fmt.Errorf("deployment requires opted-in verified manual execution")
	}
	canonical, hash, err := plan.Canonical()
	if err != nil || canonical != run.DeploymentPlan || hash != run.DeploymentPlanHash {
		return false, fmt.Errorf("immutable deployment plan mismatch")
	}
	return true, nil
}

func (rc *Coordinator) runDeployment(ctx context.Context, execution pipeline.DeploymentStepExecution) error {
	svc := rc.engine.deploymentService
	if svc == nil {
		return fmt.Errorf("pipeline deployments unavailable")
	}
	plan, err := deployment.ParsePipelinePlan(rc.run.DeploymentPlan, rc.run.DeploymentPlanHash)
	if err != nil {
		return err
	}
	if execution.PipelineID != rc.run.PipelineID || execution.PipelineRunID != rc.run.RunID || execution.JobName != plan.JobName || execution.StepName != plan.StepName || execution.JobRunID == "" || execution.StepRunID == "" {
		return fmt.Errorf("deployment execution identity mismatch")
	}
	p, err := rc.engine.repos.Pipeline.Get(ctx, rc.run.PipelineID)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds)*time.Second)
	defer cancel()
	request := service.PipelineDeploymentExecution{ProjectID: p.ProjectID, PipelineID: p.PipelineID, PipelineRunID: rc.run.RunID, JobRunID: execution.JobRunID, StepRunID: execution.StepRunID, ActorID: rc.run.DeploymentActorID}
	_, err = svc.ExecutePipelineDeployment(bounded, request, func(release *model.DeploymentRelease) error {
		if release == nil || release.ReleaseID == "" {
			return fmt.Errorf("prepared release ID is required")
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := rc.engine.repos.StepRun.PatchByStepRunID(persistCtx, execution.StepRunID, map[string]any{"deployment_release_id": release.ReleaseID}); err != nil {
			return fmt.Errorf("persist deployment step release link: %w", err)
		}
		if rc.engine.auditWriter != nil {
			event := PipelineAudit("pipeline_deployment.prepared", rc.run.DeploymentActorID, rc.run.RunID, rc.run.PipelineID)
			event.Reason = "verified pipeline deployment prepared"
			event.Payload, _ = sonic.Marshal(map[string]string{"pipelineId": rc.run.PipelineID, "runId": rc.run.RunID, "jobRunId": execution.JobRunID, "stepRunId": execution.StepRunID, "releaseId": release.ReleaseID})
			rc.engine.auditWriter.Write(persistCtx, event)
		}
		return nil
	})
	return err
}
