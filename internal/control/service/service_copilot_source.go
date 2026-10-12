// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/scm/sourcefile"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
)

const copilotSourceFiles = 3
const copilotSourceFileBytes = 8192

// CopilotSourceFile 只允许选择当前运行内的作业及相对路径，禁止指定仓库或提交
// 路径受固定 GitHub 读取器的普通文本文件规则约束
type CopilotSourceFile struct {
	JobRunID string `json:"jobRunId"`
	Path     string `json:"path"`
}

// CopilotSourceJob 仅展示受授权的作业初始检出身份和明确的不可用原因
// reported 不代表最终工作区或构建证明
type CopilotSourceJob struct {
	JobRunID        string `json:"jobRunId"`
	Name            string `json:"name"`
	Status          int    `json:"status"`
	Available       bool   `json:"available"`
	Reason          string `json:"reason"`
	Repository      string `json:"repository,omitempty"`
	RequestedRef    string `json:"requestedRef,omitempty"`
	ResolvedCommit  string `json:"resolvedCommit,omitempty"`
	ProvenanceState string `json:"provenanceState"`
}

type copilotSourceReader interface {
	ReadFileAtCommit(context.Context, string, string, string, string) (sourcefile.File, error)
}

// ConfigureSourceContext 在服务发布前配置默认关闭的 GitHub 源读取，不接受端点或客户端凭据
// 实例重启会使旧预览失效；只允许在路由初始化时调用
func (s *CopilotService) ConfigureSourceContext(config copilot.SourceContextConfig) error {
	s.sourceAllowed = map[string]bool{}
	if !config.Enabled {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return ErrCopilotContext
	}
	s.sourceKey = key
	s.sourceReader = sourcefile.NewReader()
	s.sourcePrimary = s.repos.CopilotPrimaryRepositories
	for _, id := range config.AllowedProjectIDs {
		if copilot.ValidID(id) {
			s.sourceAllowed[id] = true
		}
	}
	return nil
}

func (s *CopilotService) sourceEnabled(projectID string) bool {
	return s != nil && s.sourceAllowed[projectID] && s.sourceReader != nil && s.sourcePrimary != nil && len(s.sourceKey) == 32
}

// sourceScope 刻意不使用输入中的团队或缓存鉴权器，也不复制请求锁和取消状态
func (s *CopilotService) sourceScope(ctx context.Context, subject authz.Subject, projectID, runID string) (*CopilotService, *model.Project, *model.PipelineRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if subject.IsAgent || !copilot.ValidID(subject.UserID) || !copilot.ValidID(projectID) || !copilot.ValidID(runID) {
		return nil, nil, nil, ErrCopilotDenied
	}
	if !s.sourceEnabled(projectID) {
		return nil, nil, nil, ErrCopilotContext
	}
	repositories, err := s.sourcePrimary(ctx)
	if err != nil || repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil {
		return nil, nil, nil, ErrCopilotContext
	}
	if _, ok := repositories.Secret.(repo.ICopilotSecretValueReader); !ok {
		return nil, nil, nil, ErrCopilotSecrets
	}
	actors, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return nil, nil, nil, ErrCopilotContext
	}
	user, err := actors.GetExecutionActor(ctx, subject.UserID)
	if err != nil || user == nil || user.UserID != subject.UserID || user.IsEnabled != 1 {
		return nil, nil, nil, ErrCopilotDenied
	}
	members, err := repositories.TeamMember.ListUserTeams(ctx, subject.UserID)
	if err != nil || len(members) > 1000 {
		return nil, nil, nil, ErrCopilotDenied
	}
	liveSubject := authz.Subject{UserID: subject.UserID}
	ids, seen := []string{}, map[string]bool{}
	for _, member := range members {
		if member.UserID != subject.UserID || !copilot.ValidID(member.TeamID) {
			return nil, nil, nil, ErrCopilotDenied
		}
		if !seen[member.TeamID] {
			ids = append(ids, member.TeamID)
			seen[member.TeamID] = true
		}
	}
	if len(ids) > 0 {
		teams, err := repositories.Team.BatchGet(ctx, ids)
		if err != nil {
			return nil, nil, nil, ErrCopilotDenied
		}
		for _, team := range teams {
			if team == nil || !seen[team.TeamID] {
				return nil, nil, nil, ErrCopilotDenied
			}
			if team.IsEnabled == 1 {
				liveSubject.TeamIDs = append(liveSubject.TeamIDs, team.TeamID)
			}
		}
	}
	authorizer, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return nil, nil, nil, ErrCopilotDenied
	}
	local := &CopilotService{repos: repositories, authorizer: authorizer, registry: s.registry, secrets: s.secrets, sourceSubject: liveSubject}
	if s.artifacts != nil {
		local.artifacts = &ArtifactService{artifacts: repositories.Artifact, storageRepo: repositories.Storage, root: s.artifacts.root, newStorage: s.artifacts.newStorage}
	}
	project, err := local.project(ctx, liveSubject, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	if project.IsEnabled != 1 || project.Status != model.ProjectStatusActive {
		return nil, nil, nil, ErrCopilotDenied
	}
	run, err := local.run(ctx, liveSubject, project, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	pipeline, err := repositories.Pipeline.Get(ctx, run.PipelineID)
	if err != nil || pipeline == nil || pipeline.PipelineID != run.PipelineID || pipeline.ProjectID != projectID || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
		return nil, nil, nil, ErrCopilotDenied
	}
	return local, project, run, nil
}

func sourceProjectCredential(project *model.Project) (string, string, string) {
	if project.RepoType != model.RepoTypeGit && project.RepoType != model.RepoTypeGitHub {
		return "", "", "Only GitHub.com HTTPS repositories are supported"
	}
	repository, err := sourcefile.CanonicalRepository(project.RepoURL)
	if err != nil {
		return "", "", "Only GitHub.com HTTPS repositories are supported"
	}
	token := ""
	switch project.AuthType {
	case model.AuthTypeNone:
	case model.AuthTypeToken:
		token = strings.TrimSpace(project.Credential)
		if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
			return "", "", "Only public repositories or configured token authentication are supported"
		}
	default:
		return "", "", "Only public repositories or configured token authentication are supported"
	}
	return repository, token, ""
}

func sourceJobView(project *model.Project, job *model.JobRun) CopilotSourceJob {
	out := CopilotSourceJob{JobRunID: job.JobRunID, Name: job.JobName, Status: job.Status, ProvenanceState: "unknown"}
	repository, _, reason := sourceProjectCredential(project)
	if reason != "" {
		out.Reason = reason
		return out
	}
	var view PipelineJobView
	populateJobSourceView(&view, job)
	if !view.SourceExpectationCaptured || view.SourceProvenanceState != "reported" || view.SourceType != "git" {
		out.Reason = "This job has no usable reported initial checkout"
		return out
	}
	jobRepository, err := sourcefile.CanonicalRepository(view.SourceRepository)
	if err != nil {
		out.Reason = "Only GitHub.com HTTPS repositories are supported"
		return out
	}
	if jobRepository != repository {
		out.Reason = "The current project repository does not match this job's source"
		return out
	}
	if len(view.SourceResolvedCommit) != 40 {
		out.Reason = "This reported commit format is unsupported"
		return out
	}
	out.Available, out.Repository, out.RequestedRef, out.ResolvedCommit, out.ProvenanceState = true, repository, view.SourceRef, view.SourceResolvedCommit, "reported"
	return out
}

type copilotSourceState struct {
	owner             *CopilotService
	subject           authz.Subject
	project           *model.Project
	run               *model.PipelineRun
	selection         []CopilotSourceFile
	contextSelection  CopilotSelection
	jobs              []*model.JobRun
	steps             []*model.StepRun
	extraSteps        []*model.StepRun
	registeredSecrets []string
	artifacts         map[string]*model.ProjectArtifact
	seal              string
	baseSeal          string
}

func (s *CopilotService) loadSourceState(ctx context.Context, subject authz.Subject, projectID, runID string, selection CopilotSelection) (*CopilotService, *copilotSourceState, error) {
	local, project, run, err := s.sourceScope(ctx, subject, projectID, runID)
	if err != nil {
		return nil, nil, err
	}
	if local.repos.JobRun == nil {
		return nil, nil, ErrCopilotContext
	}
	state := &copilotSourceState{owner: s, subject: authz.Subject{UserID: subject.UserID}, project: project, run: run, selection: selection.SourceFiles, contextSelection: selection}
	ids, seen := []string{}, map[string]bool{}
	for _, selected := range selection.SourceFiles {
		job, err := local.repos.JobRun.GetByJobRunID(ctx, selected.JobRunID)
		if err != nil || job == nil || job.JobRunID != selected.JobRunID || job.PipelineRunID != run.RunID || job.PipelineID != run.PipelineID {
			return nil, nil, ErrCopilotNotFound
		}
		if !sourceJobView(project, job).Available {
			return nil, nil, ErrCopilotContext
		}
		state.jobs = append(state.jobs, job)
		if !seen[job.JobRunID] {
			seen[job.JobRunID] = true
			ids = append(ids, job.JobRunID)
		}
	}
	reader, ok := local.repos.JobRun.(repo.ICopilotSourceReader)
	if !ok {
		return nil, nil, ErrCopilotSecrets
	}
	steps, err := reader.ListCopilotSourceSteps(ctx, run.RunID, ids)
	if err != nil || len(steps) > 200 {
		return nil, nil, ErrCopilotSecrets
	}
	stepIDs := map[string]bool{}
	for _, step := range steps {
		if step == nil || !copilot.ValidID(step.StepRunID) || stepIDs[step.StepRunID] || step.PipelineID != run.PipelineID || step.PipelineRunID != run.RunID || !seen[step.JobRunID] {
			return nil, nil, ErrCopilotSecrets
		}
		stepIDs[step.StepRunID] = true
	}
	counts := map[string]int{}
	for _, step := range steps {
		counts[step.JobRunID]++
	}
	for _, job := range state.jobs {
		if job.TotalSteps < 0 || counts[job.JobRunID] < job.TotalSteps {
			return nil, nil, ErrCopilotSecrets
		}
	}
	state.steps = steps
	values := copilotSourceBaseValues(subject, project, run)
	state.baseSeal = s.sourceSeal(values...)
	for i, job := range state.jobs {
		values = append(values, job.JobRunID, job.PipelineID, job.PipelineRunID, job.SourceType, job.SourceRepository, job.SourceRef, job.SourceBindingDigest, job.SourceResolvedCommit, job.SourceProvenanceState, job.AgentID, job.ClaimID, job.CancellationClaimID, job.TerminalReportDigest, job.Env, state.selection[i].Path)
	}
	for _, step := range steps {
		values = append(values, step.StepRunID, step.JobRunID, step.Env, step.Secrets)
	}
	extra, extraSteps, artifacts, err := local.sourceExtraState(ctx, project, run, selection)
	if err != nil {
		return nil, nil, err
	}
	values = append(values, extra...)
	state.extraSteps = extraSteps
	state.artifacts = artifacts
	registered, err := local.sourceSecretSnapshot(ctx, project, run)
	if err != nil {
		return nil, nil, err
	}
	values = append(values, registered...)
	state.registeredSecrets = registered
	state.seal = s.sourceSeal(values...)
	if local.artifacts != nil && len(state.artifacts) > 0 {
		local.artifacts.artifacts = &copilotExpectedArtifacts{IArtifactRepository: local.artifacts.artifacts, expected: state.artifacts}
	}
	return local, state, nil
}

func (s *CopilotService) sourceSeal(values ...string) string {
	h := hmac.New(sha256.New, s.sourceKey)
	for _, value := range values {
		_, _ = h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (state *copilotSourceState) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, current, err := state.owner.loadSourceState(ctx, state.subject, state.project.ProjectID, state.run.RunID, state.contextSelection)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !hmac.Equal([]byte(state.seal), []byte(current.seal)) {
		return ErrCopilotPreview
	}
	// 秘密枚举可能较慢，完成后再次刷新权限及当前仓库凭据，再交还给网络发送边界
	_, project, run, err := state.owner.sourceScope(ctx, state.subject, state.project.ProjectID, state.run.RunID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !hmac.Equal([]byte(state.baseSeal), []byte(state.owner.sourceSeal(copilotSourceBaseValues(state.subject, project, run)...))) {
		return ErrCopilotPreview
	}
	return ctx.Err()
}

func (state *copilotSourceState) read(ctx context.Context, index int) (sourcefile.File, error) {
	if err := state.verify(ctx); err != nil {
		return sourcefile.File{}, err
	}
	repository, token, _ := sourceProjectCredential(state.project)
	job, selected := state.jobs[index], state.selection[index]
	file, err := state.owner.sourceReader.ReadFileAtCommit(ctx, repository, job.SourceResolvedCommit, selected.Path, token)
	if ctx.Err() != nil {
		return sourcefile.File{}, ctx.Err()
	}
	if err != nil || file.Repository != repository || file.Commit != job.SourceResolvedCommit || file.Path != selected.Path || !sourceprovenance.ValidCommit(file.BlobSHA) || len(file.BlobSHA) != 40 || (file.Mode != "100644" && file.Mode != "100755") || len(file.Content) > copilotSourceFileBytes || !utf8.Valid(file.Content) || strings.ContainsRune(string(file.Content), 0) || strings.HasPrefix(string(file.Content), "version https://git-lfs.github.com/spec/v1") {
		return sourcefile.File{}, ErrCopilotContext
	}
	hash := sha1.New() // Git 对象完整性使用标准 blob SHA-1 帧
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(file.Content))
	_, _ = hash.Write(file.Content)
	if hex.EncodeToString(hash.Sum(nil)) != file.BlobSHA {
		return sourcefile.File{}, ErrCopilotContext
	}
	return file, nil
}

func (s *CopilotService) prepareScope(ctx context.Context, subject authz.Subject, projectID string, in CopilotRequest) (*CopilotService, *model.Project, *model.PipelineRun, *copilotSourceState, error) {
	if len(in.Context.SourceFiles) > 0 {
		local, state, err := s.loadSourceState(ctx, subject, projectID, in.RunID, in.Context)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil, nil, ctx.Err()
			}
			return nil, nil, nil, nil, err
		}
		return local, state.project, state.run, state, nil
	}
	project, err := s.project(ctx, subject, projectID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	run, err := s.run(ctx, subject, project, in.RunID)
	return s, project, run, nil, err
}

func (s *CopilotService) sourceSecretSnapshot(ctx context.Context, project *model.Project, run *model.PipelineRun) ([]string, error) {
	if s.repos.Secret == nil {
		return nil, ErrCopilotSecrets
	}
	seen, size := map[string]string{}, 0
	identities := map[string]bool{}
	for _, scope := range []struct{ kind, id string }{{"project", project.ProjectID}, {"pipeline", run.PipelineID}, {"global", ""}} {
		rows, total, err := s.repos.Secret.List(ctx, 1, 1000, "", scope.kind, scope.id, "")
		if err != nil || len(rows) > 1000 || total != int64(len(rows)) {
			return nil, ErrCopilotSecrets
		}
		for _, secret := range rows {
			if secret == nil || !copilot.ValidID(secret.SecretID) {
				return nil, ErrCopilotSecrets
			}
			// 主库可能没有业务键唯一约束，重复或大小写别名不能由 GetValue 随机选中一行
			identity := strings.ToLower(secret.SecretID)
			if identities[identity] {
				return nil, ErrCopilotSecrets
			}
			identities[identity] = true
			var value string
			var err error
			if unique, ok := s.repos.Secret.(repo.ICopilotSecretValueReader); ok {
				value, err = unique.GetCopilotSecretValue(ctx, secret.SecretID)
			} else {
				value, err = s.repos.Secret.GetValue(ctx, secret.SecretID)
			}
			size += len(value)
			if err != nil || len(seen) >= 1000 || size > 512*1024 {
				return nil, ErrCopilotSecrets
			}
			seen[secret.SecretID] = value
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	values := make([]string, 0, len(ids)*2)
	for _, id := range ids {
		values = append(values, id, seen[id])
	}
	return values, nil
}

// sourceExtraState 绑定混合请求中其它作业的步骤秘密及制品归属，避免慢速源码读取跨越秘密轮换
func (s *CopilotService) sourceExtraState(ctx context.Context, project *model.Project, run *model.PipelineRun, selection CopilotSelection) ([]string, []*model.StepRun, map[string]*model.ProjectArtifact, error) {
	values, steps := []string{}, []*model.StepRun{}
	artifacts := map[string]*model.ProjectArtifact{}
	ids := append([]string{}, selection.StepRunIDs...)
	producerJobs := map[string]string{}
	for _, id := range selection.ArtifactIDs {
		if s.artifacts == nil {
			return nil, nil, nil, ErrCopilotContext
		}
		artifact, err := s.artifacts.Get(ctx, project.ProjectID, id)
		if err != nil || artifact == nil || artifact.ArtifactID != id || artifact.ProjectID != project.ProjectID || artifact.PipelineRunID != run.RunID || artifact.PipelineID != run.PipelineID {
			return nil, nil, nil, ErrCopilotNotFound
		}
		artifacts[id] = artifact
		encoded, err := sonic.MarshalString(artifact)
		if err != nil || len(encoded) > 32768 {
			return nil, nil, nil, ErrCopilotContext
		}
		values = append(values, encoded, artifact.StorageID, artifact.StorageFingerprint, artifact.StoragePath, artifact.SHA256)
		ids = append(ids, artifact.StepRunID)
		if previous, ok := producerJobs[artifact.StepRunID]; ok && previous != artifact.JobRunID {
			return nil, nil, nil, ErrCopilotNotFound
		}
		producerJobs[artifact.StepRunID] = artifact.JobRunID
	}
	for _, id := range ids {
		if s.repos.StepRun == nil {
			return nil, nil, nil, ErrCopilotContext
		}
		step, err := s.repos.StepRun.GetByStepRunID(ctx, id)
		if err != nil || step == nil || step.StepRunID != id || step.PipelineRunID != run.RunID || step.PipelineID != run.PipelineID {
			return nil, nil, nil, ErrCopilotNotFound
		}
		if job, ok := producerJobs[id]; ok && step.JobRunID != job {
			return nil, nil, nil, ErrCopilotNotFound
		}
		values = append(values, step.StepRunID, step.JobRunID, step.PipelineID, step.PipelineRunID, step.Env, step.Secrets)
		steps = append(steps, step)
	}
	return values, steps, artifacts, nil
}

// copilotExpectedArtifacts 拒绝预览准备期间制品元数据的 A→B→A 替换，读取前先与已封存快照比较
type copilotExpectedArtifacts struct {
	repo.IArtifactRepository
	expected map[string]*model.ProjectArtifact
}

func (r *copilotExpectedArtifacts) Get(ctx context.Context, projectID, artifactID string) (*model.ProjectArtifact, error) {
	expected := r.expected[artifactID]
	if expected == nil || expected.ProjectID != projectID {
		return nil, ErrCopilotNotFound
	}
	current, err := r.IArtifactRepository.Get(ctx, projectID, artifactID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, expected) {
		return nil, ErrCopilotPreview
	}
	return current, nil
}

func (state *copilotSourceState) matchesStep(step *model.StepRun) bool {
	for _, expected := range append(append([]*model.StepRun{}, state.steps...), state.extraSteps...) {
		if expected.StepRunID == step.StepRunID {
			return expected.PipelineID == step.PipelineID && expected.PipelineRunID == step.PipelineRunID && expected.JobRunID == step.JobRunID && expected.Env == step.Env && expected.Secrets == step.Secrets
		}
	}
	return false
}

func copilotSourceBaseValues(subject authz.Subject, project *model.Project, run *model.PipelineRun) []string {
	return []string{"copilot-source-state-v1", subject.UserID, project.ProjectID, project.OrgID, project.RepoURL, project.RepoType, fmt.Sprint(project.AuthType), project.Credential, project.WebhookSecret, string(project.EnvVars), run.RunID, run.PipelineID, run.Env}
}
