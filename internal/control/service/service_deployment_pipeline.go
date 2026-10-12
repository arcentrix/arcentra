// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 流水线发布错误不包含集群响应、凭据或制品内容
var (
	ErrPipelineDeploymentSource    = errors.New("pipeline deployment requires the reviewed active manual run, verified actor, exact successful predecessor attempts, and registered image artifact")
	ErrPipelineDeploymentDenied    = errors.New("pipeline deployment is disabled or current actor authorization was denied")
	ErrPipelineDeploymentUncertain = errors.New("deployment apply is unresolved; keep this release and reservation and explicitly recover the same release ID")
	ErrPipelineDeploymentStopped   = errors.New("pipeline stopped waiting for deployment; an applying or observing release remains reserved and requires inspection")
	ErrPipelineDeploymentFailed    = errors.New("deployment did not reach a successful observed rollout")
)

// PipelineDeploymentAuthorizer 每次调用重新加载当前用户、团队和目标权限
// 参数依次为 actor、project、pipeline、environment，不能信任触发时缓存的成员关系
type PipelineDeploymentAuthorizer func(context.Context, string, string, string, string) error

// PipelineDeploymentExecution 仅由控制面执行器传入持久化执行身份，不提供镜像或目标覆盖
type PipelineDeploymentExecution struct {
	ProjectID     string
	PipelineID    string
	PipelineRunID string
	JobRunID      string
	StepRunID     string
	ActorID       string
}

// ConfigurePipelineDeployments 注入服务器内部身份校验与执行记录依赖，未配置时关闭流水线发布
func (s *DeploymentService) ConfigurePipelineDeployments(jobs repo.IJobRunRepository, steps repo.IStepRunRepository, authorize PipelineDeploymentAuthorizer) {
	s.pipelineJobs, s.pipelineSteps, s.pipelineAuthorize = jobs, steps, authorize
}

func pipelineDeploymentRequest(execution PipelineDeploymentExecution) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{"pipeline-deployment-v1", execution.ProjectID, execution.PipelineID, execution.PipelineRunID, execution.JobRunID, execution.StepRunID}, "\x00")))
	return hex.EncodeToString(hash[:])
}

func (s *DeploymentService) pipelineRunPlan(ctx context.Context, execution PipelineDeploymentExecution, active bool) (*model.PipelineRun, *cluster.PipelineDeploymentPlan, error) {
	for _, value := range []string{execution.ProjectID, execution.PipelineID, execution.PipelineRunID, execution.JobRunID, execution.StepRunID, execution.ActorID} {
		if artifactutil.Identity(value) != nil {
			return nil, nil, ErrPipelineDeploymentSource
		}
	}
	if s.pipelineJobs == nil || s.pipelineSteps == nil || s.pipelineAuthorize == nil {
		return nil, nil, ErrPipelineDeploymentDenied
	}
	pipeline, err := s.pipelines.Get(ctx, execution.PipelineID)
	if err != nil || pipeline == nil || pipeline.ProjectID != execution.ProjectID {
		return nil, nil, ErrPipelineDeploymentSource
	}
	run, err := s.pipelines.GetRun(ctx, execution.PipelineRunID)
	if err != nil || run == nil || run.RunID != execution.PipelineRunID || run.PipelineID != execution.PipelineID || !run.AllowDeployments || run.TriggerType != 1 || run.DeploymentActorID != execution.ActorID || run.TriggeredBy != execution.ActorID || !commitPattern.MatchString(run.DefinitionCommitSha) || active && run.Status != model.PipelineStatusRunning {
		return nil, nil, ErrPipelineDeploymentSource
	}
	plan, err := cluster.ParsePipelinePlan(run.DeploymentPlan, run.DeploymentPlanHash)
	if err != nil || plan == nil {
		return nil, nil, ErrPipelineDeploymentSource
	}
	return run, plan, nil
}

func (s *DeploymentService) pipelinePermission(ctx context.Context, actor, project, pipeline, environment string, requireOptIn bool) error {
	env, err := s.records.GetEnvironment(ctx, project, environment)
	if err != nil {
		return ErrDeploymentNotFound
	}
	if _, err := s.adapter(env); err != nil {
		return err
	}
	if requireOptIn && !s.targets[env.TargetRef].AllowPipelineDeployments || s.pipelineAuthorize == nil || artifactutil.Identity(actor) != nil {
		return ErrPipelineDeploymentDenied
	}
	if err := s.pipelineAuthorize(ctx, actor, project, pipeline, environment); err != nil {
		return ErrPipelineDeploymentDenied
	}
	return nil
}

func (s *DeploymentService) pipelineExecution(ctx context.Context, execution PipelineDeploymentExecution, plan *cluster.PipelineDeploymentPlan, active bool) error {
	job, err := s.pipelineJobs.GetByJobRunID(ctx, execution.JobRunID)
	if err != nil || job == nil || job.PipelineID != execution.PipelineID || job.PipelineRunID != execution.PipelineRunID || job.JobName != plan.JobName || job.AgentID != "" || job.TotalSteps != 1 || active && job.Status != model.JobRunStatusRunning {
		return ErrPipelineDeploymentSource
	}
	step, err := s.pipelineSteps.GetByStepRunID(ctx, execution.StepRunID)
	if err != nil || step == nil || step.JobRunID != job.JobRunID || step.PipelineID != execution.PipelineID || step.PipelineRunID != execution.PipelineRunID || step.Name != plan.StepName || step.StepIndex != 0 || step.Attempt != 1 || step.AgentID != "" || step.CreatedBy != execution.ActorID || step.Uses != "builtin:kubernetes" || step.Action != "deploy" || step.RetryMode != "" || step.RetryCount != 0 || step.CurrentRetry != 0 || step.ContinueOnError != 0 || step.AllowFailure != 0 || active && step.Status != 3 {
		return ErrPipelineDeploymentSource
	}
	var args struct {
		EnvironmentID string                            `json:"environmentId"`
		Artifact      cluster.PipelineArtifactReference `json:"artifact"`
	}
	if sonic.UnmarshalString(step.Args, &args) != nil || args.EnvironmentID != plan.EnvironmentID || args.Artifact != plan.Artifact {
		return ErrPipelineDeploymentSource
	}
	return nil
}

func (s *DeploymentService) pipelineStepsForRun(ctx context.Context, runID string) ([]model.StepRun, error) {
	values := []model.StepRun{}
	for page := 1; ; page++ {
		batch, total, err := s.pipelineSteps.List(ctx, repo.StepRunFilter{PipelineRunID: runID, Page: page, PageSize: 200})
		if err != nil {
			return nil, err
		}
		values = append(values, batch...)
		if len(values) > 10000 {
			return nil, ErrPipelineDeploymentSource
		}
		if int64(len(values)) >= total {
			return values, nil
		}
		if len(batch) == 0 {
			return nil, ErrPipelineDeploymentSource
		}
	}
}

// successfulPipelineAttempts 检查真正执行的最新逻辑步骤，未使用预留重试不能遮盖旧失败
func successfulPipelineAttempts(job *model.JobRun, expected []cluster.PipelinePlanStep, records []model.StepRun) (map[string]model.StepRun, error) {
	names := make(map[string]int, len(expected))
	for _, step := range expected {
		if _, ok := names[step.Name]; ok {
			return nil, ErrPipelineDeploymentSource
		}
		names[step.Name] = step.Index
	}
	if len(names) == 0 || job.TotalSteps != len(names) {
		return nil, ErrPipelineDeploymentSource
	}
	attempts := map[string]map[int]model.StepRun{}
	wholeRound := 0
	for _, step := range records {
		if step.JobRunID != job.JobRunID {
			continue
		}
		index, ok := names[step.Name]
		if !ok || step.StepIndex != index || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.AgentID != job.AgentID || step.Attempt < 1 || step.Attempt > 5 || step.Status < 4 || step.Status > 8 || (step.Status == 5 && step.ContinueOnError != 0) {
			return nil, ErrPipelineDeploymentSource
		}
		if step.RetryMode != "" && step.RetryMode != "failed_step" && step.RetryMode != "whole_job" {
			return nil, ErrPipelineDeploymentSource
		}
		if job.AgentID != "" && step.TerminalReportDigest == "" {
			return nil, ErrPipelineDeploymentSource
		}
		if attempts[step.Name] == nil {
			attempts[step.Name] = map[int]model.StepRun{}
		}
		if _, duplicate := attempts[step.Name][step.Attempt]; duplicate {
			return nil, ErrPipelineDeploymentSource
		}
		attempts[step.Name][step.Attempt] = step
		if step.RetryMode == "whole_job" && (step.Status != 8 || step.StartTime != nil) && step.Attempt > wholeRound {
			wholeRound = step.Attempt
		}
	}
	selected := map[string]model.StepRun{}
	for name := range names {
		candidates := attempts[name]
		chosen := model.StepRun{}
		for attempt, step := range candidates {
			if attempt > 1 {
				previous, ok := candidates[attempt-1]
				if !ok || previous.StepRunID != step.PreviousStepRunID || previous.RetryMode != step.RetryMode {
					return nil, ErrPipelineDeploymentSource
				}
			}
			if wholeRound > 0 {
				if step.RetryMode != "whole_job" {
					return nil, ErrPipelineDeploymentSource
				}
				if attempt == wholeRound {
					chosen = step
				}
			} else if (step.Status != 8 || step.StartTime != nil) && attempt > chosen.Attempt {
				chosen = step
			}
		}
		if chosen.StepRunID == "" || chosen.Status != 4 || chosen.ExitCode != nil && *chosen.ExitCode != 0 {
			return nil, ErrPipelineDeploymentSource
		}
		selected[name] = chosen
	}
	return selected, nil
}

func (s *DeploymentService) pipelineProducer(ctx context.Context, execution PipelineDeploymentExecution, plan *cluster.PipelineDeploymentPlan) (*model.JobRun, model.StepRun, error) {
	jobs, err := s.pipelineJobs.ListByPipelineRunID(ctx, execution.PipelineRunID)
	if err != nil {
		return nil, model.StepRun{}, ErrPipelineDeploymentSource
	}
	steps, err := s.pipelineStepsForRun(ctx, execution.PipelineRunID)
	if err != nil {
		return nil, model.StepRun{}, ErrPipelineDeploymentSource
	}
	expected := map[string]cluster.PipelinePredecessor{}
	for _, predecessor := range plan.Predecessors {
		if _, duplicate := expected[predecessor.JobName]; duplicate || predecessor.JobName == plan.JobName {
			return nil, model.StepRun{}, ErrPipelineDeploymentSource
		}
		expected[predecessor.JobName] = predecessor
	}
	if len(expected) == 0 || len(jobs) != len(expected)+1 {
		return nil, model.StepRun{}, ErrPipelineDeploymentSource
	}
	var producer *model.JobRun
	var selected model.StepRun
	seen := map[string]bool{}
	for _, job := range jobs {
		if job == nil || job.PipelineID != execution.PipelineID || job.PipelineRunID != execution.PipelineRunID || seen[job.JobName] {
			return nil, model.StepRun{}, ErrPipelineDeploymentSource
		}
		seen[job.JobName] = true
		if job.JobRunID == execution.JobRunID && job.JobName == plan.JobName {
			continue
		}
		predecessor, ok := expected[job.JobName]
		if !ok || job.Status != model.JobRunStatusSuccess || job.FailedSteps != 0 || job.AgentID != "" && job.TerminalReportDigest == "" {
			return nil, model.StepRun{}, ErrPipelineDeploymentSource
		}
		latest, err := successfulPipelineAttempts(job, predecessor.Steps, steps)
		if err != nil {
			return nil, model.StepRun{}, err
		}
		if job.JobName == plan.Artifact.Job {
			producer, selected = job, latest[plan.Artifact.Step]
		}
	}
	if producer == nil || selected.StepRunID == "" || selected.Uses != "builtin:artifacts" || !strings.EqualFold(selected.Action, "upload") || selected.ContinueOnError != 0 {
		return nil, model.StepRun{}, ErrPipelineDeploymentSource
	}
	return producer, selected, nil
}

func releaseExecution(entry *model.DeploymentRelease) (PipelineDeploymentExecution, error) {
	if entry.ExecutionStepRunID == nil || *entry.ExecutionStepRunID == "" || entry.Origin != "pipeline_job" {
		return PipelineDeploymentExecution{}, ErrPipelineDeploymentSource
	}
	return PipelineDeploymentExecution{ProjectID: entry.ProjectID, PipelineID: entry.PipelineID, PipelineRunID: entry.PipelineRunID, JobRunID: entry.ExecutionJobRunID, StepRunID: *entry.ExecutionStepRunID, ActorID: entry.CreatedBy}, nil
}

func (s *DeploymentService) authorizePipelineRelease(ctx context.Context, entry *model.DeploymentRelease, actor string, automatic bool) error {
	execution, err := releaseExecution(entry)
	if err != nil {
		return err
	}
	run, plan, err := s.pipelineRunPlan(ctx, execution, automatic || entry.Status == "queued")
	if err != nil {
		return err
	}
	if automatic && actor != execution.ActorID {
		return ErrPipelineDeploymentDenied
	}
	if run.DeploymentTargetFingerprint == "" || entry.TargetFingerprint != run.DeploymentTargetFingerprint || entry.RequestID != pipelineDeploymentRequest(execution) || entry.DeploymentPlanHash != run.DeploymentPlanHash || entry.CommitSHA != run.DefinitionCommitSha || entry.EnvironmentID != plan.EnvironmentID {
		return ErrPipelineDeploymentSource
	}
	if err := s.pipelineExecution(ctx, execution, plan, automatic || entry.Status == "queued"); err != nil {
		return err
	}
	step, err := s.pipelineSteps.GetByStepRunID(ctx, execution.StepRunID)
	if err != nil || step == nil || step.DeploymentReleaseID != entry.ReleaseID {
		return ErrPipelineDeploymentSource
	}
	return s.pipelinePermission(ctx, actor, execution.ProjectID, execution.PipelineID, plan.EnvironmentID, automatic || entry.Status == "queued")
}

// PreparePipelineRelease 仅供控制面执行器使用，不暴露活动运行旁路端点或请求参数
func (s *DeploymentService) PreparePipelineRelease(ctx context.Context, execution PipelineDeploymentExecution) (*model.DeploymentRelease, error) {
	run, plan, err := s.pipelineRunPlan(ctx, execution, true)
	if err != nil {
		return nil, err
	}
	if err := s.pipelineExecution(ctx, execution, plan, true); err != nil {
		return nil, err
	}
	if err := s.pipelinePermission(ctx, execution.ActorID, execution.ProjectID, execution.PipelineID, plan.EnvironmentID, true); err != nil {
		return nil, err
	}
	fingerprint, err := s.PipelineDeploymentTargetFingerprint(ctx, execution.ProjectID, plan.EnvironmentID)
	if err != nil || run.DeploymentTargetFingerprint == "" || run.DeploymentTargetFingerprint != fingerprint {
		return nil, ErrDeploymentTargetChanged
	}
	request := pipelineDeploymentRequest(execution)
	old, err := s.records.GetReleaseByRequest(ctx, execution.ProjectID, request)
	if err == nil {
		if err := s.authorizePipelineRelease(ctx, old, execution.ActorID, true); err != nil {
			return nil, err
		}
		return old, nil // 已持久化身份不能重新选择制品，终态发布不能创建替代发布
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	producer, step, err := s.pipelineProducer(ctx, execution, plan)
	if err != nil {
		return nil, err
	}
	artifactID := artifactutil.ID(execution.ProjectID, execution.PipelineRunID, producer.JobRunID, step.StepRunID, plan.Artifact.Name)
	artifact, image, err := s.readReleaseArtifact(ctx, execution.ProjectID, execution.PipelineID, execution.PipelineRunID, artifactID)
	if err != nil {
		return nil, ErrPipelineDeploymentSource
	}
	if artifact.JobRunID != producer.JobRunID || artifact.StepRunID != step.StepRunID || artifact.Name != plan.Artifact.Name {
		return nil, ErrPipelineDeploymentSource
	}
	stepID := execution.StepRunID
	entry := &model.DeploymentRelease{Origin: "pipeline_job", ReleaseID: uuid.NewString(), Version: 1, ProjectID: execution.ProjectID, RequestID: request, EnvironmentID: plan.EnvironmentID, PipelineID: execution.PipelineID, PipelineRunID: execution.PipelineRunID, ExecutionJobRunID: execution.JobRunID, ExecutionStepRunID: &stepID, ProducerJobRunID: producer.JobRunID, ProducerStepRunID: step.StepRunID, DeploymentPlanHash: run.DeploymentPlanHash, ArtifactID: artifactID, ArtifactSHA256: artifact.SHA256, CommitSHA: run.DefinitionCommitSha, Image: image, CreatedBy: execution.ActorID, TargetFingerprint: run.DeploymentTargetFingerprint, Status: "queued", Reason: "Authorized pipeline deployment prepared; awaiting durable step linkage"}
	return s.records.CreateRelease(ctx, entry)
}

func (s *DeploymentService) verifyQueuedRelease(ctx context.Context, entry *model.DeploymentRelease, actor string) error {
	if entry.Origin != "pipeline_job" {
		run, err := s.verifyRun(ctx, entry.ProjectID, entry.PipelineID, entry.PipelineRunID)
		if err != nil || deploymentDefinitionRevision(run) != entry.CommitSHA {
			return ErrDeploymentSource
		}
		return nil
	}
	execution, err := releaseExecution(entry)
	if err != nil {
		return err
	}
	run, plan, err := s.pipelineRunPlan(ctx, execution, true)
	if err != nil || run.DeploymentPlanHash != entry.DeploymentPlanHash {
		return ErrPipelineDeploymentSource
	}
	if err := s.pipelineExecution(ctx, execution, plan, true); err != nil {
		return err
	}
	stepRecord, err := s.pipelineSteps.GetByStepRunID(ctx, execution.StepRunID)
	if err != nil || stepRecord == nil || stepRecord.DeploymentReleaseID != entry.ReleaseID {
		return ErrPipelineDeploymentSource
	}
	producer, step, err := s.pipelineProducer(ctx, execution, plan)
	if err != nil || producer.JobRunID != entry.ProducerJobRunID || step.StepRunID != entry.ProducerStepRunID || artifactutil.ID(entry.ProjectID, entry.PipelineRunID, producer.JobRunID, step.StepRunID, plan.Artifact.Name) != entry.ArtifactID {
		return ErrPipelineDeploymentSource
	}
	artifact, image, err := s.readReleaseArtifact(ctx, entry.ProjectID, entry.PipelineID, entry.PipelineRunID, entry.ArtifactID)
	if err != nil || image != entry.Image || artifact.SHA256 != entry.ArtifactSHA256 {
		return ErrPipelineDeploymentSource
	}
	return s.pipelinePermission(ctx, actor, entry.ProjectID, entry.PipelineID, entry.EnvironmentID, true)
}

// ExecutePipelineDeployment 在有界等待中执行一次提交，未知提交立即停止并保留同一发布
// onPrepared 必须先持久化步骤发布链接；写入失败时绝不访问集群
func (s *DeploymentService) ExecutePipelineDeployment(ctx context.Context, execution PipelineDeploymentExecution, onPrepared func(*model.DeploymentRelease) error) (*model.DeploymentRelease, error) {
	if onPrepared == nil {
		return nil, ErrPipelineDeploymentSource
	}
	_, plan, err := s.pipelineRunPlan(ctx, execution, true)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds)*time.Second)
	defer stop()
	entry, err := s.PreparePipelineRelease(ctx, execution)
	if err != nil {
		return nil, err
	}
	if err := onPrepared(entry); err != nil {
		return entry, err
	}
	if entry.Status == "applying" {
		return entry, ErrPipelineDeploymentUncertain
	}
	for {
		if ctx.Err() != nil {
			return s.stopPipelineDeployment(ctx, entry)
		}
		switch entry.Status {
		case "succeeded":
			return entry, nil
		case "failed", "superseded", "cancelled":
			return entry, ErrPipelineDeploymentFailed
		case "applying":
			return entry, ErrPipelineDeploymentUncertain
		}
		next, err := s.advance(ctx, execution.ProjectID, entry.ReleaseID, execution.ActorID, true, false)
		if err != nil {
			readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			current, readErr := s.GetRelease(readCtx, execution.ProjectID, entry.ReleaseID)
			cancel()
			if readErr != nil {
				return entry, err
			}
			entry = current
			if ctx.Err() != nil {
				return s.stopPipelineDeployment(ctx, entry)
			}
			if entry.Status == "applying" {
				return entry, ErrPipelineDeploymentUncertain
			}
			if entry.Status != "observing" || !errors.Is(err, cluster.ErrUnavailable) {
				return entry, err
			}
		} else {
			entry = next
		}
		if model.IsDeploymentTerminal(entry.Status) {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return s.stopPipelineDeployment(ctx, entry)
		case <-timer.C:
		}
	}
}

func (s *DeploymentService) stopPipelineDeployment(ctx context.Context, entry *model.DeploymentRelease) (*model.DeploymentRelease, error) {
	if entry.Status == "queued" {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cancelled, err := s.Cancel(cleanup, entry.ProjectID, entry.ReleaseID)
		if err == nil {
			return cancelled, ctx.Err()
		}
		current, readErr := s.GetRelease(cleanup, entry.ProjectID, entry.ReleaseID)
		if readErr == nil {
			entry = current
		}
	}
	return entry, ErrPipelineDeploymentStopped
}

// AuthorizePipelineDeployment 为触发前的当前身份与操作员目标开关执行只读校验
func (s *DeploymentService) AuthorizePipelineDeployment(ctx context.Context, actor, project, pipeline, environment string) error {
	return s.pipelinePermission(ctx, actor, project, pipeline, environment, true)
}

// PipelineDeploymentTargetFingerprint 返回供经审阅运行固定的配置身份，不读取集群
func (s *DeploymentService) PipelineDeploymentTargetFingerprint(ctx context.Context, project, environment string) (string, error) {
	env, err := s.records.GetEnvironment(ctx, project, environment)
	if err != nil {
		return "", ErrDeploymentNotFound
	}
	if _, err := s.adapter(env); err != nil {
		return "", err
	}
	binding := s.targets[env.TargetRef]
	if !binding.AllowPipelineDeployments {
		return "", ErrPipelineDeploymentDenied
	}
	return binding.Fingerprint, nil
}

// DeploymentReleaseInspection 返回只读观察摘要，不改变发布状态或环境预留
type DeploymentReleaseInspection struct {
	ReleaseID          string `json:"releaseId"`
	PersistedStatus    string `json:"persistedStatus"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	ObservedGeneration int64  `json:"observedGeneration"`
	DesiredReplicas    int32  `json:"desiredReplicas"`
	UpdatedReplicas    int32  `json:"updatedReplicas"`
	AvailableReplicas  int32  `json:"availableReplicas"`
}

// RecoverAuthorized 仅用于独立确认的同一发布恢复，可能提交原先未被接纳的镜像更新
// 普通推进、流水线重入、取消和重启不会调用此入口
func (s *DeploymentService) RecoverAuthorized(ctx context.Context, project, id, actor string) (*model.DeploymentRelease, error) {
	entry, err := s.GetRelease(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if entry.Status == "queued" {
		return nil, ErrDeploymentInput
	}
	if err := s.pipelinePermission(ctx, actor, entry.ProjectID, entry.PipelineID, entry.EnvironmentID, false); err != nil {
		return nil, err
	}
	if entry.Origin == "pipeline_job" {
		if err := s.authorizePipelineRelease(ctx, entry, actor, false); err != nil {
			return nil, err
		}
	}
	if model.IsDeploymentTerminal(entry.Status) {
		return entry, nil
	}
	changed, err := s.records.Transition(ctx, entry, entry.Status, map[string]any{"last_recovery_by": actor, "last_recovery_at": time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, repo.ErrDeploymentConflict
	}
	return s.advance(ctx, project, id, actor, false, true)
}

// InspectAuthorized 只读检查已提交或不确定的同一工作负载，不发送 PATCH 或释放环境
func (s *DeploymentService) InspectAuthorized(ctx context.Context, project, id, actor string) (*DeploymentReleaseInspection, error) {
	entry, err := s.GetRelease(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if entry.Status != "applying" && entry.Status != "observing" {
		return nil, ErrDeploymentInput
	}
	if err := s.pipelinePermission(ctx, actor, entry.ProjectID, entry.PipelineID, entry.EnvironmentID, false); err != nil {
		return nil, err
	}
	if entry.Origin == "pipeline_job" {
		if err := s.authorizePipelineRelease(ctx, entry, actor, false); err != nil {
			return nil, err
		}
	}
	env, err := s.records.GetEnvironment(ctx, project, entry.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if env.ActiveReleaseID != entry.ReleaseID {
		return nil, repo.ErrDeploymentConflict
	}
	adapter, err := s.adapter(env)
	if err != nil {
		return nil, err
	}
	if entry.TargetFingerprint == "" || entry.TargetFingerprint != s.targets[env.TargetRef].Fingerprint {
		return nil, ErrDeploymentTargetChanged
	}
	generation := entry.AppliedGeneration
	if entry.Status == "applying" {
		generation = entry.BaselineGeneration + 1
	}
	if entry.WorkloadUID == "" || generation <= 1 {
		return nil, ErrPipelineDeploymentSource
	}
	observation, err := adapter.Observe(ctx, environmentTarget(env), entry.ReleaseID, entry.Image, cluster.Receipt{UID: entry.WorkloadUID, Generation: generation})
	if err != nil {
		return nil, err
	}
	if entry.Status == "applying" && observation.Status == "superseded" {
		// 缺失确认可能只是原请求未被接纳，不能据此宣称外部更新已经取代本次发布
		observation.Status = "unresolved"
		observation.Reason = "Expected release is not confirmed; inspect the workload identity, image and release marker before explicit same-ID recovery"
	}
	return &DeploymentReleaseInspection{ReleaseID: entry.ReleaseID, PersistedStatus: entry.Status, Status: observation.Status, Reason: observation.Reason, ObservedGeneration: observation.ObservedGeneration, DesiredReplicas: observation.DesiredReplicas, UpdatedReplicas: observation.UpdatedReplicas, AvailableReplicas: observation.AvailableReplicas}, nil
}
