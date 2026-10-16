// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
)

type pollingSafetySource struct {
	*realStackPollingSource
	failDefinition bool
}

// Definition 在真实本地 Git 读取前注入确定性失败，快照仍使用真实 TLS 解析器
func (s *pollingSafetySource) Definition(ctx context.Context, pipeline *model.Pipeline, project *model.Project, commit string) (string, string, error) {
	if s.failDefinition {
		return "", "", fmt.Errorf("synthetic definition unavailable")
	}
	return s.realStackPollingSource.Definition(ctx, pipeline, project, commit)
}

type pollingSafetyFixture struct {
	f                        *controllerWorkerFixture
	repositories             *repo.Repositories
	source                   *pollingSafetySource
	service                  *controlservice.PollingService
	engine                   *realStackPollingEngine
	actor, initial, observed string
}

func newPollingSafetyFixture(t *testing.T, definition *spec.Pipeline) *pollingSafetyFixture {
	t.Helper()
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	realStackSanitizeGit(t)
	repositories := repo.NewRepositories(workerTestDatabase{f.db}, nil)
	actor, _ := realStackPollingActors(t, f, repositories)
	directory, initial, observed := controllerCheckoutRepository(t, f.ctx)
	source := &pollingSafetySource{realStackPollingSource: newRealStackPollingSource(t, f, directory)}
	if definition == nil {
		definition = &spec.Pipeline{Namespace: "controller-agent", Triggers: []*spec.Trigger{{Type: trigger.TriggerTypeEvent, Options: spec.MapAsStruct(map[string]any{trigger.OptionEventType: string(scm.EventTypeMergeRequest), trigger.OptionBranch: "main"})}}, Jobs: []*spec.Job{{Name: "safety", Steps: []*spec.Step{controllerWorkerShell("once", "printf x >> executions")}}}}
	}
	content, err := spec.MarshalProtoByFormat(definition, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "pipeline.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	controllerCheckoutGit(t, f.ctx, directory, "add", "pipeline.json")
	controllerCheckoutGit(t, f.ctx, directory, "commit", "-m", "synthetic polling safety definition")
	if err := repositories.Project.Update(f.ctx, f.projectID, map[string]any{"repo_type": model.RepoTypeGitLab, "repo_url": source.repository, "default_branch": "main", "trigger_mode": model.TriggerModeMR, "is_enabled": 1, "status": model.ProjectStatusActive, "auth_type": model.AuthTypeNone, "credential": ""}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pipeline.Update(f.ctx, f.pipelineID, map[string]any{"repo_url": source.repository, "default_branch": "main", "pipeline_file_path": "pipeline.json"}); err != nil {
		t.Fatal(err)
	}
	engine := &realStackPollingEngine{IPipelineEngine: f.engine}
	service := controlservice.NewPollingService(repositories, source, func() controlservice.IPipelineEngine { return engine }, nil)
	source.observe(t, initial, "main", time.Now().Add(-24*time.Hour))
	return &pollingSafetyFixture{f: f, repositories: repositories, source: source, service: service, engine: engine, actor: actor, initial: initial, observed: observed}
}

func (s *pollingSafetyFixture) stream(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	row, err := s.repositories.SCMPolling.GetByProject(s.f.ctx, s.f.projectID)
	if err != nil || row == nil {
		t.Fatalf("read polling safety state: %v", err)
	}
	return row
}

func (s *pollingSafetyFixture) baseline(t *testing.T) *controlservice.PollingStatus {
	t.Helper()
	baseline, err := s.service.Baseline(s.f.ctx, s.actor, s.f.projectID, 0, false)
	if err != nil || baseline == nil || baseline.State != polling.StateBaselined {
		t.Fatalf("prepare explicit safety baseline: %v", err)
	}
	return baseline
}

func (s *pollingSafetyFixture) enable(t *testing.T, revision uint64, baselineID string) {
	t.Helper()
	if _, err := s.service.Enable(s.f.ctx, s.actor, s.f.projectID, revision, baselineID, []string{s.f.pipelineID}); err != nil {
		t.Fatal(err)
	}
}

func (s *pollingSafetyFixture) assertNoExecution(t *testing.T) {
	t.Helper()
	if realStackAutomaticRunCount(t, s.f) != 0 || s.engine.submissions != 0 {
		t.Fatal("non-executing polling case created or submitted a run")
	}
	for _, entity := range []any{&model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}} {
		var count int64
		if err := s.f.db.WithContext(s.f.ctx).Model(entity).Where("pipeline_id = ?", s.f.pipelineID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("unexpected polling execution evidence in %T: count=%d error=%v", entity, count, err)
		}
	}
	s.f.mu.Lock()
	tasks := len(s.f.tasks)
	s.f.mu.Unlock()
	if tasks != 0 {
		t.Fatal("non-executing polling case published a Kafka task")
	}
}

// TestControllerWorkerPollingDefinitionFailureRetainsObservations 验证定义读取失败前的持久观察不会被新快照覆盖或隐式丢弃
func TestControllerWorkerPollingDefinitionFailureRetainsObservations(t *testing.T) {
	s := newPollingSafetyFixture(t, nil)
	baseline := s.baseline(t)
	s.enable(t, baseline.Revision, baseline.BaselineID)
	before := s.stream(t)
	s.source.observe(t, s.observed, "main", time.Now())
	s.source.failDefinition = true
	if err := s.service.PollOnce(s.f.ctx); err == nil {
		t.Fatal("definition failure was silently acknowledged")
	}
	paused := s.stream(t)
	pending, err := polling.ParseObservedBatch(paused.PendingObservationJSON)
	if err != nil || paused.State != polling.StatePaused || paused.PauseReason != "definition_unavailable" || paused.CursorJSON != before.CursorJSON || paused.PendingBatchJSON != "" || len(pending.Observations) != 1 || pending.Observations[0].SourceSHA != s.observed || pending.Observations[0].MRID != 901 || pending.Observations[0].MRIID != 17 {
		t.Fatalf("definition failure lost frozen normalized observations: %v", err)
	}
	s.assertNoExecution(t)
	s.source.failDefinition = false
	s.source.observe(t, s.initial, "release/moved", time.Now())
	if err := s.service.PollOnce(s.f.ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paused, s.stream(t)) {
		t.Fatal("later provider content overwrote a paused observation")
	}
	if _, err := s.service.Baseline(s.f.ctx, s.actor, s.f.projectID, paused.Revision, false); !errors.Is(err, polling.ErrUnresolved) {
		t.Fatalf("rebaseline omitted explicit unadmitted-observation discard confirmation: %v", err)
	}
	if !reflect.DeepEqual(paused, s.stream(t)) {
		t.Fatal("rejected rebaseline changed frozen observations")
	}
	fresh, err := s.service.Baseline(s.f.ctx, s.actor, s.f.projectID, paused.Revision, true)
	if err != nil || fresh == nil || fresh.State != polling.StateBaselined || fresh.BaselineID == baseline.BaselineID {
		t.Fatalf("explicit discard could not establish a non-executing new baseline: %v", err)
	}
	after := s.stream(t)
	if after.PendingObservationJSON != "" || after.PendingBatchJSON != "" || after.EnabledBy != "" || after.SelectionJSON != "" {
		t.Fatal("explicit rebaseline retained activation or pending dispatch")
	}
	s.assertNoExecution(t)
}

// TestControllerWorkerPollingNonmatchingRestrictedDefinitions 不相关事件不触发部署或重试审批执行限制
func TestControllerWorkerPollingNonmatchingRestrictedDefinitions(t *testing.T) {
	for _, kind := range []string{"deployment", "retry_approval"} {
		t.Run(kind, func(t *testing.T) {
			job := &spec.Job{Name: "restricted", Steps: []*spec.Step{controllerWorkerShell("once", "printf x >> executions")}}
			if kind == "retry_approval" {
				job.Approval = &spec.Approval{Required: true, Type: "manual", Plugin: "builtin:retry-approval"}
			} else {
				job.Steps = []*spec.Step{{Name: "release", Uses: "builtin:kubernetes", Action: "deploy", Timeout: "30s", Args: spec.MapAsStruct(map[string]any{"environmentId": "synthetic-environment", "artifact": map[string]any{"job": "build", "step": "publish", "name": "image.json"}})}}
			}
			definition := &spec.Pipeline{Namespace: "controller-agent", Triggers: []*spec.Trigger{{Type: trigger.TriggerTypeEvent, Options: spec.MapAsStruct(map[string]any{trigger.OptionEventType: string(scm.EventTypeMergeRequest), trigger.OptionBranch: "main"})}}, Jobs: []*spec.Job{job}}
			s := newPollingSafetyFixture(t, definition)
			baseline := s.baseline(t)
			s.enable(t, baseline.Revision, baseline.BaselineID)
			s.source.observe(t, s.observed, "release/unselected", time.Now())
			if err := s.service.PollOnce(s.f.ctx); err != nil {
				t.Fatalf("nonmatching restricted definition paused polling: %v", err)
			}
			row := s.stream(t)
			if row.State != polling.StateEnabled || row.PendingBatchJSON != "" || row.PendingObservationJSON != "" || row.LastCompletedAt == nil || row.LastObservationCount != 1 || row.LastTargetCount != 0 {
				t.Fatal("nonmatching restricted definition did not acknowledge complete empty dispatch")
			}
			s.assertNoExecution(t)
			// 相同定义若确实命中，仍须失败关闭，不能借本次过滤跳过执行限制
			s.source.observe(t, s.observed, "main", time.Now())
			if err := s.service.PollOnce(s.f.ctx); err == nil {
				t.Fatal("matching restricted definition escaped polling execution guard")
			}
			if row := s.stream(t); row.State != polling.StatePaused || row.PauseReason != "definition_rejected" || row.PendingObservationJSON == "" {
				t.Fatal("matching restricted definition lost its safe rejection and pending observations")
			}
			s.assertNoExecution(t)
		})
	}
}

// TestControllerWorkerPollingHistoricalSubmillisecondCutoff 验证低精度数据库时间不得扩大基线后的执行区间
func TestControllerWorkerPollingHistoricalSubmillisecondCutoff(t *testing.T) {
	s := newPollingSafetyFixture(t, nil)
	baseline := s.baseline(t)
	row := s.stream(t)
	var scope polling.Scope
	if err := sonic.UnmarshalString(row.ScopeJSON, &scope); err != nil {
		t.Fatal(err)
	}
	cursor, err := polling.ParseCursor(row.CursorJSON)
	if err != nil {
		t.Fatal(err)
	}
	// 确定性高精度基线走真实仓储；只把展示用时间列模拟为 MySQL 毫秒精度
	cutoff := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(500123456 * time.Nanosecond)
	row, err = s.repositories.SCMPolling.Baseline(s.f.ctx, repo.BaselineInput{ProjectID: s.f.projectID, ActorID: s.actor, ExpectedRevision: baseline.Revision, Scope: scope, Cursor: *cursor, StartedAt: cutoff, CompletedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if row.BaselineCutoff != cutoff.Format(time.RFC3339Nano) {
		t.Fatal("repository lost the exact baseline cutoff")
	}
	if err := s.f.db.WithContext(s.f.ctx).Model(&model.SCMPollingStream{}).Where("project_id = ?", s.f.projectID).Update("baseline_started_at", cutoff.Truncate(time.Millisecond)).Error; err != nil {
		t.Fatal(err)
	}
	s.enable(t, row.Revision, row.BaselineID)
	delayed := cutoff.Add(-time.Nanosecond)
	if !delayed.After(cutoff.Truncate(time.Millisecond)) {
		t.Fatal("fixture failed to cover the truncated-time gap")
	}
	s.source.observe(t, s.observed, "main", delayed)
	if err := s.service.PollOnce(s.f.ctx); err != nil {
		t.Fatal(err)
	}
	row = s.stream(t)
	if row.State != polling.StateEnabled || row.LastObservationCount != 1 || row.LastTargetCount != 0 || row.LastCompletedAt == nil || row.PendingObservationJSON != "" || row.PendingBatchJSON != "" {
		t.Fatal("delayed historical observation did not complete without dispatch")
	}
	s.source.mu.Lock()
	definitions := s.source.definitions
	s.source.mu.Unlock()
	if definitions != 0 {
		t.Fatal("historical observation fetched an executable definition")
	}
	s.assertNoExecution(t)
}

// TestControllerWorkerPollingUnresolvedDuplicatePreservesCreator 验证第二轮询者记录恢复提示但不撤销尚在入场的创建者
func TestControllerWorkerPollingUnresolvedDuplicatePreservesCreator(t *testing.T) {
	s := newPollingSafetyFixture(t, nil)
	baseline := s.baseline(t)
	s.enable(t, baseline.Revision, baseline.BaselineID)
	secondEngine := &realStackPollingEngine{IPipelineEngine: s.f.engine, beforeSubmit: func(*model.PipelineRun) error { return fmt.Errorf("duplicate poller must never submit") }}
	second := controlservice.NewPollingService(s.repositories, s.source, func() controlservice.IPipelineEngine { return secondEngine }, nil)
	var runID string
	s.engine.beforeSubmit = func(run *model.PipelineRun) error {
		runID = run.RunID
		before := s.stream(t)
		if err := second.PollOnce(s.f.ctx); err == nil {
			return fmt.Errorf("known pending duplicate lacked an unresolved result")
		}
		after := s.stream(t)
		if after.State != polling.StateEnabled || after.ActivationGeneration != before.ActivationGeneration || after.CursorJSON != before.CursorJSON || after.PendingBatchJSON != before.PendingBatchJSON || after.LastPollError != "recovery_required" || after.PendingBatchJSON == "" || secondEngine.submissions != 0 {
			return fmt.Errorf("duplicate invalidated creator, changed frozen batch, or omitted recovery blocker")
		}
		return nil
	}
	s.source.observe(t, s.observed, "main", time.Now())
	if err := s.service.PollOnce(s.f.ctx); err != nil {
		t.Fatalf("active creator could not complete after competing unresolved poll: %v", err)
	}
	if runID == "" || s.engine.submissions != 1 || secondEngine.submissions != 0 || realStackAutomaticRunCount(t, s.f) != 1 {
		t.Fatal("competing pollers created or submitted more than one run")
	}
	s.f.waitRun(runID, model.PipelineStatusSuccess)
	evidence := realStackReadAutomaticEvidence(t, s.f, runID)
	if len(evidence.Jobs) != 1 || len(evidence.Steps) != 1 {
		t.Fatal("creator did not execute its single configured job")
	}
	realStackAssertOnce(t, s.f, runID, evidence.Jobs[0].JobRunID)
	row := s.stream(t)
	if row.State != polling.StateEnabled || row.LastPollError != "" || row.PendingBatchJSON != "" || row.PendingObservationJSON != "" || row.LastTargetCount != 1 || row.LastCompletedAt == nil {
		t.Fatal("successful creator did not clear unresolved hint and acknowledge its batch")
	}
}
