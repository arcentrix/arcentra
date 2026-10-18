// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/admission"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/trigger"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/log"
)

// This fixture uses SQLite, a local Git repository, and synthetic signed events.
// It does not register webhooks, start a scheduler, or contact an external SCM.
func automaticTriggerFixture(t *testing.T, content, source string, decorate ...func(repo.IPipelineRepository) repo.IPipelineRepository) (*Process, *repo.Repositories, func()) {
	t.Helper()
	// Production SCM helpers inherit the process environment as well, so isolate
	// every Git operation in this fixture from inherited worktree/config hooks.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	engine, repositories := newTestProcess(t)
	db := repositories.Pipeline.(*repo.PipelineRepo).Database()
	if err := db.AutoMigrate(&model.Project{}); err != nil {
		t.Fatal(err)
	}
	projects := repo.NewProjectRepo(processTestDB{db})
	for _, wrap := range decorate {
		repositories.Pipeline = wrap(repositories.Pipeline)
	}
	directory := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", directory}, args...)...)
		cmd.Env = os.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("local git fixture: %v %s", err, output)
		}
	}
	runGit("-c", "init.defaultBranch=main", "init")
	if err := os.WriteFile(filepath.Join(directory, "pipeline.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "pipeline.json")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	project := &model.Project{ProjectID: "project", RepoType: model.RepoTypeGitHub, RepoURL: directory, DefaultBranch: "main", IsEnabled: 1, WebhookSecret: "synthetic-fixture-secret", Status: model.ProjectStatusActive}
	if err := projects.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	pipeline := &model.Pipeline{PipelineID: "pipeline", ProjectID: project.ProjectID, Name: "automatic", RepoURL: directory, DefaultBranch: "main", PipelineFilePath: "pipeline.json", IsEnabled: 1}
	if err := repositories.Pipeline.Create(context.Background(), pipeline); err != nil {
		t.Fatal(err)
	}
	if source == "cron" {
		scheduler := cron.New()
		manager := trigger.NewCronTriggerManager(scheduler, engine, repositories.Pipeline, projects, service.LoadPipelineDefinition)
		if err := manager.Register(pipeline.PipelineID, "* * * * *"); err != nil {
			t.Fatal(err)
		}
		entries := scheduler.Entries()
		if len(entries) != 1 {
			t.Fatalf("cron entries: %d", len(entries))
		}
		return engine, repositories, entries[0].Job.Run
	}
	svc := service.NewScmService(projects, repositories.Pipeline)
	svc.SetEngine(engine)
	body := []byte(`{"ref":"refs/heads/main","head_commit":{"id":"` + strings.Repeat("a", 40) + `","timestamp":"2026-01-01T00:00:00Z"}}`)
	mac := hmac.New(sha256.New, []byte(project.WebhookSecret))
	_, _ = mac.Write(body)
	headers := map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
	return engine, repositories, func() {
		t.Helper()
		events, err := svc.HandleWebhook(context.Background(), project.ProjectID, headers, body)
		if err != nil || len(events) != 1 {
			t.Fatalf("synthetic webhook: events=%d err=%v", len(events), err)
		}
	}
}

func TestAutomaticTriggerSchemaRejectionDoesNotLeavePendingRun(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		t.Run(source, func(t *testing.T) {
			_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event","options":{"event":"push"}}],"jobs":[]}`, source)
			fire()
			fire()
			runs, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 1 {
				t.Fatalf("rejected delivery must retain exactly one deduplication record: count=%d err=%v", count, err)
			}
			if runs[0].Status != model.PipelineStatusFailed || runs[0].EndTime == nil || runs[0].StartTime != nil {
				t.Fatalf("rejected automatic run was not terminalized before execution: status=%d start=%v end=%v", runs[0].Status, runs[0].StartTime, runs[0].EndTime)
			}
			jobs, err := repositories.JobRun.ListByPipelineRunID(context.Background(), runs[0].RunID)
			if err != nil || len(jobs) != 0 {
				t.Fatalf("rejected automatic run created execution records: jobs=%d err=%v", len(jobs), err)
			}
		})
	}
}

func TestAutomaticTriggerRetryApprovalRefusedBeforeRunCreation(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		t.Run(source, func(t *testing.T) {
			_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[{"name":"build","approval":{"required":true,"type":"manual","plugin":"builtin:retry-approval"},"steps":[{"name":"build","uses":"shell","args":{"script":"true"}}]}]}`, source)
			fire()
			_, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 0 {
				t.Fatalf("automatic retry approval created a run: count=%d err=%v", count, err)
			}
		})
	}
}

func pendingAutomaticRun(t *testing.T, repository repo.IPipelineRepository, suffix string) *model.PipelineRun {
	t.Helper()
	if _, err := repository.Get(context.Background(), "pipeline"); err != nil {
		if err := repository.Create(context.Background(), &model.Pipeline{PipelineID: "pipeline", ProjectID: "admission-project", IsEnabled: 1}); err != nil {
			t.Fatal(err)
		}
	}
	run := &model.PipelineRun{RunID: "automatic-" + suffix, PipelineID: "pipeline", RequestID: "cron:pipeline:" + suffix, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON), TriggeredBy: "cron", Status: model.PipelineStatusPending}
	if err := repository.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func validAutomaticDefinition() *spec.Pipeline {
	return &spec.Pipeline{Namespace: "test", Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{shellStep("build", "true")}}}}
}

func TestAutomaticAdmissionAcrossProcessesCannotFailAcceptedRun(t *testing.T) {
	engine, repositories := newTestProcess(t)
	engine.sem = make(chan struct{}, 1)
	engine.sem <- struct{}{}
	run := pendingAutomaticRun(t, repositories.Pipeline, "accepted")
	if err := engine.Submit(run, validAutomaticDefinition()); err != nil {
		t.Fatal(err)
	}
	other := NewProcess(repositories, nil, nil, nil, engine.logger, nil, nil)
	t.Cleanup(other.Stop)
	if err := other.Submit(run, &spec.Pipeline{}); !admission.IsRejected(err) {
		t.Fatalf("expected current-call rejection: %v", err)
	}
	stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusRunning || stored.EndTime != nil {
		t.Fatalf("rejected second process changed admitted run: %+v %v", stored, err)
	}
	if err := other.Submit(run, validAutomaticDefinition()); err == nil || admission.IsRejected(err) {
		t.Fatalf("durable duplicate admitted or classified as safe rejection: %v", err)
	}
	if err := engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, repositories, run.RunID, model.PipelineStatusCancelled)
}

func TestAutomaticAdmissionInvalidDuplicatePreservesQueuedPendingCoordinator(t *testing.T) {
	engine, repositories := newTestProcess(t)
	run := pendingAutomaticRun(t, repositories.Pipeline, "queued-legacy")
	// A coordinator accepted before the durable-claim change can still be
	// queued with PENDING state. Invalid duplicate input must not alter it.
	engine.runs.Store(run.RunID, NewCoordinator(run, validAutomaticDefinition(), engine))
	t.Cleanup(func() { engine.runs.Delete(run.RunID) })
	if err := engine.Submit(run, &spec.Pipeline{}); err == nil || admission.IsRejected(err) {
		t.Fatalf("duplicate was treated as a new rejection: %v", err)
	}
	stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusPending || stored.EndTime != nil {
		t.Fatalf("queued duplicate overwritten: %+v %v", stored, err)
	}
}

func TestAutomaticAdmissionPreflightFailuresAndManualIsolation(t *testing.T) {
	for _, failure := range []string{"schema", "nil-spec", "log-init", "shutdown"} {
		t.Run(failure, func(t *testing.T) {
			engine, repositories := newTestProcess(t)
			run := pendingAutomaticRun(t, repositories.Pipeline, failure)
			definition := validAutomaticDefinition()
			switch failure {
			case "schema":
				definition.Namespace = ""
			case "nil-spec":
				definition = nil
			case "log-init":
				engine.logInitErr = errors.New("synthetic log initialization failure")
			case "shutdown":
				engine.Stop()
			}
			if err := engine.Submit(run, definition); !admission.IsRejected(err) {
				t.Fatalf("not classified as pre-admission rejection: %v", err)
			}
			stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
			if err != nil || stored.Status != model.PipelineStatusFailed || stored.EndTime == nil || stored.StartTime != nil {
				t.Fatalf("rejected run pending: %+v %v", stored, err)
			}
		})
	}
	engine, repositories := newTestProcess(t)
	manual := &model.PipelineRun{RunID: "manual", PipelineID: "pipeline", Status: model.PipelineStatusPending, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor"}
	if err := repositories.Pipeline.CreateRun(context.Background(), manual); err != nil {
		t.Fatal(err)
	}
	if err := engine.Submit(manual, &spec.Pipeline{}); !admission.IsRejected(err) {
		t.Fatal(err)
	}
	stored, _ := repositories.Pipeline.GetRun(context.Background(), manual.RunID)
	if stored.Status != model.PipelineStatusPending || stored.EndTime != nil {
		t.Fatal("Process changed manual rejection persistence")
	}
}

type automaticAdmissionFailureRepo struct {
	repo.IPipelineRepository
	failure      error
	claimChanged bool
	rejectCalls  int
}

func (r *automaticAdmissionFailureRepo) RejectPendingAutomaticRun(context.Context, *model.PipelineRun) (bool, error) {
	r.rejectCalls++
	return false, r.failure
}
func (r *automaticAdmissionFailureRepo) StartAutomaticRunIfPending(context.Context, *model.PipelineRun) (bool, error) {
	return r.claimChanged, r.failure
}

func TestAutomaticAdmissionPersistenceErrorsRemainVisible(t *testing.T) {
	engine, repositories := newTestProcess(t)
	run := pendingAutomaticRun(t, repositories.Pipeline, "database-error")
	failure := errors.New("synthetic database unavailable")
	original := repositories.Pipeline
	repositories.Pipeline = &automaticAdmissionFailureRepo{IPipelineRepository: original, failure: failure}
	if err := engine.Submit(run, &spec.Pipeline{}); !admission.IsRejected(err) || !errors.Is(err, failure) {
		t.Fatalf("rejection persistence failure hidden: %v", err)
	}
	if err := engine.Submit(run, validAutomaticDefinition()); err == nil || admission.IsRejected(err) || !errors.Is(err, failure) {
		t.Fatalf("ambiguous positive claim treated as rejection: %v", err)
	}
	stored, _ := original.GetRun(context.Background(), run.RunID)
	if stored.Status != model.PipelineStatusPending || stored.EndTime != nil {
		t.Fatal("unknown database outcome mutated status")
	}
	if _, exists := engine.runs.Load(run.RunID); exists {
		t.Fatal("unknown claim created coordinator")
	}
}

func TestAutomaticTriggerFixtureIgnoresInheritedGitWorktree(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(directory, "wrong.git"))
	t.Setenv("GIT_WORK_TREE", directory)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "alias.init")
	t.Setenv("GIT_CONFIG_VALUE_0", "!false")
	_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[]}`, "webhook")
	fire()
	_, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
	if err != nil || count != 1 {
		t.Fatalf("inherited Git environment interfered: %d %v", count, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("fixture touched inherited worktree: %d %v", len(entries), err)
	}
}

func TestAutomaticTriggerRefusesDisabledScope(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		for _, disabled := range []string{"pipeline-disabled", "pipeline-deleted", "project-disabled", "project-archived", "project-inactive", "project-not-enabled"} {
			t.Run(source+"/"+disabled, func(t *testing.T) {
				_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[]}`, source)
				db := repositories.Pipeline.(*repo.PipelineRepo).Database()
				var err error
				switch disabled {
				case "pipeline-disabled":
					err = repositories.Pipeline.Update(context.Background(), "pipeline", map[string]any{"is_enabled": 0})
				case "pipeline-deleted":
					err = repositories.Pipeline.Update(context.Background(), "pipeline", map[string]any{"is_deleted": 1})
				case "project-disabled":
					err = repo.NewProjectRepo(processTestDB{db}).Delete(context.Background(), "project")
				case "project-archived":
					err = repo.NewProjectRepo(processTestDB{db}).Update(context.Background(), "project", map[string]any{"status": model.ProjectStatusArchived})
				case "project-inactive":
					err = repo.NewProjectRepo(processTestDB{db}).Update(context.Background(), "project", map[string]any{"status": model.ProjectStatusInactive})
				case "project-not-enabled":
					err = repo.NewProjectRepo(processTestDB{db}).Update(context.Background(), "project", map[string]any{"is_enabled": 0})
				}
				if err != nil {
					t.Fatal(err)
				}
				fire()
				_, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
				if err != nil || count != 0 {
					t.Fatalf("disabled scope triggered: %d %v", count, err)
				}
			})
		}
	}
}

type automaticLookupFailureRepo struct {
	repo.IPipelineRepository
	lookups int
}

func (r *automaticLookupFailureRepo) GetRunByRequestID(context.Context, string, string) (*model.PipelineRun, error) {
	r.lookups++
	return nil, errors.New("synthetic request lookup unavailable")
}
func TestAutomaticTriggerRequestLookupFailureDoesNotCreateRun(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		t.Run(source, func(t *testing.T) {
			var wrapper *automaticLookupFailureRepo
			_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[]}`, source, func(original repo.IPipelineRepository) repo.IPipelineRepository {
				wrapper = &automaticLookupFailureRepo{IPipelineRepository: original}
				return wrapper
			})
			fire()
			_, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 0 || wrapper.lookups != 1 {
				t.Fatalf("lookup failure allowed creation: count=%d lookups=%d err=%v", count, wrapper.lookups, err)
			}
		})
	}
}

func TestAutomaticAdmissionMalformedIdentityCannotBypassDurableClaim(t *testing.T) {
	for _, mutation := range []string{"actor", "request", "approval-context", "deployment-actor"} {
		t.Run(mutation, func(t *testing.T) {
			engine, repositories := newTestProcess(t)
			run := pendingAutomaticRun(t, repositories.Pipeline, mutation)
			candidate := *run
			switch mutation {
			case "actor":
				candidate.TriggeredBy = "actor"
			case "request":
				candidate.RequestID = "unrelated"
			case "approval-context":
				candidate.RetryApprovalContext = "untrusted"
			case "deployment-actor":
				candidate.DeploymentActorID = "actor"
			}
			if err := engine.Submit(&candidate, validAutomaticDefinition()); err == nil {
				t.Fatal("malformed automatic identity bypassed durable admission")
			}
			stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
			if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
				t.Fatalf("malformed identity mutated run: %+v %v", stored, err)
			}
			if _, exists := engine.runs.Load(run.RunID); exists {
				t.Fatal("malformed identity created coordinator")
			}
		})
	}
}

func TestAutomaticTriggerDuplicateAcceptedDeliveryRunsOnce(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		t.Run(source, func(t *testing.T) {
			_, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[{"name":"build","steps":[{"name":"build","uses":"shell","args":{"script":"true"}}]}]}`, source)
			fire()
			fire()
			runs, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 1 {
				t.Fatalf("duplicate created another run: %d %v", count, err)
			}
			waitRun(t, repositories, runs[0].RunID, model.PipelineStatusSuccess)
			fire()
			_, count, err = repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 1 {
				t.Fatalf("terminal duplicate replayed: %d %v", count, err)
			}
			jobs, err := repositories.JobRun.ListByPipelineRunID(context.Background(), runs[0].RunID)
			if err != nil || len(jobs) != 1 || jobs[0].Status != model.JobRunStatusSuccess {
				t.Fatalf("expected one actual successful job: %+v %v", jobs, err)
			}
		})
	}
}

type existingAutomaticRunRepo struct {
	repo.IPipelineRepository
	stored *model.PipelineRun
}

func (r *existingAutomaticRunRepo) GetRunByRequestID(context.Context, string, string) (*model.PipelineRun, error) {
	return r.stored, nil
}
func TestAutomaticTriggerDoesNotResumeUnknownPendingDelivery(t *testing.T) {
	for _, source := range []string{"cron", "webhook"} {
		t.Run(source, func(t *testing.T) {
			engine, repositories, fire := automaticTriggerFixture(t, `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[{"name":"build","steps":[{"name":"build","uses":"shell","args":{"script":"true"}}]}]}`, source, func(original repo.IPipelineRepository) repo.IPipelineRepository {
				return &existingAutomaticRunRepo{IPipelineRepository: original, stored: &model.PipelineRun{RunID: "old-unknown", Status: model.PipelineStatusPending}}
			})
			fire()
			_, count, err := repositories.Pipeline.ListRuns(context.Background(), &repo.PipelineRunQuery{PipelineID: "pipeline"})
			if err != nil || count != 0 {
				t.Fatalf("unknown pending caused new run: %d %v", count, err)
			}
			if _, exists := engine.runs.Load("old-unknown"); exists {
				t.Fatal("unknown pending automatically resumed")
			}
		})
	}
}

func TestAutomaticAdmissionCannotRelabelPersistedRun(t *testing.T) {
	for _, status := range []int{model.PipelineStatusPending, model.PipelineStatusRunning, model.PipelineStatusFailed, model.PipelineStatusSuccess, model.PipelineStatusCancelled} {
		for _, claimedType := range []pipelinev1.TriggerType{pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL, pipelinev1.TriggerType(0)} {
			t.Run(fmt.Sprintf("status-%d/type-%d", status, claimedType), func(t *testing.T) {
				engine, repositories := newTestProcess(t)
				run := pendingAutomaticRun(t, repositories.Pipeline, "relabel")
				if err := repositories.Pipeline.UpdateRun(context.Background(), run.RunID, map[string]any{"status": status}); err != nil {
					t.Fatal(err)
				}
				candidate := *run
				candidate.TriggerType = int(claimedType)
				candidate.TriggeredBy = "actor"
				candidate.RequestID = "manual"
				if err := engine.Submit(&candidate, validAutomaticDefinition()); err == nil || admission.IsRejected(err) {
					t.Fatalf("relabel accepted or treated as safe rejection: %v", err)
				}
				stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
				if err != nil || stored.Status != status || stored.StartTime != nil || stored.EndTime != nil {
					t.Fatalf("relabel changed run: %+v %v", stored, err)
				}
				if _, exists := engine.runs.Load(run.RunID); exists {
					t.Fatal("relabel created coordinator")
				}
			})
		}
	}
}

type admissionIdentityFailureRepo struct {
	repo.IPipelineRepository
	failure error
}

func (r *admissionIdentityFailureRepo) GetRun(context.Context, string) (*model.PipelineRun, error) {
	return nil, r.failure
}
func TestManualAdmissionIdentityReadFailsClosed(t *testing.T) {
	for _, failure := range []error{nil, errors.New("synthetic identity unavailable")} {
		engine, repositories := newTestProcess(t)
		original := repositories.Pipeline
		run := &model.PipelineRun{RunID: "manual", PipelineID: "pipeline", Status: model.PipelineStatusPending, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor"}
		if err := original.CreateRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		repositories.Pipeline = &admissionIdentityFailureRepo{IPipelineRepository: original, failure: failure}
		if err := engine.Submit(run, validAutomaticDefinition()); err == nil || admission.IsRejected(err) || failure != nil && !errors.Is(err, failure) {
			t.Fatalf("identity uncertainty admitted: %v", err)
		}
		stored, err := original.GetRun(context.Background(), run.RunID)
		if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
			t.Fatalf("uncertain identity changed run: %+v %v", stored, err)
		}
		if _, exists := engine.runs.Load(run.RunID); exists {
			t.Fatal("uncertain identity created coordinator")
		}
	}
}

func TestAutomaticAdmissionApparentClaimWithErrorRemainsUnknown(t *testing.T) {
	engine, repositories := newTestProcess(t)
	run := pendingAutomaticRun(t, repositories.Pipeline, "commit-uncertain")
	original := repositories.Pipeline
	failure := errors.New("synthetic commit result unknown")
	wrapper := &automaticAdmissionFailureRepo{IPipelineRepository: original, failure: failure, claimChanged: true}
	repositories.Pipeline = wrapper
	if err := engine.Submit(run, validAutomaticDefinition()); err == nil || admission.IsRejected(err) || !errors.Is(err, failure) {
		t.Fatalf("uncertain commit treated as accepted/rejected: %v", err)
	}
	if _, exists := engine.runs.Load(run.RunID); exists || wrapper.rejectCalls != 0 {
		t.Fatalf("uncertain commit scheduled or attempted cleanup: admitted=%v cleanup=%d", exists, wrapper.rejectCalls)
	}
	stored, err := original.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
		t.Fatalf("adapter unknown state changed: %+v %v", stored, err)
	}
}

func TestAutomaticTriggerFailureLogsDoNotEchoSourceOrSubmissionErrors(t *testing.T) {
	const canary = "synthetic-automatic-trigger-sensitive-canary"
	for _, source := range []string{"cron", "webhook"} {
		for _, failure := range []string{"load", "parse", "rejected", "unknown"} {
			t.Run(source+"/"+failure, func(t *testing.T) {
				directory := t.TempDir()
				conf := log.SetDefaults()
				conf.Output = "file"
				conf.Path = directory
				conf.Filename = "automatic.log"
				conf.Level = "DEBUG"
				if err := log.Init(conf); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = log.Init(log.SetDefaults()) })
				content := `{"namespace":"test","triggers":[{"type":"event"}],"jobs":[{"name":"build","steps":[{"name":"build","uses":"shell","args":{"script":"true"}}]}]}`
				if failure == "parse" {
					content = `{"namespace":"` + canary + `","jobs":[`
				}
				var decorators []func(repo.IPipelineRepository) repo.IPipelineRepository
				if failure == "unknown" {
					decorators = append(decorators, func(original repo.IPipelineRepository) repo.IPipelineRepository {
						return &automaticAdmissionFailureRepo{IPipelineRepository: original, failure: errors.New(canary)}
					})
				}
				engine, repositories, fire := automaticTriggerFixture(t, content, source, decorators...)
				if failure == "load" {
					if err := repositories.Pipeline.Update(context.Background(), "pipeline", map[string]any{"pipeline_file_path": canary}); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "rejected" {
					engine.logInitErr = errors.New(canary)
				}
				fire()
				_ = log.Sync()
				data, err := os.ReadFile(filepath.Join(directory, "automatic.log"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), canary) {
					t.Fatal("automatic callback logged source or raw submission error")
				}
				want := "load definition failed"
				if failure == "parse" {
					want = "parse spec failed"
				}
				if failure == "rejected" {
					want = "rejected-before-claim"
				}
				if failure == "unknown" {
					want = "unknown"
				}
				if !strings.Contains(string(data), want) {
					t.Fatalf("expected safe failure class %q absent", want)
				}
			})
		}
	}
}

func TestAutomaticAdmissionCannotRelabelManualRun(t *testing.T) {
	engine, repositories := newTestProcess(t)
	run := &model.PipelineRun{RunID: "manual-original", PipelineID: "pipeline", RequestID: "manual-original", Status: model.PipelineStatusPending, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor"}
	if err := repositories.Pipeline.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	candidate := *run
	candidate.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON)
	candidate.TriggeredBy = "cron"
	candidate.RequestID = "cron:pipeline:relabelled-manual"
	if err := engine.Submit(&candidate, validAutomaticDefinition()); err == nil || admission.IsRejected(err) {
		t.Fatalf("manual row relabelled as automatic was admitted: %v", err)
	}
	stored, err := repositories.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
		t.Fatalf("relabel changed manual state: %+v %v", stored, err)
	}
	if _, exists := engine.runs.Load(run.RunID); exists {
		t.Fatal("relabel created coordinator")
	}
}
