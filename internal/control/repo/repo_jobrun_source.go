package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var jobSourceColumns = []string{"source_expectation_captured", "source_type", "source_repository", "source_ref", "source_binding_digest", "source_resolved_commit", "source_provenance_state"}
var jobSnapshotColumns = append([]string{"job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "status", "cancellation_claim_id", "terminal_report_digest"}, jobSourceColumns...)

// CaptureSourceExpectation 在派发前原子保存一次源身份和目标 Agent，不允许重新绑定或重复排队
func (r *JobRunRepo) CaptureSourceExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding) error {
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
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", jobID).First(&current).Error; err != nil {
			return err
		}
		if current.JobRunID != jobID || current.PipelineID != pipelineID || current.PipelineRunID != runID || current.ClaimID != "" || current.CancellationClaimID != "" || current.TerminalReportDigest != "" || (current.Status != 1 && current.Status != 2) || current.SourceExpectationCaptured || current.SourceBindingDigest != "" || current.SourceType != "" || current.SourceRepository != "" || current.SourceRef != "" || current.SourceResolvedCommit != "" || (current.SourceProvenanceState != "" && current.SourceProvenanceState != "unknown") || (current.AgentID != "" && current.AgentID != agentID) {
			return fmt.Errorf("source expectation requires an unclaimed uncaptured job")
		}
		updates := map[string]any{"source_expectation_captured": true, "source_provenance_state": "unknown", "agent_id": agentID, "status": model.JobRunStatusQueued, "updated_at": time.Now()}
		if canonical != nil {
			updates["source_type"] = canonical.Type
			updates["source_repository"] = canonical.Repository
			updates["source_ref"] = canonical.Ref
			updates["source_binding_digest"] = canonical.Digest
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
	return a.JobRunID == b.JobRunID && a.PipelineID == b.PipelineID && a.PipelineRunID == b.PipelineRunID && a.AgentID == b.AgentID && a.ClaimID == b.ClaimID && a.Status == b.Status && a.CancellationClaimID == b.CancellationClaimID && a.TerminalReportDigest == b.TerminalReportDigest && jobSourceEqual(a, b)
}
func sourceField(key string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(key), "_", ""), "source")
}
func rejectSourceUpdates(updates map[string]any) error {
	for key := range updates {
		if sourceField(key) {
			return fmt.Errorf("source provenance requires a dedicated atomic operation")
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
		case "jobrunid", "pipelineid", "pipelinerunid", "agentid", "cancellationclaimid":
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
