// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
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

// 发布输入错误均使用固定文本，禁止将凭据或制品内容写入响应
var (
	ErrDeploymentInput         = errors.New("invalid deployment input")
	ErrDeploymentSource        = errors.New("release requires a successful run, approved persisted approval requests, immutable commit, and its digest-pinned image manifest artifact")
	ErrDeploymentNotFound      = errors.New("deployment record not found")
	ErrDeploymentTargetChanged = errors.New("operator target binding changed; existing release cannot execute against a different target")
	ErrDeploymentCancel        = errors.New("only queued, unapplied releases can be cancelled")
)
var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// DeploymentTargetBinding 由管理员在启动时注入精确的项目和工作负载白名单
type DeploymentTargetBinding struct {
	AllowPipelineDeployments bool
	Fingerprint              string
	Tier                     string
	ProjectID                string
	Target                   cluster.Target
	Adapter                  cluster.Adapter
}

// DeploymentTargets 是静态目标注册表，不接受浏览器传入 URL、凭据或宽泛命名空间
// 构造服务时复制绑定，运行时不得修改适配器的授权目标
type DeploymentTargets map[string]DeploymentTargetBinding

// DeploymentService 将已成功的构建来源固定为可恢复的发布记录
type DeploymentService struct {
	records           repo.IDeploymentRepository
	pipelines         repo.IPipelineRepository
	artifacts         repo.IArtifactRepository
	files             *ArtifactService
	approvals         repo.IApprovalRepository
	targets           DeploymentTargets
	pipelineJobs      repo.IJobRunRepository
	pipelineSteps     repo.IStepRunRepository
	pipelineAuthorize PipelineDeploymentAuthorizer
}

// NewDeploymentService 创建默认无集群连接的发布服务
func NewDeploymentService(records repo.IDeploymentRepository, pipelines repo.IPipelineRepository, artifacts repo.IArtifactRepository, files *ArtifactService, approvals repo.IApprovalRepository, targets DeploymentTargets) *DeploymentService {
	bindings := DeploymentTargets{}
	for ref, binding := range targets {
		if binding.Tier == "" {
			binding.Tier = "prod"
		}
		if binding.Fingerprint == "" {
			binding.Fingerprint = deploymentTargetFingerprint(ref, binding, "")
		}
		bindings[ref] = binding
	}
	return &DeploymentService{records: records, pipelines: pipelines, artifacts: artifacts, files: files, approvals: approvals, targets: bindings}
}

// DeploymentEnvironmentInput 描述不可变环境绑定，集群配置只能由管理员提供
type DeploymentEnvironmentInput struct {
	Name       string `json:"name"`
	TargetRef  string `json:"targetRef"`
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
}

// DeploymentEnvironmentView 附带目标配置状态，配置存在不代表集群健康
type DeploymentEnvironmentView struct {
	model.DeploymentEnvironment
	Configured               bool   `json:"configured"`
	AllowPipelineDeployments bool   `json:"allowPipelineDeployments"`
	Tier                     string `json:"tier"`
}

// DeploymentReleaseInput 仅选择既有流水线来源，不允许直接输入镜像或修改版本
type DeploymentReleaseInput struct {
	RequestID     string `json:"requestId"`
	EnvironmentID string `json:"environmentId"`
	PipelineID    string `json:"pipelineId"`
	PipelineRunID string `json:"pipelineRunId"`
	ArtifactID    string `json:"artifactId"`
}

// DeploymentReleasePage 返回权限过滤之后的发布列表
type DeploymentReleasePage struct {
	List     []model.DeploymentRelease `json:"list"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
}

// DeploymentEnvironmentPage 返回项目环境列表
type DeploymentEnvironmentPage struct {
	List     []DeploymentEnvironmentView `json:"list"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

func environmentTarget(e *model.DeploymentEnvironment) cluster.Target {
	return cluster.Target{Namespace: e.Namespace, Deployment: e.Deployment, Container: e.Container}
}
func (s *DeploymentService) adapter(e *model.DeploymentEnvironment) (cluster.Adapter, error) {
	binding, ok := s.targets[e.TargetRef]
	if !ok || binding.ProjectID != e.ProjectID || binding.Target != environmentTarget(e) || binding.Adapter == nil {
		return nil, cluster.ErrUnconfigured
	}
	return binding.Adapter, nil
}

// CreateEnvironment 保存精确目标声明，未配置目标仍明确显示为不可执行
func (s *DeploymentService) CreateEnvironment(ctx context.Context, project string, input DeploymentEnvironmentInput) (*DeploymentEnvironmentView, error) {
	if artifactutil.Identity(project) != nil || strings.TrimSpace(input.Name) == "" || len(input.Name) > 128 || artifactutil.Identity(input.TargetRef) != nil || !cluster.ValidTarget(cluster.Target{Namespace: input.Namespace, Deployment: input.Deployment, Container: input.Container}) {
		return nil, ErrDeploymentInput
	}
	entry := &model.DeploymentEnvironment{EnvironmentID: uuid.NewString(), ProjectID: project, Name: strings.TrimSpace(input.Name), TargetRef: input.TargetRef, Namespace: input.Namespace, Deployment: input.Deployment, Container: input.Container}
	if err := s.records.CreateEnvironment(ctx, entry); err != nil {
		return nil, err
	}
	_, err := s.adapter(entry)
	return &DeploymentEnvironmentView{DeploymentEnvironment: *entry, Configured: err == nil, AllowPipelineDeployments: err == nil && s.targets[entry.TargetRef].AllowPipelineDeployments, Tier: s.environmentTier(entry, err)}, nil
}

// Environments 读取环境，不探测集群或推断健康
func (s *DeploymentService) Environments(ctx context.Context, project string, page, size int) (*DeploymentEnvironmentPage, error) {
	values, total, err := s.records.ListEnvironments(ctx, project, (page-1)*size, size)
	if err != nil {
		return nil, err
	}
	out := &DeploymentEnvironmentPage{List: []DeploymentEnvironmentView{}, Total: total, Page: page, PageSize: size}
	for _, entry := range values {
		_, err := s.adapter(&entry)
		out.List = append(out.List, DeploymentEnvironmentView{DeploymentEnvironment: entry, Configured: err == nil, AllowPipelineDeployments: err == nil && s.targets[entry.TargetRef].AllowPipelineDeployments, Tier: s.environmentTier(&entry, err)})
	}
	return out, nil
}

// Releases 在逐流水线权限校验后分页，禁止泄露无权限来源及总数
func (s *DeploymentService) Releases(ctx context.Context, project string, page, size int, allow func(string) (bool, error)) (*DeploymentReleasePage, error) {
	out := &DeploymentReleasePage{List: []model.DeploymentRelease{}, Page: page, PageSize: size}
	if allow == nil {
		return nil, ErrDeploymentInput
	}
	seen := map[string]bool{}
	permissions := map[string]bool{}
	for offset := 0; ; offset += 100 {
		values, total, err := s.records.ListReleases(ctx, project, offset, 100)
		if err != nil {
			return nil, err
		}
		for _, entry := range values {
			if seen[entry.ReleaseID] {
				continue
			}
			seen[entry.ReleaseID] = true
			allowed, ok := permissions[entry.PipelineID]
			if !ok {
				allowed, err = allow(entry.PipelineID)
				if err != nil {
					return nil, err
				}
				permissions[entry.PipelineID] = allowed
			}
			if !allowed {
				continue
			}
			if out.Total >= int64((page-1)*size) && len(out.List) < size {
				out.List = append(out.List, entry)
			}
			out.Total++
		}
		if len(values) == 0 || int64(offset+len(values)) >= total {
			break
		}
	}
	return out, nil
}

// GetRelease 按项目读取发布身份，由 HTTP 层在展示之前校验流水线权限
func (s *DeploymentService) GetRelease(ctx context.Context, project, id string) (*model.DeploymentRelease, error) {
	entry, err := s.records.GetRelease(ctx, project, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrDeploymentNotFound
	}
	return entry, err
}
func matchesRelease(entry *model.DeploymentRelease, input DeploymentReleaseInput) bool {
	return (entry.Origin == "" || entry.Origin == "manual") && entry.ExecutionStepRunID == nil && entry.EnvironmentID == input.EnvironmentID && entry.PipelineID == input.PipelineID && entry.PipelineRunID == input.PipelineRunID && entry.ArtifactID == input.ArtifactID
}
func (s *DeploymentService) verifyRun(ctx context.Context, project, pipelineID, runID string) (*model.PipelineRun, error) {
	pipeline, err := s.pipelines.Get(ctx, pipelineID)
	if err != nil || pipeline == nil || pipeline.ProjectID != project {
		return nil, ErrDeploymentSource
	}
	run, err := s.pipelines.GetRun(ctx, runID)
	if err != nil || run == nil || run.PipelineID != pipelineID || run.Status != model.PipelineStatusSuccess || deploymentDefinitionRevision(run) == "" {
		return nil, ErrDeploymentSource
	}
	if s.approvals == nil {
		return nil, ErrDeploymentSource
	}
	approvals, err := s.approvals.ListByPipelineRunID(ctx, runID)
	if err != nil {
		return nil, ErrDeploymentSource
	}
	for _, approval := range approvals {
		if approval == nil || approval.PipelineRunID != runID || approval.Status != model.ApprovalStatusApproved {
			return nil, ErrDeploymentSource
		}
	}
	return run, nil
}

// CreateRelease 校验来源归属和内容哈希后固定镜像，重复请求不能改变已选版本
func (s *DeploymentService) CreateRelease(ctx context.Context, project, actor string, input DeploymentReleaseInput) (*model.DeploymentRelease, error) {
	for _, id := range []string{project, actor, input.EnvironmentID, input.PipelineID, input.PipelineRunID, input.ArtifactID, input.RequestID} {
		if artifactutil.Identity(id) != nil {
			return nil, ErrDeploymentInput
		}
	}
	old, err := s.records.GetReleaseByRequest(ctx, project, input.RequestID)
	if err == nil {
		if !matchesRelease(old, input) {
			return nil, repo.ErrDeploymentConflict
		}
		return old, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	environment, err := s.records.GetEnvironment(ctx, project, input.EnvironmentID)
	if err != nil {
		return nil, ErrDeploymentNotFound
	}
	if _, err = s.adapter(environment); err != nil {
		return nil, err
	}
	run, err := s.verifyRun(ctx, project, input.PipelineID, input.PipelineRunID)
	if err != nil {
		return nil, err
	}
	entry, image, err := s.readReleaseArtifact(ctx, project, input.PipelineID, input.PipelineRunID, input.ArtifactID)
	if err != nil {
		return nil, err
	}
	release := &model.DeploymentRelease{Origin: "manual", ReleaseID: uuid.NewString(), ProjectID: project, RequestID: input.RequestID, EnvironmentID: input.EnvironmentID, PipelineID: input.PipelineID, PipelineRunID: input.PipelineRunID, ArtifactID: input.ArtifactID, ArtifactSHA256: entry.SHA256, TargetFingerprint: s.targets[environment.TargetRef].Fingerprint, CommitSHA: deploymentDefinitionRevision(run), Image: image, CreatedBy: actor, Status: "queued", Reason: "Awaiting explicit execution", Version: 1}
	return s.records.CreateRelease(ctx, release)
}

// Cancel 仅取消尚未取得提交基线的发布，绝不宣称撤回已经提交的集群变更
func (s *DeploymentService) Cancel(ctx context.Context, project, id string) (*model.DeploymentRelease, error) {
	entry, err := s.GetRelease(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if entry.Status == "cancelled" {
		return entry, nil
	}
	if entry.Status != "queued" {
		return nil, ErrDeploymentCancel
	}
	changed, err := s.records.Transition(ctx, entry, "cancelled", map[string]any{"reason": "Cancelled before Kubernetes apply"})
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, repo.ErrDeploymentConflict
	}
	return s.GetRelease(ctx, project, id)
}

// Advance 执行一次可重入推进，不创建后台调度器，不从分发成功推断部署成功
func (s *DeploymentService) Advance(ctx context.Context, project, id string) (*model.DeploymentRelease, error) {
	return s.advance(ctx, project, id, "", false, false)
}

// AdvanceAuthorized 推进显式手动发布或观察流水线发布，不恢复不确定的流水线提交
func (s *DeploymentService) AdvanceAuthorized(ctx context.Context, project, id, actor string) (*model.DeploymentRelease, error) {
	return s.advance(ctx, project, id, actor, false, false)
}

func (s *DeploymentService) advance(ctx context.Context, project, id, actor string, automatic, recovery bool) (*model.DeploymentRelease, error) {
	entry, err := s.GetRelease(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if entry.Origin == "pipeline_job" {
		if !automatic && entry.Status == "queued" {
			return nil, ErrPipelineDeploymentDenied
		}
		if !recovery && entry.Status == "applying" {
			return nil, ErrPipelineDeploymentUncertain
		}
		if err := s.authorizePipelineRelease(ctx, entry, actor, automatic); err != nil {
			return nil, err
		}
	} else if entry.Origin != "" && entry.Origin != "manual" {
		return nil, ErrDeploymentSource
	}
	if model.IsDeploymentTerminal(entry.Status) {
		return entry, nil
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
	target := environmentTarget(env)
	if entry.Status == "queued" {
		if err := s.verifyQueuedRelease(ctx, entry, actor); err != nil {
			return nil, err
		}
		baseline, err := adapter.Inspect(ctx, target)
		if err != nil {
			return nil, err
		}
		if baseline.UID == "" || baseline.Generation <= 0 {
			return nil, cluster.ErrUnavailable
		}
		changed, err := s.records.Transition(ctx, entry, "applying", map[string]any{"workload_uid": baseline.UID, "baseline_generation": baseline.Generation, "reason": "Submitting conditional image update"})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, repo.ErrDeploymentConflict
		}
		entry, err = s.GetRelease(ctx, project, id)
		if err != nil {
			return nil, err
		}
	}
	if entry.Status == "applying" {
		if recovery && entry.LastRecoveryBy != actor {
			return nil, repo.ErrDeploymentConflict
		}
		if entry.Origin == "pipeline_job" {
			if err := s.authorizePipelineRelease(ctx, entry, actor, automatic); err != nil {
				return nil, err
			}
		}
		applyCtx := ctx
		if entry.Origin == "pipeline_job" || recovery {
			applyCtx = cluster.WithApplyGuard(ctx, func(checkCtx context.Context) error {
				current, err := s.GetRelease(checkCtx, project, id)
				if err != nil {
					return err
				}
				if current.Status != "applying" || current.Version != entry.Version {
					return repo.ErrDeploymentConflict
				}
				environment, err := s.records.GetEnvironment(checkCtx, project, current.EnvironmentID)
				if err != nil || environment.ActiveReleaseID != current.ReleaseID {
					return repo.ErrDeploymentConflict
				}
				if current.TargetFingerprint != s.targets[environment.TargetRef].Fingerprint {
					return ErrDeploymentTargetChanged
				}
				if current.Origin == "pipeline_job" {
					return s.authorizePipelineRelease(checkCtx, current, actor, automatic)
				}
				return s.pipelinePermission(checkCtx, actor, current.ProjectID, current.PipelineID, current.EnvironmentID, false)
			})
		}
		receipt, err := adapter.Apply(applyCtx, target, entry.ReleaseID, entry.Image, cluster.Receipt{UID: entry.WorkloadUID, Generation: entry.BaselineGeneration})
		if errors.Is(err, cluster.ErrSuperseded) {
			_, updateErr := s.records.Transition(ctx, entry, "superseded", map[string]any{"reason": "Workload identity or generation changed; no overwrite performed"})
			if updateErr != nil {
				return nil, updateErr
			}
			return s.GetRelease(ctx, project, id)
		}
		if err != nil {
			return nil, err
		}
		if receipt.UID != entry.WorkloadUID || receipt.Generation != entry.BaselineGeneration+1 {
			return nil, cluster.ErrUnavailable
		}
		changed, err := s.records.Transition(ctx, entry, "observing", map[string]any{"applied_generation": receipt.Generation, "reason": "Kubernetes accepted image update; awaiting observation"})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, repo.ErrDeploymentConflict
		}
		// 必须使用下一次独立观察确认结果，PATCH 返回不能作为健康证据
		return s.GetRelease(ctx, project, id)
	}
	if entry.Status != "observing" {
		return nil, ErrDeploymentInput
	}
	observed, err := adapter.Observe(ctx, target, entry.ReleaseID, entry.Image, cluster.Receipt{UID: entry.WorkloadUID, Generation: entry.AppliedGeneration})
	if err != nil {
		return nil, err
	}
	switch observed.Status {
	case "observing", "succeeded", "failed", "superseded":
	default:
		return nil, cluster.ErrUnavailable
	}
	_, err = s.records.Transition(ctx, entry, observed.Status, map[string]any{"reason": observed.Reason, "observed_generation": observed.ObservedGeneration, "desired_replicas": observed.DesiredReplicas, "updated_replicas": observed.UpdatedReplicas, "available_replicas": observed.AvailableReplicas, "last_observed_at": time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	return s.GetRelease(ctx, project, id)
}

// DeploymentAction 根据精确的操作员绑定决定发布权限，缺省或未配置目标按生产环境处理
func (s *DeploymentService) DeploymentAction(ctx context.Context, project, id string) (string, error) {
	environment, err := s.records.GetEnvironment(ctx, project, id)
	if err != nil {
		return "", ErrDeploymentNotFound
	}
	tier := "prod"
	if binding, ok := s.targets[environment.TargetRef]; ok && binding.ProjectID == project && binding.Target == environmentTarget(environment) {
		switch binding.Tier {
		case "dev", "staging", "prod":
			tier = binding.Tier
		}
	}
	return "release:deploy_" + tier, nil
}

func deploymentTargetFingerprint(ref string, binding DeploymentTargetBinding, endpoint string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{ref, binding.ProjectID, binding.Target.Namespace, binding.Target.Deployment, binding.Target.Container, binding.Tier, strings.TrimRight(endpoint, "/")}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *DeploymentService) readReleaseArtifact(ctx context.Context, project, pipelineID, runID, artifactID string) (*model.ProjectArtifact, string, error) {
	if s.artifacts == nil || s.files == nil {
		return nil, "", ErrDeploymentSource
	}
	entry, err := s.artifacts.Get(ctx, project, artifactID)
	if err != nil || entry == nil || entry.PipelineID != pipelineID || entry.PipelineRunID != runID || entry.ProjectID != project || entry.Size <= 0 || entry.Size > 4096 || artifactutil.Digest(entry.SHA256) != nil || entry.ExpiresAt != nil && !entry.ExpiresAt.After(time.Now()) {
		return nil, "", ErrDeploymentSource
	}
	file, err := s.files.Open(ctx, project, artifactID)
	if err != nil {
		return nil, "", ErrDeploymentSource
	}
	defer file.Reader.Close()
	payload, err := io.ReadAll(io.LimitReader(file.Reader, 4097))
	if err != nil || len(payload) > 4096 || int64(len(payload)) != entry.Size {
		return nil, "", ErrDeploymentSource
	}
	hash := sha256.Sum256(payload)
	if hex.EncodeToString(hash[:]) != entry.SHA256 {
		return nil, "", ErrDeploymentSource
	}
	manifest := map[string]string{}
	if sonic.Unmarshal(payload, &manifest) != nil || len(manifest) != 1 || !cluster.ValidImage(manifest["image"]) {
		return nil, "", ErrDeploymentSource
	}
	return entry, manifest["image"], nil
}

// Environment 读取精确目标的配置状态，不探测集群或泄露端点与凭据
func (s *DeploymentService) Environment(ctx context.Context, project, id string) (*DeploymentEnvironmentView, error) {
	entry, err := s.records.GetEnvironment(ctx, project, id)
	if err != nil {
		return nil, ErrDeploymentNotFound
	}
	_, err = s.adapter(entry)
	return &DeploymentEnvironmentView{DeploymentEnvironment: *entry, Configured: err == nil, AllowPipelineDeployments: err == nil && s.targets[entry.TargetRef].AllowPipelineDeployments, Tier: s.environmentTier(entry, err)}, nil
}

func deploymentDefinitionRevision(run *model.PipelineRun) string {
	if run == nil {
		return ""
	}
	if run.DefinitionCommitSha != "" {
		if commitPattern.MatchString(run.DefinitionCommitSha) {
			return run.DefinitionCommitSha
		}
		return ""
	}
	// 兼容旧运行的历史字段；仍只表示流水线定义版本，绝不证明构建源码版本
	if commitPattern.MatchString(run.CommitSha) {
		return run.CommitSha
	}
	return ""
}

func (s *DeploymentService) environmentTier(entry *model.DeploymentEnvironment, configurationErr error) string {
	if configurationErr != nil {
		return ""
	}
	return s.targets[entry.TargetRef].Tier
}
