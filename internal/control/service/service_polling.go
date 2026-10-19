// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	tmpl "github.com/arcentrix/arcentra/internal/shared/pipeline/template"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/bitbucket"
	"github.com/arcentrix/arcentra/pkg/scm/gitea"
	"github.com/arcentrix/arcentra/pkg/scm/gitee"
	"github.com/arcentrix/arcentra/pkg/scm/github"
	"github.com/arcentrix/arcentra/pkg/scm/gitlab"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// PollingSource 定义可信服务构造时注入的只读来源，不接受 HTTP 请求指定实现
// 实际提供方访问与定义读取均在数据库事务外完成
type PollingSource interface {
	Snapshot(context.Context, *model.Project, pollsnapshot.Request) (pollsnapshot.Result, error)
	Definition(context.Context, *model.Pipeline, *model.Project, string) (string, string, error)
}

// definitionReader is a private trusted-construction test seam; HTTP input cannot set it.
type productionPollingSource struct{ definitionReader pollingDefinitionReader }

func (productionPollingSource) Snapshot(ctx context.Context, p *model.Project, request pollsnapshot.Request) (pollsnapshot.Result, error) {
	if !pollingProjectEligible(p) {
		return pollsnapshot.Result{}, polling.ErrBlocked
	}
	repository, e := polling.CanonicalRepository(p.RepoURL)
	if e != nil {
		return pollsnapshot.Result{}, polling.ErrBlocked
	}
	u, _ := url.Parse(repository)
	cfg := scm.ProviderConfig{Kind: scm.ProviderKind(p.RepoType), BaseURL: u.Scheme + "://" + u.Host}
	if p.AuthType == model.AuthTypeToken {
		cfg.Token = p.Credential
	}
	switch p.RepoType {
	case model.RepoTypeGitLab:
		reader, err := gitlab.NewSnapshotReader(cfg, nil, gitlab.SnapshotLimits{})
		if err != nil {
			return pollsnapshot.Result{}, err
		}
		return reader.Read(ctx, request)
	case model.RepoTypeGitHub:
		reader, err := github.NewSnapshotReader(cfg, nil, pollsnapshot.Limits{})
		if err != nil {
			return pollsnapshot.Result{}, err
		}
		return reader.Read(ctx, request)
	case model.RepoTypeGitea:
		reader, err := gitea.NewSnapshotReader(cfg, nil, pollsnapshot.Limits{})
		if err != nil {
			return pollsnapshot.Result{}, err
		}
		return reader.Read(ctx, request)
	case model.RepoTypeGitee:
		reader, err := gitee.NewSnapshotReader(cfg, nil, pollsnapshot.Limits{})
		if err != nil {
			return pollsnapshot.Result{}, err
		}
		return reader.Read(ctx, request)
	case model.RepoTypeBitbucket:
		reader, err := bitbucket.NewSnapshotReader(cfg, nil, pollsnapshot.Limits{})
		if err != nil {
			return pollsnapshot.Result{}, err
		}
		return reader.Read(ctx, request)
	default:
		return pollsnapshot.Result{}, polling.ErrBlocked
	}
}
func (source productionPollingSource) Definition(ctx context.Context, p *model.Pipeline, project *model.Project, commit string) (string, string, error) {
	read := source.definitionReader
	if read == nil {
		read = scm.ReadHTTPSDefinitionContext
	}
	return loadPollingDefinition(ctx, p, project, commit, commit != "", read)
}

// PollingService 只为已显式激活的受支持 MR/PR 快照流建立有界派发
type PollingService struct {
	pollMu       sync.Mutex
	initErr      error
	repositories *repo.Repositories
	source       PollingSource
	engine       func() IPipelineEngine
	resolver     tmpl.ITemplateResolver
}

// NewPollingService 建立依赖注入服务，不执行网络读取或隐式开启流
func NewPollingService(repositories *repo.Repositories, source PollingSource, engine func() IPipelineEngine, resolver tmpl.ITemplateResolver) *PollingService {
	if source == nil {
		source = productionPollingSource{}
	}
	var view *repo.Repositories
	var err error
	if repositories == nil {
		err = fmt.Errorf("polling repositories unavailable")
	} else {
		view, err = repositories.PollingPrimaryRepositories()
	}
	return &PollingService{repositories: view, source: source, engine: engine, resolver: resolver, initErr: err}
}

// PollingDefinitionDigest 绑定 include 展开并解析后的确定性 protobuf 定义
func PollingDefinitionDigest(parsed *spec.Pipeline) (string, error) {
	if parsed == nil {
		return "", polling.ErrBlocked
	}
	data, e := (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(data)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

func (s *PollingService) ready() error {
	if s != nil && s.initErr != nil {
		return s.initErr
	}
	if s == nil || s.repositories == nil || s.repositories.SCMPolling == nil || s.repositories.Project == nil || s.repositories.Pipeline == nil || s.source == nil {
		return fmt.Errorf("polling services unavailable")
	}
	return nil
}
func (s *PollingService) project(ctx context.Context, id string) (*model.Project, error) {
	if e := s.ready(); e != nil {
		return nil, e
	}
	p, e := s.repositories.Project.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	if p == nil || p.ProjectID != id {
		return nil, polling.ErrForbidden
	}
	return p, nil
}
func (s *PollingService) readAccess(ctx context.Context, actor string, p *model.Project) (authz.Subject, authz.IAuthorizer, error) {
	subject, e := pollingSubject(ctx, s.repositories, actor)
	if e != nil {
		return subject, nil, e
	}
	checker, e := authz.NewLiveAuthorizer(ctx, s.repositories)
	if e != nil {
		return subject, nil, e
	}
	allowed, e := checker.Check(ctx, subject, "project:read", authz.ResourceRef{Type: "project", ID: p.ProjectID, ProjectID: p.ProjectID, OrgID: p.OrgID})
	if e != nil {
		return subject, nil, e
	}
	if !allowed {
		return subject, nil, polling.ErrForbidden
	}
	return subject, checker, nil
}

// PollingScopeView 仅展示不含凭据的提供方身份
type PollingScopeView struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Stream     string `json:"stream"`
}

// PollingCapabilities 表示当前请求的提示，不能取代变更事务内授权
type PollingCapabilities struct {
	CanBaseline bool `json:"canBaseline"`
	CanEnable   bool `json:"canEnable"`
	CanDisable  bool `json:"canDisable"`
}

// PollingPipelineView 只展示本次当前读权限允许的流水线
type PollingPipelineView struct {
	PipelineID string `json:"pipelineId"`
	Name       string `json:"name"`
}

// PollingStatus 不输出游标、原始配置、令牌或隐藏流水线数据
type PollingStatus struct {
	NextRetryAt          *time.Time `json:"nextRetryAt,omitempty"`
	LastPollError        string     `json:"lastPollError,omitempty"`
	LastCompletedAt      *time.Time `json:"lastCompletedAt,omitempty"`
	LastObservationCount *int       `json:"lastObservationCount,omitempty"`
	LastTargetCount      *int       `json:"lastTargetCount,omitempty"`

	Supported                bool                  `json:"supported"`
	State                    string                `json:"state"`
	Revision                 uint64                `json:"revision"`
	BaselineID               string                `json:"baselineId,omitempty"`
	BaselineCompletedAt      *time.Time            `json:"baselineCompletedAt,omitempty"`
	BaselineObservationCount *int                  `json:"baselineObservationCount,omitempty"`
	AuthorizerID             string                `json:"authorizerId,omitempty"`
	Scope                    *PollingScopeView     `json:"scope,omitempty"`
	SelectedPipelines        []PollingPipelineView `json:"selectedPipelines"`
	Blocker                  string                `json:"blocker"`
	Capabilities             PollingCapabilities   `json:"capabilities"`
}

// Status 读取受权限过滤的配置状态，不读取提供方或改变游标
func (s *PollingService) Status(ctx context.Context, actor, projectID string) (*PollingStatus, error) {
	p, e := s.project(ctx, projectID)
	if e != nil {
		return nil, e
	}
	subject, checker, e := s.readAccess(ctx, actor, p)
	if e != nil {
		return nil, e
	}
	out := &PollingStatus{State: polling.StateDisabled, SelectedPipelines: []PollingPipelineView{}}
	repository, e := polling.CanonicalRepository(p.RepoURL)
	out.Supported = e == nil && pollingProjectEligible(p)
	if !out.Supported {
		out.Blocker = "requires_existing_enabled_supported_pr_project"
	}
	if out.Supported {
		_, _, stream, err := polling.ProviderRepository(p.RepoType, repository)
		if err != nil {
			return nil, polling.ErrBlocked
		}
		out.Scope = &PollingScopeView{Provider: p.RepoType, Repository: repository, Stream: stream}
	}
	canUpdate, e := checker.Check(ctx, subject, "project:update", authz.ResourceRef{Type: "project", ID: projectID, ProjectID: projectID, OrgID: p.OrgID})
	if e != nil {
		return nil, e
	}
	row, e := s.repositories.SCMPolling.GetByProject(ctx, projectID)
	if e != nil {
		return nil, e
	}
	out.Capabilities.CanBaseline = out.Supported && canUpdate
	if row == nil {
		return out, nil
	}
	out.State = row.State
	out.Revision = row.Revision
	if row.PauseReason != "" {
		out.Blocker = row.PauseReason
	}
	out.Capabilities.CanBaseline = out.Supported && canUpdate && row.State != polling.StateEnabled
	out.Capabilities.CanDisable = canUpdate && (row.State == polling.StateEnabled || row.State == polling.StatePaused || row.State == polling.StateBaselined)
	out.Capabilities.CanEnable = out.Supported && canUpdate && row.State == polling.StateBaselined
	// 基线统计及启用者仅向有配置权限者展示；项目读取者不获隐含权限目录或批次计数
	if canUpdate {
		out.BaselineID = row.BaselineID
		out.BaselineCompletedAt = row.BaselineCompletedAt
		count := row.BaselineCount
		out.BaselineObservationCount = &count
		out.AuthorizerID = row.EnabledBy
		out.NextRetryAt = row.NextPollAt
		out.LastPollError = row.LastPollError
		out.LastCompletedAt = row.LastCompletedAt
		observed, matched := row.LastObservationCount, row.LastTargetCount
		out.LastObservationCount = &observed
		out.LastTargetCount = &matched
	}
	if row.SelectionJSON != "" {
		selection, e := polling.ParseSelection(row.SelectionJSON)
		if e != nil {
			return nil, e
		}
		for _, binding := range selection.Pipelines {
			allowed, e := checker.Check(ctx, subject, "pipeline:read", authz.ResourceRef{Type: "pipeline", ID: binding.PipelineID, ProjectID: projectID, OrgID: p.OrgID})
			if e != nil {
				return nil, e
			}
			if !allowed {
				continue
			}
			pipeline, e := s.repositories.Pipeline.Get(ctx, binding.PipelineID)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			if e != nil {
				return nil, e
			}
			if pipeline == nil || pipeline.ProjectID != projectID {
				continue
			}
			out.SelectedPipelines = append(out.SelectedPipelines, PollingPipelineView{PipelineID: pipeline.PipelineID, Name: pipeline.Name})
		}
	}
	return out, nil
}

// PollingCandidate 保存已授权现有流水线的绑定及就绪提示
type PollingCandidate struct {
	PipelineID     string `json:"pipelineId"`
	Name           string `json:"name"`
	DefaultBranch  string `json:"defaultBranch"`
	DefinitionPath string `json:"definitionPath"`
	CanEnable      bool   `json:"canEnable"`
	Eligible       bool   `json:"eligible"`
	Blocker        string `json:"blocker"`
}

// PollingCandidates 是有界分页视图，Total 只计当前可读取的项目流水线
type PollingCandidates struct {
	Items    []PollingCandidate `json:"items"`
	Total    int                `json:"total"`
	PageNum  int                `json:"pageNum"`
	PageSize int                `json:"pageSize"`
}

// Candidates 分页枚举全部现有流水线后按当前读取权限过滤
func (s *PollingService) Candidates(ctx context.Context, actor, projectID string, page, size int) (*PollingCandidates, error) {
	if page < 1 || size < 1 || size > 100 {
		return nil, polling.ErrBlocked
	}
	p, e := s.project(ctx, projectID)
	if e != nil {
		return nil, e
	}
	subject, checker, e := s.readAccess(ctx, actor, p)
	if e != nil {
		return nil, e
	}
	canUpdate, e := checker.Check(ctx, subject, "project:update", authz.ResourceRef{Type: "project", ID: projectID, ProjectID: projectID, OrgID: p.OrgID})
	if e != nil {
		return nil, e
	}
	output := &PollingCandidates{Items: []PollingCandidate{}, PageNum: page, PageSize: size}
	for dbPage := 1; dbPage <= 100; dbPage++ {
		rows, total, e := s.repositories.Pipeline.List(ctx, &repo.PipelineQuery{ProjectID: projectID, Page: dbPage, PageSize: 100})
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			if row == nil || row.ProjectID != projectID {
				return nil, polling.ErrConflict
			}
			scope := authz.ResourceRef{Type: "pipeline", ID: row.PipelineID, ProjectID: projectID, OrgID: p.OrgID}
			allowed, e := checker.Check(ctx, subject, "pipeline:read", scope)
			if e != nil {
				return nil, e
			}
			if !allowed {
				continue
			}
			output.Total++
			if output.Total <= (page-1)*size || output.Total > page*size {
				continue
			}
			item := PollingCandidate{PipelineID: row.PipelineID, Name: row.Name, DefaultBranch: row.DefaultBranch, DefinitionPath: row.PipelineFilePath}
			pipelineRepo, pe := polling.CanonicalRepository(row.RepoURL)
			projectRepo, re := polling.CanonicalRepository(p.RepoURL)
			_, pathErr := validatedPipelinePath(row.PipelineFilePath)
			item.Eligible = pollingProjectEligible(p) && row.IsEnabled == 1 && row.IsDeleted == 0 && pe == nil && re == nil && pipelineRepo == projectRepo && row.DefaultBranch != "" && pathErr == nil
			if !item.Eligible {
				item.Blocker = "pipeline_repository_ref_or_state_not_eligible"
			}
			triggerAllowed, e := checker.Check(ctx, subject, "pipeline:trigger", scope)
			if e != nil {
				return nil, e
			}
			item.CanEnable = item.Eligible && canUpdate && triggerAllowed
			if item.Eligible && !item.CanEnable {
				item.Blocker = "insufficient_current_trigger_authority"
			}
			output.Items = append(output.Items, item)
		}
		if int64(dbPage*100) >= total {
			return output, nil
		}
	}
	return nil, fmt.Errorf("polling candidate enumeration exceeds bound")
}

func snapshotScope(p *model.Project, result pollsnapshot.Result) (polling.Scope, error) {
	if !pollingProjectEligible(p) {
		return polling.Scope{}, polling.ErrBlocked
	}
	repository, e := polling.CanonicalRepository(p.RepoURL)
	if e != nil {
		return polling.Scope{}, e
	}
	version, origin, stream, e := polling.ProviderRepository(p.RepoType, repository)
	if e != nil {
		return polling.Scope{}, polling.ErrBlocked
	}
	u, _ := url.Parse(repository)
	expectedProvider := p.RepoType
	if p.RepoType == model.RepoTypeGitLab {
		expectedProvider = ""
	} // Preserve exact legacy v1 evidence.
	if result.Version != version || result.Provider != expectedProvider || result.APIOrigin != origin || result.Repository != strings.TrimPrefix(u.Path, "/") {
		return polling.Scope{}, polling.ErrBlocked
	}
	projectKey := strconv.FormatInt(result.ProviderProjectID, 10)
	if version == polling.ScopeVersion3 {
		if result.ProviderProjectID != 0 || result.ProviderProjectKey == "" {
			return polling.Scope{}, polling.ErrBlocked
		}
		projectKey = result.ProviderProjectKey
	} else if result.ProviderProjectID <= 0 || result.ProviderProjectKey != "" {
		return polling.Scope{}, polling.ErrBlocked
	}
	scope := polling.Scope{Version: version, ProjectID: p.ProjectID, Provider: p.RepoType, APIOrigin: origin, Repository: repository, ProviderProjectID: projectKey, Stream: stream}
	return scope, scope.Validate()
}
func pollingSnapshotObservation(observation pollsnapshot.Observation) polling.Observation {
	mergedAt := ""
	if observation.MergedAt != nil {
		mergedAt = observation.MergedAt.UTC().Format(time.RFC3339Nano)
	}
	return polling.Observation{MRID: observation.ID, MRIID: observation.IID, State: observation.State, UpdatedAt: observation.UpdatedAt.UTC().Format(time.RFC3339Nano), MergedAt: mergedAt, MergeSHA: observation.MergeSHA, SquashSHA: observation.SquashSHA, SourceBranch: observation.SourceBranch, TargetBranch: observation.TargetBranch, SourceSHA: observation.SourceSHA, ObservationID: observation.ObservationID, EvidenceVersion: observation.EvidenceVersion, ProviderProjectKey: observation.ProviderProjectKey, SourceProjectKey: observation.SourceProjectKey, MergeEvidence: observation.MergeEvidence, SourceProjectID: observation.SourceProjectID, SourceRepository: observation.SourceRepository, TargetSHA: observation.TargetSHA}
}

func snapshotCursor(scope polling.Scope, result pollsnapshot.Result, previous *polling.Cursor) (polling.Cursor, error) {
	next := polling.Cursor{Version: polling.Version, Boundary: result.UpperBound.UTC().Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{}}
	if previous != nil {
		for k, v := range previous.Observed {
			next.Observed[k] = v
		}
	}
	seen := map[int64]bool{}
	for _, observation := range result.Observations {
		if observation.UpdatedAt.After(result.UpperBound) {
			return polling.Cursor{}, polling.ErrBlocked
		}
		if scope.Version == polling.ScopeVersion3 && observation.ProviderProjectID != 0 {
			return polling.Cursor{}, polling.ErrBlocked
		}
		if seen[observation.ID] {
			return polling.Cursor{}, polling.ErrBlocked
		}
		seen[observation.ID] = true
		if err := pollingSnapshotObservation(observation).ValidateForScope(scope); err != nil {
			return polling.Cursor{}, polling.ErrBlocked
		}
		next.Observed[strconv.FormatInt(observation.ID, 10)] = observation.ObservationID
	}
	_, e := next.Canonical()
	return next, e
}
func (s *PollingService) readSnapshot(ctx context.Context, p *model.Project, scope *polling.Scope, cursor *polling.Cursor) (pollsnapshot.Result, error) {
	repository, e := polling.CanonicalRepository(p.RepoURL)
	if e != nil {
		return pollsnapshot.Result{}, e
	}
	u, _ := url.Parse(repository)
	request := pollsnapshot.Request{Repository: strings.TrimPrefix(u.Path, "/"), UpperBound: time.Now().UTC()}
	if scope != nil {
		if e := scope.Validate(); e != nil {
			return pollsnapshot.Result{}, e
		}
		if scope.Version == polling.ScopeVersion3 {
			request.ProviderProjectKey = scope.ProviderProjectID
		} else {
			request.ProviderProjectID, e = strconv.ParseInt(scope.ProviderProjectID, 10, 64)
			if e != nil {
				return pollsnapshot.Result{}, e
			}
		}
	}
	// 首版每次重新读取有界完整库存，不能仅靠更新时间游标证明延迟可见项不存在
	_ = cursor

	return s.source.Snapshot(ctx, p, request)
}

// Baseline 显式读取完整库存并保存非执行基线，不产生运行
func (s *PollingService) Baseline(ctx context.Context, actor, projectID string, revision uint64, discard bool) (*PollingStatus, error) {
	p, e := s.project(ctx, projectID)
	if e != nil {
		return nil, e
	}
	if e = authorizePollingActor(ctx, s.repositories, actor, projectID, nil); e != nil {
		return nil, e
	}
	if !pollingProjectEligible(p) {
		return nil, polling.ErrBlocked
	}
	started := time.Now().UTC()
	result, e := s.readSnapshot(ctx, p, nil, nil)
	if e != nil {
		return nil, e
	}
	scope, e := snapshotScope(p, result)
	if e != nil {
		return nil, e
	}
	cursor, e := snapshotCursor(scope, result, nil)
	if e != nil {
		return nil, e
	}
	_, e = s.repositories.SCMPolling.Baseline(ctx, repo.BaselineInput{ProjectID: projectID, ActorID: actor, ExpectedRevision: revision, Scope: scope, Cursor: cursor, StartedAt: started, CompletedAt: time.Now().UTC(), DiscardPending: discard})
	if e != nil {
		return nil, e
	}
	return s.Status(ctx, actor, projectID)
}

// Enable 只授权显式选择的已存在流水线，当前事务必须再次校验全部权限
func (s *PollingService) Enable(ctx context.Context, actor, projectID string, revision uint64, baselineID string, pipelineIDs []string) (*PollingStatus, error) {
	if e := s.ready(); e != nil {
		return nil, e
	}
	_, e := s.repositories.SCMPolling.Enable(ctx, repo.EnableInput{ProjectID: projectID, ActorID: actor, ExpectedRevision: revision, BaselineID: baselineID, PipelineIDs: pipelineIDs})
	if e != nil {
		return nil, e
	}
	return s.Status(ctx, actor, projectID)
}

// Disable 停止新增准入，不取消或回滚已接受运行
func (s *PollingService) Disable(ctx context.Context, actor, projectID string, revision uint64) (*PollingStatus, error) {
	if e := s.ready(); e != nil {
		return nil, e
	}
	_, e := s.repositories.SCMPolling.Disable(ctx, repo.DisableInput{ProjectID: projectID, ActorID: actor, ExpectedRevision: revision})
	if e != nil {
		return nil, e
	}
	return s.Status(ctx, actor, projectID)
}

func (s *PollingService) definition(ctx context.Context, p *model.Pipeline, project *model.Project, commit string) (*spec.Pipeline, string, string, error) {
	content, actual, e := s.source.Definition(ctx, p, project, commit)
	if e != nil {
		return nil, "", "", e
	}
	if commit != "" && actual != commit {
		return nil, "", "", polling.ErrBlocked
	}
	if s.resolver != nil {
		content, e = tmpl.ResolveIncludes(ctx, content, s.resolver, model.TemplateScopeProject, p.ProjectID)
		if e != nil {
			return nil, "", "", e
		}
	}
	parsed, e := spec.ParseContentToProto(content, pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
	if e != nil {
		return nil, "", "", e
	}

	digest, e := PollingDefinitionDigest(parsed)
	return parsed, actual, digest, e
}

func validatePollingExecutable(parsed *spec.Pipeline) error {
	for _, job := range parsed.Jobs {
		if job != nil && job.Approval != nil && job.Approval.Plugin == "builtin:retry-approval" {
			return polling.ErrBlocked
		}
	}
	plan, e := deployment.DecodePipelinePlan(parsed)
	if e != nil || plan != nil {
		return polling.ErrBlocked
	}
	return nil
}

// PollOnce 仅处理显式启用的持久流，空配置不读取外部提供方
func (s *PollingService) PollOnce(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("polling services unavailable")
	}
	if !s.pollMu.TryLock() {
		return nil
	}
	defer s.pollMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if e := s.ready(); e != nil {
		return e
	}
	if s.engine == nil || isNilPipelineEngine(s.engine()) {
		return fmt.Errorf("polling engine unavailable")
	}
	rows, e := s.repositories.SCMPolling.ListEnabled(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, row := range rows {
		if e := ctx.Err(); e != nil {
			return e
		}
		row, attemptErr := s.repositories.SCMPolling.RecordAttempt(ctx, row.ProjectID, row.Revision, row.ActivationGeneration)
		if attemptErr != nil {
			failures = append(failures, fmt.Errorf("polling attempt could not be recorded"))
			continue
		}
		if e := s.pollStream(ctx, row); e != nil {
			log.Warnw("polling stream remains unresolved", "projectId", row.ProjectID, "reason", pollingFailureReason(e))
			failures = append(failures, fmt.Errorf("polling stream could not advance"))
		}
	}
	return errors.Join(failures...)
}
func pollingFailureReason(e error) string {
	if errors.Is(e, polling.ErrForbidden) {
		return "authorization_denied"
	}
	if errors.Is(e, polling.ErrUnresolved) {
		return "recovery_required"
	}
	if errors.Is(e, gitlab.ErrSnapshotIncomplete) || errors.Is(e, pollsnapshot.ErrIncomplete) {
		return "provider_incomplete"
	}
	return "unavailable"
}

func pollingStoredScope(row *model.SCMPollingStream) (polling.Scope, error) {
	var scope polling.Scope
	if row == nil {
		return scope, polling.ErrConflict
	}
	if e := sonic.UnmarshalString(row.ScopeJSON, &scope); e != nil {
		return scope, e
	}
	digest, e := scope.Digest()
	if e != nil || digest != row.ScopeDigest || scope.ProjectID != row.ProjectID {
		return scope, polling.ErrConflict
	}
	return scope, nil
}
func (s *PollingService) pause(ctx context.Context, row *model.SCMPollingStream, reason string, cause error) error {
	if row == nil {
		return cause
	}
	_, e := s.repositories.SCMPolling.Pause(ctx, repo.PauseInput{ProjectID: row.ProjectID, ExpectedRevision: row.Revision, ActivationGeneration: row.ActivationGeneration, Reason: reason})
	return errors.Join(cause, e)
}
func (s *PollingService) pollStream(ctx context.Context, row *model.SCMPollingStream) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if row == nil || row.State != polling.StateEnabled {
		return polling.ErrConflict
	}
	scope, e := pollingStoredScope(row)
	if e != nil {
		return e
	}
	project, e := s.project(ctx, row.ProjectID)
	if e != nil {
		return e
	}
	repository, re := polling.CanonicalRepository(project.RepoURL)
	if re != nil || repository != scope.Repository || !pollingProjectEligible(project) {
		return s.pause(ctx, row, "configuration_changed", polling.ErrBlocked)
	}
	selection, e := polling.ParseSelection(row.SelectionJSON)
	if e != nil {
		return e
	}
	sd, e := selection.Digest()
	if e != nil || sd != row.SelectionDigest {
		return polling.ErrConflict
	}
	ids := make([]string, 0, len(selection.Pipelines))
	for _, p := range selection.Pipelines {
		ids = append(ids, p.PipelineID)
	}
	if e := authorizePollingActor(ctx, s.repositories, row.EnabledBy, row.ProjectID, ids); e != nil {
		if errors.Is(e, polling.ErrForbidden) {
			return s.pause(ctx, row, "authorization_denied", e)
		}
		return e
	}
	var batch *polling.Batch
	if row.PendingBatchJSON != "" {
		batch, e = polling.ParseBatchForScope(row.PendingBatchJSON, scope)
		if e != nil {
			return e
		}
	} else {
		var observed *polling.ObservedBatch
		if row.PendingObservationJSON != "" {
			observed, e = polling.ParseObservedBatchForScope(row.PendingObservationJSON, scope)
			if e != nil {
				return e
			}
		} else {
			cursor, e := polling.ParseCursor(row.CursorJSON)
			if e != nil {
				return e
			}
			snapshot, e := s.readSnapshot(ctx, project, &scope, cursor)
			if e != nil {
				return s.deferProvider(ctx, row, e)
			}
			currentScope, e := snapshotScope(project, snapshot)
			if e != nil || currentScope != scope {
				return s.pause(ctx, row, "configuration_changed", polling.ErrBlocked)
			}
			next, e := snapshotCursor(scope, snapshot, cursor)
			if e != nil {
				if scope.Version == polling.ScopeVersion3 {
					return s.pause(ctx, row, "provider_incomplete", e)
				}
				return e
			}
			observed = &polling.ObservedBatch{Version: polling.Version, ID: id.GetUild(), ScopeDigest: row.ScopeDigest, Generation: row.ActivationGeneration, SelectionDigest: row.SelectionDigest, Revision: row.Revision + 1, NextCursor: next, Observations: []polling.Observation{}}
			for _, observation := range snapshot.Observations {
				if previous, seen := cursor.Observed[strconv.FormatInt(observation.ID, 10)]; seen && previous == observation.ObservationID {
					continue
				}
				observed.Observations = append(observed.Observations, pollingSnapshotObservation(observation))
			}
			if _, validateErr := observed.CanonicalForScope(scope); validateErr != nil {
				return s.pause(ctx, row, "provider_incomplete", polling.ErrBlocked)
			}
			row, e = s.repositories.SCMPolling.CaptureObservations(ctx, repo.CaptureObservationsInput{ProjectID: row.ProjectID, ExpectedRevision: row.Revision, ActivationGeneration: row.ActivationGeneration, Batch: *observed})
			if e != nil {
				return e
			}
			observed, e = polling.ParseObservedBatchForScope(row.PendingObservationJSON, scope)
			if e != nil {
				return e
			}
		}
		batch = &polling.Batch{Version: polling.Version, ID: observed.ID, ScopeDigest: observed.ScopeDigest, Generation: observed.Generation, SelectionDigest: observed.SelectionDigest, Revision: row.Revision + 1, NextCursor: observed.NextCursor, ObservationCount: len(observed.Observations), Targets: []polling.Target{}}

		type definition struct {
			value          *model.Pipeline
			spec           *spec.Pipeline
			commit, digest string
		}
		definitions := map[string]definition{}
		for _, observation := range observed.Observations {
			// 新可见的旧记录只补库存；显式基线不允许重放其前的历史变化
			cutoff, cutoffErr := time.Parse(time.RFC3339Nano, row.BaselineCutoff)
			if cutoffErr != nil {
				return polling.ErrConflict
			}
			observedAt, parseErr := pollingObservationTime(scope.Provider, observation)
			if parseErr != nil {
				return parseErr
			}
			if !observedAt.After(cutoff) {
				continue
			}
			if observation.State != "opened" && observation.State != "merged" {
				continue
			}
			eventType := string(scm.EventTypeMergeRequest)
			if observation.State == "merged" {
				eventType = string(scm.EventTypeMergeRequestMerged)
			}
			for _, binding := range selection.Pipelines {
				loaded, ok := definitions[binding.PipelineID]
				if !ok {
					pipeline, e := s.repositories.Pipeline.Get(ctx, binding.PipelineID)
					if e != nil {
						return e
					}
					if pipeline == nil || pipeline.ProjectID != row.ProjectID || pipeline.DefaultBranch != binding.DefaultBranch || pipeline.PipelineFilePath != binding.DefinitionPath || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
						return s.pause(ctx, row, "configuration_changed", polling.ErrBlocked)
					}
					repoURL, e := polling.CanonicalRepository(pipeline.RepoURL)
					if e != nil || repoURL != binding.Repository {
						return s.pause(ctx, row, "configuration_changed", polling.ErrBlocked)
					}
					parsed, commit, digest, e := s.definition(ctx, pipeline, project, "")
					if e != nil {
						return s.pause(ctx, row, "definition_unavailable", fmt.Errorf("polling definition unavailable"))
					}
					loaded = definition{value: pipeline, spec: parsed, commit: commit, digest: digest}
					definitions[binding.PipelineID] = loaded
				}
				if !trigger.MatchAnyEventTrigger(loaded.spec, eventType, observation.TargetBranch) {
					continue
				}
				if e := validatePollingExecutable(loaded.spec); e != nil {
					return s.pause(ctx, row, "definition_rejected", e)
				}
				requestID, e := polling.EventRequestID(scope, observation.ObservationID)
				if e != nil {
					return e
				}

				batch.Targets = append(batch.Targets, polling.Target{MRID: observation.MRID, MRIID: observation.MRIID, State: observation.State, UpdatedAt: observation.UpdatedAt, MergedAt: observation.MergedAt, MergeSHA: observation.MergeSHA, SquashSHA: observation.SquashSHA, PipelineID: binding.PipelineID, ObservationID: observation.ObservationID, RequestID: requestID, SourceBranch: observation.SourceBranch, TargetBranch: observation.TargetBranch, SourceSHA: observation.SourceSHA, EvidenceVersion: observation.EvidenceVersion, ProviderProjectKey: observation.ProviderProjectKey, SourceProjectKey: observation.SourceProjectKey, MergeEvidence: observation.MergeEvidence, SourceProjectID: observation.SourceProjectID, SourceRepository: observation.SourceRepository, TargetSHA: observation.TargetSHA, DefinitionCommitSHA: loaded.commit, DefinitionPath: binding.DefinitionPath, ResolvedDefinitionDigest: loaded.digest})
			}
		}
		if _, validateErr := batch.CanonicalForScope(scope); validateErr != nil {
			return s.pause(ctx, row, "definition_rejected", polling.ErrBlocked)
		}
		row, e = s.repositories.SCMPolling.CaptureBatch(ctx, repo.CaptureBatchInput{ProjectID: row.ProjectID, ExpectedRevision: row.Revision, ActivationGeneration: row.ActivationGeneration, Batch: *batch})
		if e != nil {
			return e
		}
		batch, e = polling.ParseBatchForScope(row.PendingBatchJSON, scope)
		if e != nil {
			return e
		}
	}
	creator, ok := s.repositories.Pipeline.(interface {
		CreatePollingRun(context.Context, *model.PipelineRun) (polling.CreationOutcome, error)
	})
	if !ok {
		return fmt.Errorf("polling run creation unavailable")
	}
	for _, target := range batch.Targets {
		alreadyRecorded := false
		for _, receipt := range batch.Results {
			if receipt.PipelineID == target.PipelineID && receipt.RequestID == target.RequestID {
				alreadyRecorded = true
				break
			}
		}
		if alreadyRecorded {
			continue
		} // 最终 Ack 仍在锁内校验原始持久 run 与回执，不把本地记录视为授权
		pipeline, e := s.repositories.Pipeline.Get(ctx, target.PipelineID)
		if e != nil {
			return e
		}
		if pipeline == nil || pipeline.ProjectID != row.ProjectID {
			return polling.ErrConflict
		}
		parsed, commit, digest, e := s.definition(ctx, pipeline, project, target.DefinitionCommitSHA)
		if e != nil || commit != target.DefinitionCommitSHA || digest != target.ResolvedDefinitionDigest {
			return s.pause(ctx, row, "definition_unavailable", fmt.Errorf("frozen polling definition no longer available"))
		}
		if e := validatePollingExecutable(parsed); e != nil {
			return s.pause(ctx, row, "definition_rejected", e)
		}
		run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: target.PipelineID, RequestID: target.RequestID, PipelineName: pipeline.Name, Branch: target.SourceBranch, CommitSha: target.SourceSHA, DefinitionCommitSha: target.DefinitionCommitSHA, DefinitionPath: target.DefinitionPath, Status: model.PipelineStatusPending, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT), TriggeredBy: polling.Actor, Env: "{}"}
		evidence := polling.RunContext{Version: polling.Version, BaselineID: row.BaselineID, BaselineCutoff: row.BaselineCutoff, StreamID: row.StreamID, Scope: scope, ScopeDigest: row.ScopeDigest, ActivationGeneration: row.ActivationGeneration, ActorID: row.EnabledBy, SelectionDigest: row.SelectionDigest, BatchID: batch.ID, BatchRevision: batch.Revision, RunID: run.RunID, Target: target}
		run.PollingContext, e = evidence.Canonical()
		if e != nil {
			return e
		}
		run.PollingContextDigest, e = evidence.Digest()
		if e != nil {
			return e
		}
		created, e := creator.CreatePollingRun(ctx, run)
		if e != nil {
			return e
		}
		if created.CreatedNow {
			if created.Outcome != polling.OutcomeCreated || created.RunID != run.RunID {
				return polling.ErrConflict
			}
			engine := s.engine()
			if isNilPipelineEngine(engine) {
				return s.markUnresolved(ctx, row, polling.ErrUnresolved)
			}
			submitErr := engine.Submit(run, parsed)
			// 重新读取只判断可确认的持久回执，绝不在错误或重启后再次 Submit
			confirmed, checkErr := creator.CreatePollingRun(ctx, run)
			if checkErr != nil {
				return errors.Join(submitErr, checkErr)
			}
			if confirmed.Outcome != polling.OutcomeAdmitted && confirmed.Outcome != polling.OutcomeRejected {
				return s.markUnresolved(ctx, row, errors.Join(submitErr, polling.ErrUnresolved))
			}
		} else if created.Outcome != polling.OutcomeAdmitted && created.Outcome != polling.OutcomeRejected {
			return s.markUnresolved(ctx, row, polling.ErrUnresolved)
		}
		// 每个准入可能更新流修订，下一次失败暂停及最终 Ack 使用最新状态
		row, e = s.repositories.SCMPolling.GetByProject(ctx, row.ProjectID)
		if e != nil {
			return e
		}
		if row == nil || row.State != polling.StateEnabled || row.ActivationGeneration != batch.Generation {
			return polling.ErrConflict
		}
	}
	row, e = s.repositories.SCMPolling.GetByProject(ctx, row.ProjectID)
	if e != nil {
		return e
	}
	if row == nil {
		return polling.ErrConflict
	}
	_, e = s.repositories.SCMPolling.AcknowledgeBatch(ctx, repo.AcknowledgeBatchInput{ProjectID: row.ProjectID, ExpectedRevision: row.Revision, ActivationGeneration: batch.Generation, BatchID: batch.ID})
	return e
}

// New providers require explicit operator recovery after invalid identity or
// evidence. Transient movement/rate/transport failures retain bounded backoff.
// Legacy provider behavior is unchanged.
func pollingV3HardEvidenceFailure(scope polling.Scope, cause error) bool {
	if scope.Version != polling.ScopeVersion3 {
		return false
	}
	var evidence *pollsnapshot.Error
	if !errors.As(cause, &evidence) {
		return false
	}
	switch evidence.Reason {
	case pollsnapshot.ReasonConfiguration, pollsnapshot.ReasonRequest,
		pollsnapshot.ReasonMalformed, pollsnapshot.ReasonIdentity,
		pollsnapshot.ReasonPagination, pollsnapshot.ReasonUnsupportedEvidence,
		pollsnapshot.ReasonAuthentication, pollsnapshot.ReasonForbidden:
		return true
	default:
		return false
	}
}

func (s *PollingService) deferProvider(ctx context.Context, row *model.SCMPollingStream, cause error) error {
	scope, scopeErr := pollingStoredScope(row)
	if scopeErr != nil {
		return scopeErr
	}
	if pollingV3HardEvidenceFailure(scope, cause) {
		return s.pause(ctx, row, "provider_incomplete", cause)
	}
	next := time.Now().Add(time.Minute)
	reason := "provider_unavailable"
	var legacy *gitlab.SnapshotError
	var neutral *pollsnapshot.Error
	var status int
	var retryAt, resetAt time.Time
	var remaining *int64
	if errors.As(cause, &legacy) {
		status, retryAt, resetAt, remaining = legacy.StatusCode, legacy.RetryAt, legacy.RateLimitResetAt, legacy.RateLimitRemaining
		reason = "provider_incomplete"
	} else if errors.As(cause, &neutral) {
		status, retryAt, resetAt, remaining = neutral.StatusCode, neutral.RetryAt, neutral.RateLimitResetAt, neutral.RateLimitRemaining
		reason = "provider_incomplete"
	}
	if reason == "provider_incomplete" {
		if status == 429 {
			reason = "provider_rate_limited"
		}
		if retryAt.After(next) {
			next = retryAt
		}
		if resetAt.After(next) && remaining != nil && *remaining == 0 {
			next = resetAt
		}
	}
	if next.After(time.Now().Add(24 * time.Hour)) {
		return s.pause(ctx, row, "provider_unavailable", cause)
	}
	_, e := s.repositories.SCMPolling.ScheduleRetry(ctx, row.ProjectID, row.Revision, row.ActivationGeneration, next, reason)
	return errors.Join(cause, e)
}

func (s *PollingService) markUnresolved(ctx context.Context, row *model.SCMPollingStream, cause error) error {
	_, err := s.repositories.SCMPolling.MarkUnresolved(ctx, row.ProjectID, row.Revision, row.ActivationGeneration)
	return errors.Join(cause, err)
}

// Gitea's manual merge API may expose the commit author time as merged_at.
// Its eligibility, and v3 provider eligibility with incomplete merge evidence, uses
// the observed update, never an inferred merge-action time.
// Stable merge identities already seen at baseline remain deduplicated separately.
func pollingObservationTime(provider string, observation polling.Observation) (time.Time, error) {
	raw := observation.UpdatedAt
	if observation.State == "merged" && provider != model.RepoTypeGitea && provider != model.RepoTypeGitee && provider != model.RepoTypeBitbucket {
		raw = observation.MergedAt
	}
	return time.Parse(time.RFC3339Nano, raw)
}
