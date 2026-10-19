// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/gitlab"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// realStackPollingSource 仅将固定合成提供方身份映射到本地 Git 定义仓库
// API 使用真实 SnapshotReader 和受信 TLS；生产 HTTPS Git 加载器另有独立测试
// 不能把此测试描述为真实提供方账户或远程提供方验收
// 定义和作业均经过真实 Git，而非返回预制 protobuf 或伪造定义提交
// 此注入仅存在于测试文件，不能由 HTTP 请求或项目配置提供
type realStackPollingSource struct {
	reader interface {
		Read(context.Context, pollsnapshot.Request) (pollsnapshot.Result, error)
	}
	repository, directory, projectID, pipelineID   string
	provider, repositoryName, apiOrigin, targetSHA string
	snapshotVersion, requestsPerSnapshot           int
	mu                                             sync.Mutex
	body                                           []byte
	requests, snapshots, definitions               int
	last                                           pollsnapshot.Result
}

func newRealStackPollingSource(t *testing.T, f *controllerWorkerFixture, directory string) *realStackPollingSource {
	t.Helper()
	s := &realStackPollingSource{directory: directory, projectID: f.projectID, pipelineID: f.pipelineID, provider: model.RepoTypeGitLab, snapshotVersion: gitlab.SnapshotVersion, requestsPerSnapshot: 4}
	const name = "acceptance/nested/configured-repository"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("Authorization") != "" {
			http.Error(w, "fixture accepts only unauthenticated reads", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/" + name:
			_, _ = io.WriteString(w, `{"id":731,"path_with_namespace":"`+name+`"}`)
		case "/api/v4/projects/" + name + "/merge_requests":
			q := r.URL.Query()
			if q.Get("page") != "1" || q.Get("scope") != "all" || q.Get("state") != "all" || q.Get("updated_before") == "" {
				http.Error(w, "fixture requires a bounded complete inventory request", http.StatusBadRequest)
				return
			}
			w.Header().Set("X-Page", "1")
			w.Header().Set("X-Total", "1")
			w.Header().Set("X-Total-Pages", "1")
			w.Header().Set("X-Per-Page", q.Get("per_page"))
			_, _ = w.Write(s.body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	s.repository = server.URL + "/" + name
	s.repositoryName, s.apiOrigin = name, server.URL
	var err error
	s.reader, err = gitlab.NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitLab, BaseURL: server.URL}, server.Client(), gitlab.SnapshotLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *realStackPollingSource) observe(t *testing.T, sha, target string, at time.Time) {
	t.Helper()
	item := map[string]any{"id": 901, "iid": 17, "project_id": 731, "state": "opened", "source_branch": "feature/observed-mr", "target_branch": target, "sha": sha, "updated_at": at.UTC().Format(time.RFC3339Nano)}
	if s.provider != model.RepoTypeGitLab {
		item = map[string]any{
			"id": 901, "number": 17, "state": "open", "merged": false,
			"created_at": "2025-01-01T00:00:00Z", "updated_at": at.UTC().Format(time.RFC3339Nano),
			"head": map[string]any{"ref": "feature/observed-mr", "sha": sha, "repo_id": 732, "repo": map[string]any{"id": 732, "full_name": "contributor/source-fork"}},
			"base": map[string]any{"ref": target, "sha": s.targetSHA, "repo_id": 731, "repo": map[string]any{"id": 731, "full_name": s.repositoryName}},
		}
	}
	body, err := sonic.Marshal([]map[string]any{item})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

// Snapshot 使用真实提供方快照解析器读取本地 TLS 合成响应
func (s *realStackPollingSource) Snapshot(ctx context.Context, p *model.Project, request pollsnapshot.Request) (pollsnapshot.Result, error) {
	if p == nil || p.ProjectID != s.projectID || p.RepoURL != s.repository || p.RepoType != s.provider || request.Repository != s.repositoryName {
		return pollsnapshot.Result{}, fmt.Errorf("unexpected synthetic project identity")
	}
	result, err := s.reader.Read(ctx, request)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots++
	if err == nil {
		s.last = result
	}
	return result, err
}

// Definition 在可信测试映射下克隆真实本地 Git，并读取指定提交的定义 blob
func (s *realStackPollingSource) Definition(ctx context.Context, p *model.Pipeline, project *model.Project, commit string) (string, string, error) {
	if p == nil || project == nil || p.PipelineID != s.pipelineID || p.ProjectID != s.projectID || project.ProjectID != s.projectID || project.RepoType != s.provider || p.RepoURL != s.repository || project.RepoURL != s.repository || p.DefaultBranch != "main" || p.PipelineFilePath != "pipeline.json" || project.AuthType != model.AuthTypeNone || project.Credential != "" {
		return "", "", fmt.Errorf("unexpected configured definition binding")
	}
	dir, err := os.MkdirTemp("", "arcentra-polling-acceptance-definition-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := scm.CloneContext(ctx, scm.GitCloneRequest{Workdir: dir, RepoURL: s.directory, Branch: p.DefaultBranch}); err != nil {
		return "", "", err
	}
	if commit == "" {
		commit, err = scm.HeadSHAContext(ctx, scm.GitHeadSHARequest{Workdir: dir})
		if err != nil {
			return "", "", err
		}
	}
	if len(commit) != 40 || strings.Trim(commit, "0123456789abcdef") != "" {
		return "", "", fmt.Errorf("invalid fixture definition revision")
	}
	command := exec.CommandContext(ctx, "git", "cat-file", "blob", commit+":"+p.PipelineFilePath)
	command.Dir = dir
	content, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("read immutable local Git definition: %w", err)
	}
	s.mu.Lock()
	s.definitions++
	s.mu.Unlock()
	return string(content), commit, nil
}

type realStackPollingEngine struct {
	controlservice.IPipelineEngine
	beforeSubmit func(*model.PipelineRun) error
	submissions  int
}

// Submit 仅在真实 Process 入场前设置确定性撤权边界，不替代任何准入或执行逻辑
func (e *realStackPollingEngine) Submit(run *model.PipelineRun, parsed *spec.Pipeline) error {
	e.submissions++
	if e.beforeSubmit != nil {
		if err := e.beforeSubmit(run); err != nil {
			return err
		}
	}
	return e.IPipelineEngine.Submit(run, parsed)
}

type realStackPollingDownloads interface {
	DownloadStream(context.Context, string) (io.ReadCloser, error)
}

// TestControllerWorkerPollingIntegration 以 SQLite、Kafka 协议模拟和内存对象存储快速验证相同验收场景
// 此测试不声称替代 TestRealStackAcceptance 的真实基础设施证据
func TestControllerWorkerPollingIntegration(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	realStackAssertPolling(t, f, f.store, artifactRPCReference())
}

func realStackPollingActors(t *testing.T, f *controllerWorkerFixture, repositories *repo.Repositories) (actor, triggerGrant string) {
	t.Helper()
	// 仅扩展现有一次性测试 schema；不调用运行时迁移，不改变产品授权种子
	if err := f.db.WithContext(f.ctx).AutoMigrate(&model.SCMPollingStream{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.RoleGrant{}); err != nil {
		t.Fatal(err)
	}
	actor = "polling-actor-" + id.GetUild()
	roles, grants, permissions := []string{}, []string{}, []string{}
	t.Cleanup(func() {
		if err := repo.SetPollingAuthorizer(repositories, nil); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, item := range []struct {
			entity any
			column string
			ids    []string
		}{
			{&model.SCMPollingStream{}, "project_id", []string{f.projectID}},
			{&model.RolePermissionBinding{}, "role_id", roles}, {&model.RoleGrant{}, "grant_id", grants},
			{&model.Role{}, "role_id", roles}, {&model.Permission{}, "permission_id", permissions}, {&model.User{}, "user_id", []string{actor}},
		} {
			if len(item.ids) != 0 {
				if err := f.db.WithContext(ctx).Where(item.column+" IN ?", item.ids).Delete(item.entity).Error; err != nil {
					t.Error(err)
				}
			}
		}
	})
	if err := f.db.WithContext(f.ctx).Create(&model.User{UserID: actor, IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, grantScope := range []struct {
		kind, id string
		actions  []string
	}{
		{"project", f.projectID, []string{"project:read", "project:update"}},
		{"pipeline", f.pipelineID, []string{"pipeline:read", "pipeline:trigger"}},
	} {
		role, grant := "polling-role-"+id.GetUild(), "polling-grant-"+id.GetUild()
		roles, grants = append(roles, role), append(grants, grant)
		for _, row := range []any{&model.Role{RoleID: role, Name: role, ScopeType: grantScope.kind, IsEnabled: 1}, &model.RoleGrant{GrantID: grant, SubjectType: "user", SubjectID: actor, RoleID: role, ScopeType: grantScope.kind, ScopeID: grantScope.id, IsEnabled: 1}} {
			if err := f.db.WithContext(f.ctx).Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, action := range grantScope.actions {
			resource, verb, _ := strings.Cut(action, ":")
			permission := "polling-permission-" + id.GetUild()
			permissions = append(permissions, permission)
			for _, row := range []any{&model.Permission{PermissionID: permission, ResourceType: resource, Action: verb, ScopeType: grantScope.kind}, &model.RolePermissionBinding{RoleID: role, PermissionID: permission}} {
				if err := f.db.WithContext(f.ctx).Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		if grantScope.kind == "pipeline" {
			triggerGrant = grant
		}
	}
	services := controlservice.NewServices(workerTestDatabase{f.db}, nil, repositories)
	if err := services.ConfigurePollingAuthorization(); err != nil {
		t.Fatal(err)
	}
	return actor, triggerGrant
}

// realStackAssertPolling keeps the original GitLab scenario names for existing CI,
// and exercises GitHub/Gitea through the same infrastructure and execution assertions.
func realStackAssertPolling(t *testing.T, f *controllerWorkerFixture, store realStackPollingDownloads, reference taskpayload.ArtifactReference) {
	t.Helper()
	for _, provider := range []string{model.RepoTypeGitLab, model.RepoTypeGitHub, model.RepoTypeGitea} {
		var priorRuns []model.PipelineRun
		if err := f.db.WithContext(f.ctx).Where("pipeline_id = ? AND triggered_by = ?", f.pipelineID, polling.Actor).Find(&priorRuns).Error; err != nil {
			t.Fatal(err)
		}
		priorEvidence := make([]realStackAutomaticEvidence, 0, len(priorRuns))
		for _, run := range priorRuns {
			priorEvidence = append(priorEvidence, realStackReadAutomaticEvidence(t, f, run.RunID))
		}
		if provider != model.RepoTypeGitLab {
			// Fixture setup only: remove the prior provider's paused stream. Immutable
			// run, receipt, checkout and artifact evidence remains available for checks.
			if err := f.db.WithContext(f.ctx).Where("project_id = ?", f.projectID).Delete(&model.SCMPollingStream{}).Error; err != nil {
				t.Fatal(err)
			}
		}
		realStackAssertProviderPolling(t, f, store, reference, provider)
		if t.Failed() {
			return
		}
		for _, evidence := range priorEvidence {
			if !reflect.DeepEqual(evidence, realStackReadAutomaticEvidence(t, f, evidence.Run.RunID)) {
				t.Fatal("switching synthetic provider rewrote earlier provider execution evidence")
			}
		}
	}
}

func realStackAssertProviderPolling(t *testing.T, f *controllerWorkerFixture, store realStackPollingDownloads, reference taskpayload.ArtifactReference, provider string) {
	t.Helper()
	realStackSanitizeGit(t)
	repositories := repo.NewRepositories(workerTestDatabase{f.db}, nil)
	actor, triggerGrant := realStackPollingActors(t, f, repositories)
	directory, initial, observed := controllerCheckoutRepository(t, f.ctx)
	controllerCheckoutGit(t, f.ctx, directory, "branch", "configured-job-source", initial)
	var source *realStackPollingSource
	if provider == model.RepoTypeGitLab {
		source = newRealStackPollingSource(t, f, directory)
	} else {
		source = newRealStackPRPollingSource(t, f, directory, provider)
	}
	source.targetSHA = initial
	prefix := "polling_"
	if provider != model.RepoTypeGitLab {
		prefix += provider + "_"
	}
	expectedScope := polling.Scope{Version: source.snapshotVersion, ProjectID: f.projectID, Provider: provider, APIOrigin: source.apiOrigin, Repository: source.repository, ProviderProjectID: "731", Stream: "merge_requests"}
	if provider != model.RepoTypeGitLab {
		expectedScope.Stream = "pull_requests"
	}
	definition := &spec.Pipeline{Namespace: "controller-agent", Triggers: []*spec.Trigger{{Type: trigger.TriggerTypeEvent, Options: spec.MapAsStruct(map[string]any{trigger.OptionEventType: string(scm.EventTypeMergeRequest), trigger.OptionBranch: "main"})}}, Jobs: []*spec.Job{{Name: "polling-build", Source: &spec.Source{Type: "git", Repo: directory, Branch: "configured-job-source"}, Steps: []*spec.Step{
		controllerWorkerShell("produce", `set -eu; test "$(cat source.txt)" = 'initial checkout'; printf x >> executions; mkdir -p dist; printf 'polling-configured-pipeline' > dist/polling.txt`),
		{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}, "expireIn": "7d"})},
	}}}}
	content, err := spec.MarshalProtoByFormat(definition, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "pipeline.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	controllerCheckoutGit(t, f.ctx, directory, "add", "pipeline.json")
	controllerCheckoutGit(t, f.ctx, directory, "commit", "-m", "synthetic configured polling pipeline")
	definitionCommit := controllerCheckoutGit(t, f.ctx, directory, "rev-parse", "HEAD")
	if initial == definitionCommit || observed == definitionCommit {
		t.Fatal("fixture must distinguish observed MR, definition, and initial source revisions")
	}
	if err := repositories.Project.Update(f.ctx, f.projectID, map[string]any{"repo_type": provider, "repo_url": source.repository, "default_branch": "main", "trigger_mode": model.TriggerModeMR, "is_enabled": 1, "status": model.ProjectStatusActive, "auth_type": model.AuthTypeNone, "credential": ""}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pipeline.Update(f.ctx, f.pipelineID, map[string]any{"repo_url": source.repository, "default_branch": "main", "pipeline_file_path": "pipeline.json"}); err != nil {
		t.Fatal(err)
	}
	engine := &realStackPollingEngine{IPipelineEngine: f.engine}
	service := controlservice.NewPollingService(repositories, source, func() controlservice.IPipelineEngine { return engine }, nil)
	beforeRuns := realStackAutomaticRunCount(t, f)
	var completed realStackAutomaticEvidence
	stream := func(t *testing.T) *model.SCMPollingStream {
		t.Helper()
		row, err := repositories.SCMPolling.GetByProject(f.ctx, f.projectID)
		if err != nil || row == nil {
			t.Fatalf("read persisted polling stream: %v", err)
		}
		return row
	}
	assertProgress := func(t *testing.T, row *model.SCMPollingStream, observations, targets int) {
		t.Helper()
		cursor, err := polling.ParseCursor(row.CursorJSON)
		source.mu.Lock()
		last := source.last
		source.mu.Unlock()
		if err != nil || !cursor.Complete || row.State != polling.StateEnabled || row.PendingBatchJSON != "" || row.PendingObservationJSON != "" || row.LastCompletedAt == nil || row.LastObservationCount != observations || row.LastTargetCount != targets || len(last.Observations) != 1 || cursor.Observed["901"] != last.Observations[0].ObservationID {
			t.Fatalf("polling did not durably complete the expected bounded batch: observations=%d targets=%d error=%v", row.LastObservationCount, row.LastTargetCount, err)
		}
	}
	if !t.Run(prefix+"baseline_enable_changed_observation", func(t *testing.T) {
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		source.mu.Lock()
		requests := source.requests
		source.mu.Unlock()
		if requests != 0 || realStackAutomaticRunCount(t, f) != beforeRuns {
			t.Fatal("unconfigured polling read the provider or created a run")
		}
		source.observe(t, initial, "main", time.Now().Add(-time.Hour))
		baseline, err := service.Baseline(f.ctx, actor, f.projectID, 0, false)
		if err != nil || baseline == nil || baseline.State != polling.StateBaselined || baseline.BaselineID == "" {
			t.Fatalf("explicit baseline failed: %v", err)
		}
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		if realStackAutomaticRunCount(t, f) != beforeRuns || engine.submissions != 0 || stream(t).EnabledBy != "" {
			t.Fatal("baseline implicitly enabled or executed existing history")
		}
		enabled, err := service.Enable(f.ctx, actor, f.projectID, baseline.Revision, baseline.BaselineID, []string{f.pipelineID})
		if err != nil || enabled == nil || enabled.State != polling.StateEnabled || enabled.AuthorizerID != actor {
			t.Fatalf("separate explicit persisted-actor enable failed: %v", err)
		}
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		if realStackAutomaticRunCount(t, f) != beforeRuns || engine.submissions != 0 {
			t.Fatal("enable replayed the baseline revision")
		}
		source.observe(t, observed, "main", time.Now())
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		source.mu.Lock()
		last := source.last
		source.mu.Unlock()
		if len(last.Observations) != 1 {
			t.Fatalf("expected one real reader observation, got %d", len(last.Observations))
		}
		expectedRequest, err := polling.EventRequestID(expectedScope, last.Observations[0].ObservationID)
		if err != nil {
			t.Fatal(err)
		}
		var runs []model.PipelineRun
		if err := f.db.WithContext(f.ctx).Where("pipeline_id = ? AND triggered_by = ? AND request_id = ?", f.pipelineID, polling.Actor, expectedRequest).Find(&runs).Error; err != nil || len(runs) != 1 {
			t.Fatalf("expected exactly one provider-scoped polling run: rows=%d error=%v", len(runs), err)
		}
		run := f.waitRun(runs[0].RunID, model.PipelineStatusSuccess)
		f.wait("polling terminal receipts acknowledged", func() bool {
			f.worker.reports.mu.Lock()
			defer f.worker.reports.mu.Unlock()
			return len(f.worker.reports.entries) == 0
		})
		completed = realStackReadAutomaticEvidence(t, f, run.RunID)
		provenance, err := polling.ParseRunContext(run.PollingContext)
		if err != nil || provenance.Scope != expectedScope || provenance.Target.ObservationID != last.Observations[0].ObservationID || provenance.Target.RequestID != expectedRequest || provenance.Target.SourceSHA != observed || provenance.Target.DefinitionCommitSHA != definitionCommit || provenance.Target.DefinitionPath != "pipeline.json" || run.RequestID != expectedRequest || provenance.ActorID != actor || provenance.Target.MRID != 901 || provenance.Target.MRIID != 17 || provenance.Target.State != "opened" || provenance.Target.UpdatedAt == "" || provenance.Target.TargetBranch != "main" || provenance.Target.SourceBranch != "feature/observed-mr" || run.TriggeredBy != polling.Actor || run.CommitSha != observed || run.DefinitionCommitSha != definitionCommit || run.DefinitionPath != "pipeline.json" || run.Branch != "feature/observed-mr" || run.PollingContextDigest == "" || run.StartTime == nil || run.EndTime == nil || len(completed.Jobs) != 1 || len(completed.Steps) != 2 || len(completed.Artifacts) != 1 || engine.submissions != 1 || realStackAutomaticRunCount(t, f) != beforeRuns+1 {
			t.Fatalf("configured polling run lost exact identity or execution evidence: %v", err)
		}
		if provider != model.RepoTypeGitLab {
			if provenance.Target.SourceProjectID != 732 || provenance.Target.SourceRepository != "contributor/source-fork" || provenance.Target.TargetSHA != initial {
				t.Fatal("persisted PR provenance lost source fork stable identity or target revision")
			}
			observation := last.Observations[0]
			if last.Provider != provider || last.Version != pollsnapshot.Version || last.APIOrigin != source.apiOrigin || last.Repository != source.repositoryName || last.ProviderProjectID != 731 || !strings.HasPrefix(observation.ObservationID, provider+":pr:v2:") || observation.ProviderProjectID != 731 || observation.SourceProjectID != 732 || observation.SourceRepository != "contributor/source-fork" || observation.SourceSHA != observed || observation.TargetSHA != initial || observation.CreatedAt == nil {
				t.Fatal("real PR reader lost provider, repository/fork stable IDs or source/target revision evidence")
			}
		} else if provenance.Target.SourceProjectID != 0 || provenance.Target.SourceRepository != "" || provenance.Target.TargetSHA != "" || strings.Contains(run.PollingContext, `"sourceProjectId"`) || strings.Contains(run.PollingContext, `"sourceRepository"`) || strings.Contains(run.PollingContext, `"targetSha"`) {
			t.Fatal("legacy GitLab provenance unexpectedly acquired v2 PR fields")
		}
		job := completed.Jobs[0]
		if job.Status != model.JobRunStatusSuccess || job.AgentID != f.conf.Agent.ID || job.SourceRepository != directory || job.SourceRef != "configured-job-source" || job.SourceResolvedCommit != initial || job.SourceResolvedCommit == run.CommitSha || job.SourceResolvedCommit == run.DefinitionCommitSha || job.SourceProvenanceState != "reported" || job.SourceBindingDigest == "" {
			t.Fatal("Agent source evidence confused the observed MR or definition SHA with actual configured checkout")
		}
		f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
		realStackAssertOnce(t, f, run.RunID, job.JobRunID)
		for _, step := range completed.Steps {
			if step.Status != 4 || step.Attempt != 1 || step.ExitCode == nil || *step.ExitCode != 0 {
				t.Fatal("polling step did not finish once")
			}
			f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, job.JobRunID)
		}
		artifact := completed.Artifacts[0]
		const want = "polling-configured-pipeline"
		if artifact.StorageID != reference.StorageID || artifact.StorageFingerprint != reference.Fingerprint || artifact.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(want))) || artifact.Size != int64(len(want)) || artifact.PipelineRunID != run.RunID || artifact.JobRunID != job.JobRunID {
			t.Fatal("polling artifact lost producing run or storage identity")
		}
		reader, err := store.DownloadStream(f.ctx, artifact.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		bytes, readErr := io.ReadAll(io.LimitReader(reader, 1024))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || string(bytes) != want {
			t.Fatalf("polling uploaded object bytes mismatch: read=%v close=%v", readErr, closeErr)
		}
		assertProgress(t, stream(t), 1, 1)
		source.mu.Lock()
		reads, snapshots, definitions := source.requests, source.snapshots, source.definitions
		source.mu.Unlock()
		if snapshots != 3 || reads != 3*source.requestsPerSnapshot || definitions != 2 {
			t.Fatalf("expected actual two-pass parser and head/pinned Git reads, got requests=%d snapshots=%d definitions=%d", reads, snapshots, definitions)
		}
	}) || completed.Run == nil {
		return
	}
	if !t.Run(prefix+"duplicate_no_execution", func(t *testing.T) {
		for range 2 {
			if err := service.PollOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(completed, realStackReadAutomaticEvidence(t, f, completed.Run.RunID)) || realStackAutomaticRunCount(t, f) != beforeRuns+1 || engine.submissions != 1 {
			t.Fatal("duplicate polling changed execution or artifact evidence")
		}
		realStackAssertOnce(t, f, completed.Run.RunID, completed.Jobs[0].JobRunID)
		assertProgress(t, stream(t), 0, 0)
	}) {
		return
	}
	if !t.Run(prefix+"no_eligible_ref_completes", func(t *testing.T) {
		before := stream(t)
		source.observe(t, observed, "release/unselected", time.Now())
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		after := stream(t)
		assertProgress(t, after, 1, 0)
		if after.Revision <= before.Revision || after.CursorJSON == before.CursorJSON || engine.submissions != 1 || realStackAutomaticRunCount(t, f) != beforeRuns+1 || !reflect.DeepEqual(completed, realStackReadAutomaticEvidence(t, f, completed.Run.RunID)) {
			t.Fatal("unmatched target ref ran or failed to acknowledge complete progress")
		}
	}) {
		return
	}
	t.Run(prefix+"revoke_before_admission", func(t *testing.T) {
		before := stream(t)
		var deniedRunID, frozenBatch string
		engine.beforeSubmit = func(run *model.PipelineRun) error {
			stored, err := f.repos.Pipeline.GetRun(f.ctx, run.RunID)
			if err != nil || stored == nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil {
				return fmt.Errorf("revocation boundary lacks the real persisted pending run: %v", err)
			}
			deniedRunID = run.RunID
			frozenBatch = stream(t).PendingBatchJSON
			return repositories.RoleGrant.DisableGrant(f.ctx, triggerGrant)
		}
		source.observe(t, initial, "main", time.Now())
		if err := service.PollOnce(f.ctx); err == nil {
			t.Fatal("revoked live permission was accepted by Process")
		}
		if deniedRunID == "" || frozenBatch == "" || engine.submissions != 2 {
			t.Fatal("test did not reach the persisted-run/pre-Process revocation boundary")
		}
		after := stream(t)
		batch, err := polling.ParseBatch(after.PendingBatchJSON)
		if err != nil || after.State != polling.StatePaused || after.PauseReason != "authorization_denied" || after.ActivationGeneration <= before.ActivationGeneration || after.CursorJSON != before.CursorJSON || len(batch.Targets) != 1 || len(batch.Results) != 1 || batch.Results[0].RunID != deniedRunID || batch.Results[0].Outcome != polling.OutcomeRejected {
			t.Fatalf("revocation lost safe paused state, unacknowledged batch, or typed rejection: %v", err)
		}
		if provider != model.RepoTypeGitLab && (batch.Targets[0].SourceProjectID != 732 || batch.Targets[0].SourceRepository != "contributor/source-fork" || batch.Targets[0].TargetSHA != initial) {
			t.Fatal("revocation lost frozen PR repository provenance")
		}
		denied := realStackReadAutomaticEvidence(t, f, deniedRunID)
		deniedProvenance, err := polling.ParseRunContext(denied.Run.PollingContext)
		if err != nil || deniedProvenance.Scope != expectedScope || deniedProvenance.Target != batch.Targets[0] {
			t.Fatalf("revoked run lost frozen provider observation evidence: %v", err)
		}
		if denied.Run.Status != model.PipelineStatusFailed || denied.Run.StartTime != nil || denied.Run.EndTime == nil || len(denied.Jobs) != 0 || len(denied.Steps) != 0 || len(denied.Artifacts) != 0 || realStackAutomaticRunCount(t, f) != beforeRuns+2 {
			t.Fatal("revoked pending observation created execution evidence")
		}
		realStackAssertNoAutomaticDispatch(t, f, deniedRunID)
		// 恢复同一测试授权不能使粘性暂停自动复活；本场景不执行恢复或历史重放
		if err := f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", triggerGrant).Update("is_enabled", 1).Error; err != nil {
			t.Fatal(err)
		}
		if err := service.PollOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, stream(t)) || !reflect.DeepEqual(denied, realStackReadAutomaticEvidence(t, f, deniedRunID)) || !reflect.DeepEqual(completed, realStackReadAutomaticEvidence(t, f, completed.Run.RunID)) || engine.submissions != 2 || realStackAutomaticRunCount(t, f) != beforeRuns+2 {
			t.Fatal("permission restoration resumed polling or rewrote persisted evidence")
		}
		realStackAssertNoAutomaticDispatch(t, f, deniedRunID)
	})
}
