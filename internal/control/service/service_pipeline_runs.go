package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"gorm.io/gorm"
)

// ErrPipelineRunNotFound 表示指定流水线下不存在该运行记录
var ErrPipelineRunNotFound = errors.New("pipeline run not found")

// PipelineJobView 仅暴露作业执行状态，避免返回包含凭据的环境和参数
type PipelineJobView struct {
	JobRunID       string             `json:"jobRunId"`
	PipelineID     string             `json:"pipelineId"`
	PipelineRunID  string             `json:"pipelineRunId"`
	JobName        string             `json:"jobName"`
	AgentID        string             `json:"agentId"`
	Status         int                `json:"status"`
	TotalSteps     int                `json:"totalSteps"`
	CompletedSteps int                `json:"completedSteps"`
	FailedSteps    int                `json:"failedSteps"`
	ErrorMessage   string             `json:"errorMessage"`
	StartTime      *time.Time         `json:"startTime"`
	EndTime        *time.Time         `json:"endTime"`
	Duration       int64              `json:"duration"`
	Steps          []PipelineStepView `json:"steps"`
}

// PipelineStepView 描述单个步骤的执行进度
type PipelineStepView struct {
	DeploymentReleaseID string     `json:"deploymentReleaseId"`
	Attempt             int        `json:"attempt"`
	RetryMode           string     `json:"retryMode"`
	PreviousStepRunID   string     `json:"previousStepRunId"`
	StepRunID           string     `json:"stepRunId"`
	JobRunID            string     `json:"jobRunId"`
	Name                string     `json:"name"`
	StepIndex           int        `json:"stepIndex"`
	Status              int        `json:"status"`
	Uses                string     `json:"uses"`
	Action              string     `json:"action"`
	ExitCode            *int       `json:"exitCode"`
	ErrorMessage        string     `json:"errorMessage"`
	StartTime           *time.Time `json:"startTime"`
	EndTime             *time.Time `json:"endTime"`
	Duration            int64      `json:"duration"`
}

// PipelineLogView 描述经过运行归属校验的构建日志
type PipelineLogView struct {
	Timestamp  int64  `json:"timestamp"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	JobID      string `json:"jobId"`
	StepID     string `json:"stepId"`
	LineNumber int32  `json:"lineNumber"`
	Stream     string `json:"stream"`
}

func (s *PipelineServiceImpl) scopedRun(ctx context.Context, pipelineID, runID string) (*model.PipelineRun, error) {
	run, err := s.pipelineRepo.GetRun(ctx, runID)
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && (run == nil || run.PipelineID != pipelineID) {
		return nil, ErrPipelineRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return run, nil
}

// GetScopedPipelineRun 验证运行归属后返回流水线进度
func (s *PipelineServiceImpl) GetScopedPipelineRun(ctx context.Context, pipelineID, runID string) (*pipelinev1.PipelineRunDetail, error) {
	run, err := s.scopedRun(ctx, pipelineID, runID)
	if err != nil {
		return nil, err
	}
	if s.jobRunRepo != nil {
		jobs, err := s.jobRunRepo.ListByPipelineRunID(ctx, runID)
		if err != nil {
			return nil, err
		}
		run.CompletedJobs, run.FailedJobs, run.RunningJobs = jobCounts(jobs)
	}
	return toPipelineRunDetail(run), nil
}

func jobCounts(jobs []*model.JobRun) (completed, failed, running int) {
	for _, job := range jobs {
		if job == nil {
			continue
		}
		if model.IsJobRunTerminal(job.Status) {
			completed++
		}
		if job.Status == model.JobRunStatusFailed || job.Status == model.JobRunStatusTimeout {
			failed++
		}
		if job.Status == model.JobRunStatusRunning {
			running++
		}
	}
	return
}

func (s *PipelineServiceImpl) runSteps(ctx context.Context, pipelineID, runID string) ([]model.StepRun, error) {
	if s.stepRunRepo == nil {
		return nil, fmt.Errorf("step run repository unavailable")
	}
	steps := make([]model.StepRun, 0)
	for page := 1; ; page++ {
		list, total, err := s.stepRunRepo.List(ctx, repo.StepRunFilter{PipelineID: pipelineID, PipelineRunID: runID, Page: page, PageSize: 200, SortBy: "step_index"})
		if err != nil {
			return nil, err
		}
		steps = append(steps, list...)
		if len(list) == 0 || int64(len(steps)) >= total {
			break
		}
	}
	return steps, nil
}

// ListPipelineRunJobs 返回当前运行的作业及步骤状态
func (s *PipelineServiceImpl) ListPipelineRunJobs(ctx context.Context, pipelineID, runID string) ([]PipelineJobView, error) {
	if _, err := s.scopedRun(ctx, pipelineID, runID); err != nil {
		return nil, err
	}
	if s.jobRunRepo == nil {
		return nil, fmt.Errorf("job run repository unavailable")
	}
	jobs, err := s.jobRunRepo.ListByPipelineRunID(ctx, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.runSteps(ctx, pipelineID, runID)
	if err != nil {
		return nil, err
	}
	byJob := make(map[string][]PipelineStepView)
	for _, step := range steps {
		byJob[step.JobRunID] = append(byJob[step.JobRunID], PipelineStepView{DeploymentReleaseID: step.DeploymentReleaseID, StepRunID: step.StepRunID, JobRunID: step.JobRunID,
			Attempt: max(step.Attempt, 1), RetryMode: step.RetryMode, PreviousStepRunID: step.PreviousStepRunID, Name: step.Name, StepIndex: step.StepIndex, Status: step.Status, Uses: step.Uses, Action: step.Action, ExitCode: step.ExitCode,
			ErrorMessage: step.ErrorMessage, StartTime: step.StartTime, EndTime: step.EndTime, Duration: step.Duration})
	}
	out := make([]PipelineJobView, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}
		childSteps := byJob[job.JobRunID]
		if childSteps == nil {
			childSteps = []PipelineStepView{}
		}
		sort.SliceStable(childSteps, func(i, j int) bool {
			if childSteps[i].StepIndex != childSteps[j].StepIndex {
				return childSteps[i].StepIndex < childSteps[j].StepIndex
			}
			return childSteps[i].Attempt < childSteps[j].Attempt
		})
		latest := map[string]PipelineStepView{}
		for _, step := range childSteps {
			previous, found := latest[step.Name]
			used := step.Status >= 3 && !(step.Status == 8 && step.ErrorMessage == "attempt not used")
			previousUsed := previous.Status >= 3 && !(previous.Status == 8 && previous.ErrorMessage == "attempt not used")
			if !found || used && (!previousUsed || step.Attempt >= previous.Attempt) {
				latest[step.Name] = step
			}
		}
		completed, failed := 0, 0
		for _, step := range latest {
			if step.Status >= 4 {
				completed++
			}
			if step.Status == 5 || step.Status == 7 {
				failed++
			}
		}
		out = append(out, PipelineJobView{JobRunID: job.JobRunID, PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID,
			JobName: job.JobName, AgentID: job.AgentID, Status: job.Status, TotalSteps: job.TotalSteps, CompletedSteps: completed,
			FailedSteps: failed, ErrorMessage: job.ErrorMessage, StartTime: job.StartTime, EndTime: job.EndTime, Duration: job.Duration, Steps: childSteps})
	}
	return out, nil
}

// GetPipelineRunLogs 仅查询当前运行所属步骤的日志
func (s *PipelineServiceImpl) GetPipelineRunLogs(ctx context.Context, pipelineID, runID, stepID string, fromLine int32, limit int) ([]PipelineLogView, bool, error) {
	if _, err := s.scopedRun(ctx, pipelineID, runID); err != nil {
		return nil, false, err
	}
	steps, err := s.runSteps(ctx, pipelineID, runID)
	if err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(steps))
	byID := make(map[string]string)
	for _, step := range steps {
		if stepID != "" && step.StepRunID != stepID {
			continue
		}
		ids = append(ids, step.StepRunID)
		byID[step.StepRunID] = step.JobRunID
	}
	if stepID != "" && len(ids) == 0 {
		return nil, false, ErrPipelineRunNotFound
	}
	out := make([]PipelineLogView, 0)
	if len(ids) == 0 {
		return out, false, nil
	}
	if s.logAggregator == nil {
		return nil, false, fmt.Errorf("log aggregator unavailable")
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	if fromLine < 0 {
		fromLine = 0
	}
	var entries []*LogEntry
	if stepID != "" {
		entries, err = s.logAggregator.GetLogsByStepRunIDContext(ctx, stepID, fromLine, limit+1)
	} else {
		entries, err = s.logAggregator.GetLogsForStepRunIDs(ctx, ids, fromLine, limit+1)
	}
	if err != nil {
		return nil, false, err
	}
	truncated := len(entries) > limit || limit == 5000 && len(entries) == limit
	if truncated {
		entries = entries[:limit]
	}
	for _, entry := range entries {
		out = append(out, PipelineLogView{Timestamp: entry.Timestamp, Level: entry.Level, Message: entry.Content,
			JobID: byID[entry.StepRunID], StepID: entry.StepRunID, LineNumber: entry.LineNumber, Stream: entry.Stream})
	}
	return out, truncated, nil
}
