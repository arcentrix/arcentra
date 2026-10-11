package taskqueue

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type transientStatusClient struct {
	agentv1.AgentServiceClient
	terminalCalls int
}

func (c *transientStatusClient) ReportStepRunStatus(context.Context, *agentv1.ReportStepRunStatusRequest, ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}
func (c *transientStatusClient) ReportJobRunStatus(_ context.Context, req *agentv1.ReportJobRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	if req.Status >= 4 {
		c.terminalCalls++
		if c.terminalCalls == 1 {
			return nil, status.Error(codes.Unavailable, "temporary network failure")
		}
	}
	return &agentv1.ReportJobRunStatusResponse{Success: true}, nil
}

func TestTerminalReportRetryDoesNotRepeatExecution(t *testing.T) {
	client := &transientStatusClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "retry-report", Steps: []taskpayload.StepPayload{{Name: "once", StepRunID: "retry-report-step", Args: map[string]any{"run": "printf x >> executions"}}}}
	if err := executeJobRun(context.Background(), conf, wrapper, nil, payload); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected retryable report failure: %v", err)
	}
	if err := executeJobRun(context.Background(), conf, wrapper, nil, payload); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", "retry-report", "executions"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes) != "x" {
		t.Fatalf("shell executed more than once: %q", bytes)
	}
	if client.terminalCalls != 2 {
		t.Fatalf("terminal report not retried: %d", client.terminalCalls)
	}
}

func TestStepReportFailureDoesNotChangeSuccessfulExecution(t *testing.T) {
	client := &artifactStatusClient{failStepTerminals: 2, failStepID: "receipt-success-first"}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "receipt-success", Steps: []taskpayload.StepPayload{
		{Name: "first", StepRunID: "receipt-success-first", Args: map[string]any{"run": "printf x >> first"}},
		{Name: "second", StepRunID: "receipt-success-second", Args: map[string]any{"run": "printf x >> second"}},
	}}
	if err := executeJobRun(context.Background(), conf, wrapper, nil, payload); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected pending step report: %v", err)
	}
	for _, report := range client.jobReports {
		if report.Status >= 4 {
			t.Fatal("job was acknowledged before pending step reports")
		}
	}
	if err := executeJobRun(context.Background(), conf, wrapper, nil, payload); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if got := artifactTestRead(t, filepath.Join(conf.Agent.WorkspaceDir, "run", "receipt-success"), name); got != "x" {
			t.Fatalf("%s was skipped or repeated: %s", name, got)
		}
	}
	if got := client.jobReports[len(client.jobReports)-1].Status; got != 4 {
		t.Fatalf("reporting failure changed successful job to %d", got)
	}
}

func TestSynthesizedTerminalStepReportsAreRetried(t *testing.T) {
	for _, test := range []struct {
		name             string
		when             string
		cancel           bool
		workspaceFailure bool
		firstFailure     bool
		wantStep         int32
		wantJob          int32
	}{
		{name: "condition_skip", when: "false", wantStep: 8, wantJob: 4},
		{name: "condition_error", when: "missing.field", wantStep: 5, wantJob: 5},
		{name: "cancel", cancel: true, wantStep: 6, wantJob: 6},
		{name: "workspace", workspaceFailure: true, wantStep: 8, wantJob: 5},
		{name: "previous_failure", firstFailure: true, wantStep: 8, wantJob: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobID := "receipt-" + test.name
			stepID := jobID + "-terminal"
			client := &artifactStatusClient{failStepTerminals: 2, failStepID: stepID}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
			if test.workspaceFailure {
				file := filepath.Join(conf.Agent.WorkspaceDir, "not-a-directory")
				if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				conf.Agent.WorkspaceDir = file
			}
			payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: jobID, Steps: []taskpayload.StepPayload{{Name: "unexecuted", StepRunID: stepID, When: test.when, Args: map[string]any{"run": "touch unexpected"}}}}
			if test.firstFailure {
				payload.Steps = append([]taskpayload.StepPayload{{Name: "fail", StepRunID: jobID + "-fail", Args: map[string]any{"run": "exit 1"}}}, payload.Steps...)
			}
			if test.cancel {
				CancelJobRun(jobID)
			}
			if err := executeJobRun(context.Background(), conf, wrapper, nil, payload); status.Code(err) != codes.Unavailable {
				t.Fatalf("terminal report failure was lost: %v", err)
			}
			for _, report := range client.jobReports {
				if report.Status >= 4 {
					t.Fatal("job was acknowledged before pending terminal report")
				}
			}
			err := executeJobRun(context.Background(), conf, wrapper, nil, payload)
			if test.wantJob == 4 && err != nil || test.wantJob != 4 && acknowledgeCompleted(err) != nil {
				t.Fatalf("terminal delivery retry failed: %v", err)
			}
			calls := 0
			var timestamp int64
			for _, report := range client.stepReports {
				if report.StepRunId == stepID {
					calls++
					if int32(report.Status) != test.wantStep {
						t.Fatalf("unexpected reported state: %v", report.Status)
					}
					if timestamp != 0 && timestamp != report.EndTime {
						t.Fatal("retry changed terminal timestamp")
					}
					timestamp = report.EndTime
				}
			}
			if calls != 3 || timestamp == 0 {
				t.Fatalf("terminal report was not retained: calls=%d timestamp=%d", calls, timestamp)
			}
			if got := client.jobReports[len(client.jobReports)-1].Status; got != test.wantJob {
				t.Fatalf("job status=%d want=%d", got, test.wantJob)
			}
		})
	}
}

func TestStandaloneWorkspaceFailureReportIsRetried(t *testing.T) {
	client := &artifactStatusClient{failStepTerminals: 1}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	base := t.TempDir()
	file := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: file}}
	payload := &taskpayload.StepRunTaskPayload{PipelineRunID: "run", JobID: "job", StepRunID: "receipt-standalone", Args: map[string]any{"run": "exit 0"}}
	if err := executeStepRunViaExecutor(context.Background(), conf, wrapper, nil, payload); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected failed terminal report: %v", err)
	}
	if err := executeStepRunViaExecutor(context.Background(), conf, wrapper, nil, payload); err == nil || acknowledgeCompleted(err) != nil {
		t.Fatalf("failed workspace result was not retained: %v", err)
	}
	if len(client.stepReports) != 3 || int(client.stepReports[0].Status) != 3 || int(client.stepReports[1].Status) != 5 || int(client.stepReports[2].Status) != 5 {
		t.Fatalf("unexpected replay: %v", client.stepReports)
	}
}
