package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type pipelineServiceTestDB struct{ db *gorm.DB }

func (d pipelineServiceTestDB) Database() *gorm.DB { return d.db }

func runServiceFixture(t *testing.T) *PipelineServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runs.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}); err != nil {
		t.Fatal(err)
	}
	adapter := pipelineServiceTestDB{db}
	svc := &PipelineServiceImpl{pipelineRepo: repo.NewPipelineRepo(adapter), jobRunRepo: repo.NewJobRunRepo(adapter), stepRunRepo: repo.NewStepRunRepo(adapter), logAggregator: NewLogAggregator(repo.NewLogRepository(db))}
	t.Cleanup(func() { _ = svc.logAggregator.Close(context.Background()) })
	for _, suffix := range []string{"a", "b"} {
		if err := svc.pipelineRepo.Create(context.Background(), &model.Pipeline{PipelineID: "pipeline-" + suffix, Name: suffix, IsEnabled: 1}); err != nil {
			t.Fatal(err)
		}
		if err := svc.pipelineRepo.CreateRun(context.Background(), &model.PipelineRun{PipelineID: "pipeline-" + suffix, RunID: "run-" + suffix, Status: model.PipelineStatusRunning}); err != nil {
			t.Fatal(err)
		}
		if err := svc.jobRunRepo.Create(context.Background(), &model.JobRun{PipelineID: "pipeline-" + suffix, PipelineRunID: "run-" + suffix, JobRunID: "job-" + suffix, JobName: "build", Status: 3, Env: `{"TOKEN":"hidden-value"}`}); err != nil {
			t.Fatal(err)
		}
		if err := svc.stepRunRepo.Create(context.Background(), &model.StepRun{PipelineID: "pipeline-" + suffix, PipelineRunID: "run-" + suffix, JobRunID: "job-" + suffix, StepRunID: "step-" + suffix, Name: "shell", Status: 3, Env: `{"TOKEN":"hidden-value"}`, Secrets: `{"PASSWORD":"hidden-value"}`, Args: `{"token":"hidden-value"}`}); err != nil {
			t.Fatal(err)
		}
		if err := svc.logAggregator.PushLog(&LogEntry{StepRunID: "step-" + suffix, Timestamp: 1, LineNumber: 1, Content: "output-" + suffix}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.logAggregator.CloseStream("step-" + suffix) })
	}
	return svc
}

func TestPipelineRunEndpointsEnforceRunAndStepScope(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	if _, err := svc.GetScopedPipelineRun(ctx, "pipeline-a", "run-b"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("cross-pipeline run accepted: %v", err)
	}
	if _, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-b"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("cross-pipeline jobs accepted: %v", err)
	}
	if _, _, err := svc.GetPipelineRunLogs(ctx, "pipeline-a", "run-a", "step-b", 0, 100); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("foreign step accepted: %v", err)
	}
	logs, truncated, err := svc.GetPipelineRunLogs(ctx, "pipeline-a", "run-a", "", 0, 100)
	if err != nil || truncated || len(logs) != 1 || logs[0].Message != "output-a" || logs[0].JobID != "job-a" {
		t.Fatalf("run log scope mismatch: %+v %v %v", logs, truncated, err)
	}
	jobs, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := sonic.MarshalString(jobs)
	for _, forbidden := range []string{"hidden-value", "secrets", `"env"`, `"args"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("job DTO exposes %q: %s", forbidden, raw)
		}
	}
	if len(jobs) != 1 || len(jobs[0].Steps) != 1 {
		t.Fatalf("missing nested steps: %+v", jobs)
	}
}

func TestStopPipelineRejectsCrossPipelineAndTerminalRuns(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	response, err := svc.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "pipeline-a", RunId: "run-b"})
	if err != nil || response.Success || response.Error.Code != 404 {
		t.Fatalf("cross-pipeline stop: %+v %v", response, err)
	}
	_ = svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"status": model.PipelineStatusSuccess})
	response, err = svc.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("finished run mutated: %+v %v", response, err)
	}
	run, _ := svc.pipelineRepo.GetRun(ctx, "run-a")
	if run.Status != model.PipelineStatusSuccess {
		t.Fatal("terminal run was changed")
	}
}

func TestTriggerRejectsDisabledOrUnavailableEngine(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	_ = svc.pipelineRepo.Update(ctx, "pipeline-a", map[string]any{"is_enabled": 0})
	response, err := svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "pipeline-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("disabled pipeline accepted: %+v %v", response, err)
	}
	response, err = svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "pipeline-b"})
	if err != nil || response.Success || response.Error.Code != 503 {
		t.Fatalf("missing engine reported success: %+v %v", response, err)
	}
}

func TestDeletePipelineRemovesItFromListing(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	response, err := svc.DeletePipeline(ctx, &pipelinev1.DeletePipelineRequest{PipelineId: "pipeline-a"})
	if err != nil || !response.Success {
		t.Fatal(response, err)
	}
	list, total, err := svc.pipelineRepo.List(ctx, &repo.PipelineQuery{})
	if err != nil || total != 1 || list[0].PipelineID != "pipeline-b" {
		t.Fatalf("deleted pipeline still listed: %+v %d %v", list, total, err)
	}
	if _, err := svc.pipelineRepo.Get(ctx, "pipeline-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted pipeline still readable: %v", err)
	}
}

func TestValidatedPipelinePath(t *testing.T) {
	for _, invalid := range []string{"", ".", "../outside", "/etc/passwd", ".git/config", "x/../../escape", `dir\\file.yml`} {
		if _, err := validatedPipelinePath(invalid); err == nil {
			t.Errorf("unsafe path accepted: %q", invalid)
		}
	}
	if path, err := validatedPipelinePath(".arcentra/build.yml"); err != nil || path != ".arcentra/build.yml" {
		t.Fatalf("valid path rejected: %q %v", path, err)
	}
}

func TestPipelineSCMCredentialsStayOnApprovedRepository(t *testing.T) {
	project := &model.Project{RepoURL: "https://git.example/team/app.git", AuthType: model.AuthTypeToken, Credential: "private-token"}
	for _, repository := range []string{"https://evil.example/team/app.git", "https://git.example/other/app.git", "http://git.example/team/app.git"} {
		if auth, err := scmAuthForPipeline(&model.Pipeline{RepoURL: repository}, project); err == nil || auth != nil {
			t.Fatalf("credentials allowed at unapproved repository %q", repository)
		}
	}
	auth, err := scmAuthForPipeline(&model.Pipeline{RepoURL: "https://git.example/team/app"}, project)
	if err != nil || auth["token"] != "private-token" {
		t.Fatalf("approved repository rejected: %v", err)
	}
	project.Credential = ""
	if _, err := scmAuthForPipeline(&model.Pipeline{RepoURL: "https://public.example/other.git"}, project); err != nil {
		t.Fatalf("credential-free source override rejected: %v", err)
	}
}

func TestSingleStepLogPagingUsesLineOrder(t *testing.T) {
	svc := runServiceFixture(t)
	if err := svc.logAggregator.PushLog(&LogEntry{StepRunID: "step-a", Timestamp: 0, LineNumber: 2, Content: "clock-skew"}); err != nil {
		t.Fatal(err)
	}
	entries, more, err := svc.GetPipelineRunLogs(context.Background(), "pipeline-a", "run-a", "step-a", 1, 1)
	if err != nil || !more || len(entries) != 1 || entries[0].LineNumber != 1 {
		t.Fatalf("line cursor ordering broken: %+v %v %v", entries, more, err)
	}
	entries, more, err = svc.GetPipelineRunLogs(context.Background(), "pipeline-a", "run-a", "step-a", 2, 1)
	if err != nil || more || len(entries) != 1 || entries[0].LineNumber != 2 {
		t.Fatalf("clock-skewed output skipped: %+v %v %v", entries, more, err)
	}
}

type completingPauseEngine struct {
	IPipelineEngine
	complete func()
	resumed  bool
}

func (e *completingPauseEngine) PauseRun(string) error  { e.complete(); return nil }
func (e *completingPauseEngine) ResumeRun(string) error { e.resumed = true; return nil }

func TestPauseCannotResurrectConcurrentCompletedRun(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	engine := &completingPauseEngine{complete: func() {
		if err := svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"status": model.PipelineStatusSuccess}); err != nil {
			t.Fatal(err)
		}
	}}
	svc.engine = engine
	response, err := svc.PausePipeline(ctx, &pipelinev1.PausePipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("completion race not rejected: %+v %v", response, err)
	}
	run, err := svc.pipelineRepo.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineStatusSuccess || !engine.resumed {
		t.Fatalf("terminal run resurrected or engine not restored: %+v", run)
	}
}

func TestRunAttemptHistoryAndLatestLogicalCounts(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	if err := svc.stepRunRepo.PatchByStepRunID(ctx, "step-a", map[string]any{"status": 5, "attempt": 1, "retry_mode": "failed_step"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id              string
		attempt, status int
		reason          string
	}{{"step-a-2", 2, 4, ""}, {"step-a-3", 3, 8, "attempt not used"}} {
		if err := svc.stepRunRepo.Create(ctx, &model.StepRun{PipelineID: "pipeline-a", PipelineRunID: "run-a", JobRunID: "job-a", StepRunID: item.id, Name: "shell", Attempt: item.attempt, RetryMode: "failed_step", PreviousStepRunID: "step-a", Status: item.status, ErrorMessage: item.reason}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-a")
	if err != nil || len(jobs) != 1 || len(jobs[0].Steps) != 3 {
		t.Fatalf("history missing: %+v %v", jobs, err)
	}
	job := jobs[0]
	if job.CompletedSteps != 1 || job.FailedSteps != 0 || job.Steps[0].Status != 5 || job.Steps[1].Attempt != 2 || job.Steps[2].Status != 8 {
		t.Fatalf("wrong logical result or audit: %+v", job)
	}
}

func TestManualStepRetryDoesNotCreateOrphanAttempt(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	_, before, err := svc.stepRunRepo.List(ctx, repo.StepRunFilter{PipelineRunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewStepRunServiceImpl(svc.stepRunRepo).RetryStepRun(ctx, &steprunv1.RetryStepRunRequest{StepRunId: "step-a"})
	if response != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("unsafe manual retry accepted: %+v %v", response, err)
	}
	_, after, err := svc.stepRunRepo.List(ctx, repo.StepRunFilter{PipelineRunID: "run-a"})
	if err != nil || before != after {
		t.Fatalf("orphan retry inserted: %d -> %d (%v)", before, after, err)
	}
}

func TestRunPendingAttemptSlotsDoNotHideExecutedResults(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	if err := svc.stepRunRepo.PatchByStepRunID(ctx, "step-a", map[string]any{"status": 4, "attempt": 1, "retry_mode": "failed_step"}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{2, 3} {
		if err := svc.stepRunRepo.Create(ctx, &model.StepRun{PipelineID: "pipeline-a", PipelineRunID: "run-a", JobRunID: "job-a", StepRunID: fmt.Sprintf("reserved-%d", attempt), Name: "shell", Status: 2, Attempt: attempt, RetryMode: "failed_step"}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-a")
	if err != nil || len(jobs) != 1 || jobs[0].CompletedSteps != 1 || jobs[0].FailedSteps != 0 {
		t.Fatalf("reserved attempts hid success: %+v %v", jobs, err)
	}
}

func TestPipelineJobSourceViewKeepsPerJobInitialCheckoutEvidence(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	if err := svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"commit_sha": strings.Repeat("d", 40), "definition_commit_sha": strings.Repeat("e", 40)}); err != nil {
		t.Fatal(err)
	}
	for index, repository := range []string{"https://git.example/Team/Build.git", "git@git.example:Other/Source.git"} {
		id := fmt.Sprintf("source-%d", index)
		ref := fmt.Sprintf("refs/heads/branch-%d", index)
		binding, err := sourceprovenance.NewBinding("pipeline-a", "run-a", id, "git", repository, ref)
		if err != nil {
			t.Fatal(err)
		}
		commit := strings.Repeat(fmt.Sprint(index+1), 40+24*index)
		// 仅为读取投影测试直接构造已接受的回执；生产创建入口禁止预填检出证据
		if err := svc.jobRunRepo.(*repo.JobRunRepo).Database().WithContext(ctx).Create(&model.JobRun{PipelineID: "pipeline-a", PipelineRunID: "run-a", JobRunID: id, JobName: id,
			Status: model.JobRunStatusSuccess, AgentID: "agent-1", ClaimID: "original-claim", TerminalReportDigest: strings.Repeat("f", 64),
			SourceExpectationCaptured: true, SourceType: binding.Type, SourceRepository: binding.Repository, SourceRef: binding.Ref, SourceBindingDigest: binding.Digest,
			SourceResolvedCommit: commit, SourceProvenanceState: "reported", Env: `{"TOKEN":"hidden-credential"}`}).Error; err != nil {
			t.Fatal(err)
		}
	}
	views, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-a")
	if err != nil || len(views) != 3 {
		t.Fatalf("missing per-job views: %+v %v", views, err)
	}
	byID := map[string]PipelineJobView{}
	for _, view := range views {
		byID[view.JobRunID] = view
	}
	if legacy := byID["job-a"]; legacy.SourceProvenanceState != "unknown" || legacy.SourceExpectationCaptured || legacy.SourceResolvedCommit != "" || legacy.SourceRepository != "" {
		t.Fatalf("legacy run inherited invented checkout evidence: %+v", legacy)
	}
	for index, repository := range []string{"https://git.example/Team/Build.git", "git@git.example:Other/Source.git"} {
		view := byID[fmt.Sprintf("source-%d", index)]
		if !view.SourceExpectationCaptured || view.SourceProvenanceState != "reported" || view.SourceRepository != repository ||
			view.SourceRef != fmt.Sprintf("refs/heads/branch-%d", index) || view.SourceResolvedCommit != strings.Repeat(fmt.Sprint(index+1), 40+24*index) {
			t.Fatalf("job evidence conflated with another source: %+v", view)
		}
	}
	raw, err := sonic.MarshalString(views)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hidden-credential", "sourceBindingDigest", "v1:", strings.Repeat("d", 40), strings.Repeat("e", 40)} {
		if strings.Contains(raw, secret) {
			t.Fatalf("source DTO exposed internal, credential or run-level evidence %q", secret)
		}
	}
}

func TestPipelineJobSourceViewFailsClosedForAbsentAndInvalidEvidence(t *testing.T) {
	binding, err := sourceprovenance.NewBinding("pipeline-a", "run-a", "job-a", "git", "https://git.example/Team/Source.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	valid := model.JobRun{PipelineID: "pipeline-a", PipelineRunID: "run-a", JobRunID: "job-a", SourceExpectationCaptured: true,
		Status: model.JobRunStatusSuccess, AgentID: "agent-1", ClaimID: "original-claim", TerminalReportDigest: strings.Repeat("f", 64),
		SourceType: binding.Type, SourceRepository: binding.Repository, SourceRef: binding.Ref, SourceBindingDigest: binding.Digest,
		SourceResolvedCommit: strings.Repeat("a", 40), SourceProvenanceState: "reported"}
	for _, tc := range []struct {
		name     string
		mutate   func(*model.JobRun)
		captured bool
	}{
		{"legacy expectation absent", func(job *model.JobRun) { job.SourceExpectationCaptured = false }, false},
		{"old Agent", func(job *model.JobRun) { job.SourceProvenanceState, job.SourceResolvedCommit = "", "" }, true},
		{"failed checkout", func(job *model.JobRun) {
			job.Status, job.SourceProvenanceState, job.SourceResolvedCommit = model.JobRunStatusFailed, "unknown", ""
		}, true},
		{"missing hash", func(job *model.JobRun) { job.SourceResolvedCommit = "" }, true},
		{"short hash", func(job *model.JobRun) { job.SourceResolvedCommit = "abcdef1" }, true},
		{"uppercase hash", func(job *model.JobRun) { job.SourceResolvedCommit = strings.Repeat("A", 40) }, true},
		{"unrecognized state", func(job *model.JobRun) { job.SourceProvenanceState = "verified" }, true},
		{"active job", func(job *model.JobRun) { job.Status = model.JobRunStatusRunning }, true},
		{"missing Agent", func(job *model.JobRun) { job.AgentID = "" }, true},
		{"missing original claim", func(job *model.JobRun) { job.ClaimID = "" }, true},
		{"missing receipt", func(job *model.JobRun) { job.TerminalReportDigest = "" }, true},
		{"malformed receipt", func(job *model.JobRun) { job.TerminalReportDigest = "not-a-receipt" }, true},
		{"unbound cancellation receipt", func(job *model.JobRun) {
			job.Status, job.CancellationClaimID = model.JobRunStatusCancelled, "different-claim"
		}, true},
		{"foreign job binding", func(job *model.JobRun) { job.JobRunID = "another-job" }, false},
		{"missing binding", func(job *model.JobRun) { job.SourceBindingDigest = "" }, false},
		{"changed ref", func(job *model.JobRun) { job.SourceRef = "other-branch" }, false},
		{"embedded credentials", func(job *model.JobRun) {
			job.SourceRepository = "https://user:hidden-password@git.example/Team/Source.git"
		}, false},
		{"query credentials", func(job *model.JobRun) { job.SourceRepository += "?token=hidden-token" }, false},
		{"no source", func(job *model.JobRun) { *job = model.JobRun{SourceExpectationCaptured: true} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := valid
			tc.mutate(&job)
			view := PipelineJobView{}
			populateJobSourceView(&view, &job)
			if view.SourceProvenanceState != "unknown" || view.SourceResolvedCommit != "" || view.SourceExpectationCaptured != tc.captured {
				t.Fatalf("invalid checkout promoted or wrong expectation state: %+v", view)
			}
			if !tc.captured && (view.SourceType != "" || view.SourceRepository != "" || view.SourceRef != "") {
				t.Fatalf("invalid expectation exposed raw source fields: %+v", view)
			}
		})
	}
	for _, state := range []int{model.JobRunStatusSuccess, model.JobRunStatusFailed, model.JobRunStatusTimeout, model.JobRunStatusCancelled} {
		job := valid
		job.Status = state
		view := PipelineJobView{}
		populateJobSourceView(&view, &job)
		if view.SourceProvenanceState != "reported" || view.SourceResolvedCommit != valid.SourceResolvedCommit {
			t.Fatalf("terminal result %d erased an initial checkout already reported by its original Agent: %+v", state, view)
		}
		if state == model.JobRunStatusCancelled {
			job.CancellationClaimID = job.ClaimID
			controllerCancelled := PipelineJobView{}
			populateJobSourceView(&controllerCancelled, &job)
			if controllerCancelled.SourceProvenanceState != "reported" || controllerCancelled.SourceResolvedCommit != valid.SourceResolvedCommit {
				t.Fatalf("controller cancellation erased an original-claim checkout report: %+v", controllerCancelled)
			}
		}
	}
}
