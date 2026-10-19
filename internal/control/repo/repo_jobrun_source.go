package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var jobSourceColumns = []string{"source_expectation_captured", "source_type", "source_repository", "source_ref", "source_binding_digest", "source_resolved_commit", "source_provenance_state"}
var jobSnapshotColumns = append([]string{"job_run_id", "job_name", "retry_approval_required", "retry_approval_id", "retry_approval_evidence_hash", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "status", "cancellation_claim_id", "terminal_report_digest", "start_time", "timeout"}, append(append(append(jobSourceColumns, jobRuntimeColumns...), jobInputColumns...), jobGenerationColumns...)...)

// CaptureSourceExpectation 在派发前原子保存一次源身份和目标 Agent，不允许重新绑定或重复排队
func (r *JobRunRepo) CaptureSourceExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding) error {
	return r.captureDispatchExpectation(ctx, pipelineID, runID, jobID, agentID, binding, nil, nil)
}

func (r *JobRunRepo) captureDispatchExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, inputs *taskqueue.JobInputManifest, plans ...*taskqueue.JobRetryPlan) error {
	var plan *taskqueue.JobRetryPlan
	if len(plans) > 1 {
		return fmt.Errorf("one retry plan is required")
	}
	if len(plans) == 1 {
		plan = plans[0]
	}
	if pipelineID == "" || runID == "" || jobID == "" || agentID == "" {
		return fmt.Errorf("job dispatch identity is required")
	}
	var canonical *sourceprovenance.Binding
	if binding != nil {
		var err error
		canonical, err = sourceprovenance.NewBinding(pipelineID, runID, jobID, binding.Type, binding.Repository, binding.Ref)
		if err != nil || *canonical != *binding {
			return fmt.Errorf("source binding does not match execution identity")
		}
	}
	return executionTransaction(ctx, r.Database(), jobID, func(tx *gorm.DB) error {
		lockMode := executionLockActive
		lockedJob, lockErr := lockExecutionJob(ctx, tx, jobID, lockMode)
		if lockErr != nil {
			return lockErr
		}
		current := *lockedJob
		if !emptyRetryApproval(&current) || !emptyJobGeneration(&current) || !emptyJobInputs(&current) || !emptyRuntime(&current) || current.JobRunID != jobID || current.PipelineID != pipelineID || current.PipelineRunID != runID || current.ClaimID != "" || current.CancellationClaimID != "" || current.TerminalReportDigest != "" || (current.Status != 1 && current.Status != 2) || current.SourceExpectationCaptured || current.SourceBindingDigest != "" || current.SourceType != "" || current.SourceRepository != "" || current.SourceRef != "" || current.SourceResolvedCommit != "" || (current.SourceProvenanceState != "" && current.SourceProvenanceState != "unknown") || (current.AgentID != "" && current.AgentID != agentID) {
			return fmt.Errorf("source expectation requires an unclaimed uncaptured job")
		}
		gate, err := retryApprovalContextForCapture(ctx, tx, &current)
		if err != nil {
			return err
		}
		required := gate != nil && gate.RequiresJob(current.JobName)
		if required && (plan == nil || runtime == nil || inputs == nil) {
			return fmt.Errorf("gated retry requires complete immutable dispatch")
		}
		if plan != nil {
			if plan.RetryApprovalRequired != required || (required && plan.RetryApprovalTimeoutSeconds != gate.Policy.TimeoutSeconds) {
				return fmt.Errorf("retry plan approval marker or budget differs from trusted run")
			}
		}
		if inputs != nil {
			if err := validateRegisteredInputArtifacts(ctx, tx, &current, inputs); err != nil {
				return err
			}
		}
		updates := map[string]any{"source_expectation_captured": true, "source_provenance_state": "unknown", "agent_id": agentID, "status": model.JobRunStatusQueued, "updated_at": time.Now(), "retry_approval_required": required}
		if inputs != nil {
			encoded, err := inputs.Canonical()
			if err != nil {
				return err
			}
			updates["input_expectation"] = encoded
		}
		if runtime != nil {
			updates["runtime_type"], updates["runtime_image"], updates["runtime_cpu"], updates["runtime_memory"], updates["runtime_binding_digest"] = runtime.Type, runtime.Image, runtime.Resources.CPU, runtime.Resources.Memory, runtime.BindingDigest
		}
		if canonical != nil {
			updates["source_type"] = canonical.Type
			updates["source_repository"] = canonical.Repository
			updates["source_ref"] = canonical.Ref
			updates["source_binding_digest"] = canonical.Digest
		}
		if plan != nil {
			encoded, err := plan.Canonical()
			if err != nil {
				return err
			}
			if runtime == nil || inputs == nil {
				return fmt.Errorf("retry requires runtime and input dispatch")
			}
			digest, err := plan.Digest(pipelineID, runID, jobID, agentID, runtime.BindingDigest)
			if err != nil {
				return err
			}
			if err := validateCapturedRetrySteps(ctx, tx, &current, agentID, plan); err != nil {
				return err
			}
			updates["retry_plan"], updates["retry_plan_digest"] = encoded, digest
		}
		// 同一事务分配预生成的步骤，避免取消在作业与步骤 Agent 赋值之间穿过
		var steps []model.StepRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "status", "cancellation_claim_id", "terminal_report_digest").Where("job_run_id = ?", jobID).Find(&steps).Error; err != nil {
			return err
		}
		for _, step := range steps {
			if step.JobRunID != jobID || step.PipelineID != pipelineID || step.PipelineRunID != runID || step.ClaimID != "" || step.CancellationClaimID != "" || step.TerminalReportDigest != "" || (step.Status != 1 && step.Status != 2) || (step.AgentID != "" && step.AgentID != agentID) {
				return fmt.Errorf("source capture requires unclaimed job steps")
			}
		}
		if len(steps) > 0 {
			result := tx.Model(&model.StepRun{}).Where("job_run_id = ? AND pipeline_id = ? AND pipeline_run_id = ?", jobID, pipelineID, runID).Where("status IN ? AND claim_id = ? AND agent_id IN ?", []int{1, 2}, "", []string{"", agentID}).Updates(map[string]any{"agent_id": agentID, "status": model.JobRunStatusQueued, "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(steps)) {
				return fmt.Errorf("job step identity changed during source capture")
			}
		}
		return reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), &current).Updates(updates))
	})
}

func jobSourceEqual(a, b *model.JobRun) bool {
	return a.SourceExpectationCaptured == b.SourceExpectationCaptured && a.SourceType == b.SourceType && a.SourceRepository == b.SourceRepository && a.SourceRef == b.SourceRef && a.SourceBindingDigest == b.SourceBindingDigest && a.SourceResolvedCommit == b.SourceResolvedCommit && a.SourceProvenanceState == b.SourceProvenanceState
}
func jobSnapshotEqual(a, b *model.JobRun) bool {
	return a.JobName == b.JobName && a.JobRunID == b.JobRunID && a.PipelineID == b.PipelineID && a.PipelineRunID == b.PipelineRunID && a.AgentID == b.AgentID && a.ClaimID == b.ClaimID && a.Status == b.Status && a.CancellationClaimID == b.CancellationClaimID && a.TerminalReportDigest == b.TerminalReportDigest && jobSourceEqual(a, b) && jobRuntimeEqual(a, b) && jobInputsEqual(a, b) && jobGenerationEqual(a, b) && retryApprovalEqual(a, b)
}
func sourceField(key string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(key), "_", ""), "source")
}
func rejectSourceUpdates(updates map[string]any) error {
	for key := range updates {
		if strings.ReplaceAll(strings.ToLower(key), "_", "") == "jobname" {
			return fmt.Errorf("job name is immutable")
		}
		if sourceField(key) || runtimeField(key) || inputField(key) || generationField(key) || retryApprovalField(key) {
			return fmt.Errorf("source and runtime expectations require a dedicated atomic operation")
		}
	}
	return nil
}
func changesJobIdentity(updates map[string]any) bool {
	for key := range updates {
		switch strings.ReplaceAll(strings.ToLower(key), "_", "") {
		case "jobrunid", "pipelineid", "pipelinerunid", "agentid", "claimid", "cancellationclaimid":
			return true
		}
	}
	return false
}
func validateSourceReportUpdates(expected *model.JobRun, updates map[string]any, cancellation bool) error {
	if err := validatePinnedSourceUpdates(expected, updates); err != nil {
		return err
	}
	for key := range updates {
		if runtimeField(key) || inputField(key) || generationField(key) || retryApprovalField(key) {
			return fmt.Errorf("runtime expectation is immutable")
		}
	}
	found := false
	for key := range updates {
		if sourceField(key) {
			if key != "source_resolved_commit" && key != "source_provenance_state" {
				return fmt.Errorf("source expectation is immutable")
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	state, stateOK := updates["source_provenance_state"].(string)
	commit, commitOK := updates["source_resolved_commit"].(string)
	digest, _ := updates["terminal_report_digest"].(string)
	next, _ := updates["status"].(int)
	if !stateOK || !commitOK || len(digest) != 64 || (!cancellation && !model.IsJobRunTerminal(next)) || expected.SourceResolvedCommit != "" || (expected.SourceProvenanceState != "" && expected.SourceProvenanceState != "unknown") {
		return fmt.Errorf("source result requires a new terminal receipt")
	}
	if state == "unknown" && commit == "" {
		return nil
	}
	if state != "reported" || !sourceprovenance.ValidCommit(commit) || !expected.SourceExpectationCaptured || expected.ClaimID == "" || expected.AgentID == "" {
		return fmt.Errorf("source result requires an original execution claim")
	}
	binding, err := sourceprovenance.NewBinding(expected.PipelineID, expected.PipelineRunID, expected.JobRunID, expected.SourceType, expected.SourceRepository, expected.SourceRef)
	if err != nil || binding.Type != "git" || binding.Digest != expected.SourceBindingDigest {
		return fmt.Errorf("source result binding is invalid")
	}
	claim, _ := updates["claim_id"].(string)
	if !cancellation && claim != expected.ClaimID {
		return fmt.Errorf("source result claim changed")
	}
	return nil
}

func validateJobReportIdentityUpdates(expected *model.JobRun, updates map[string]any) error {
	for key, value := range updates {
		normalized := strings.ReplaceAll(strings.ToLower(key), "_", "")
		switch normalized {
		case "jobrunid", "jobname", "pipelineid", "pipelinerunid", "agentid", "cancellationclaimid":
			return fmt.Errorf("job report cannot change execution identity")
		case "claimid":
			claim, ok := value.(string)
			if key != "claim_id" || !ok || claim == "" || (expected.ClaimID != "" && expected.ClaimID != claim) {
				return fmt.Errorf("job report cannot replace the original claim")
			}
		}
	}
	return nil
}
