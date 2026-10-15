// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IStepAttemptRepository 提供独立可选的步骤准入能力，避免扩大旧仓储契约
type IStepAttemptRepository interface {
	ReconcileStepAttempt(context.Context, taskqueue.StepAttemptTransition) (*taskqueue.StepAttemptResult, error)
}

var stepAttemptColumns = []string{"step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "cancellation_claim_id", "name", "step_index", "attempt", "retry_mode", "retry_policy", "previous_step_run_id", "uses", "action", "continue_on_error", "status", "terminal_report_digest", "exit_code", "generation_id", "generation_binding_digest", "retryable_shell_exit", "attempt_admission_request", "attempt_admission_request_digest", "attempt_admission"}

func stepAttemptField(key string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(key), "_", ""), "attemptadmission")
}
func emptyStepAdmission(step *model.StepRun) bool {
	return step.AttemptAdmissionRequest == "" && step.AttemptAdmissionRequestDigest == "" && step.AttemptAdmission == ""
}
func sameStepAdmission(a, b *model.StepRun) bool {
	return a.AttemptAdmissionRequest == b.AttemptAdmissionRequest && a.AttemptAdmissionRequestDigest == b.AttemptAdmissionRequestDigest && a.AttemptAdmission == b.AttemptAdmission
}

type admittedStepChain struct {
	plan       taskqueue.RetryStepPlan
	rows       []model.StepRun
	requests   []*taskqueue.StepAttemptTransition
	admissions []*taskqueue.StepAttemptAdmission
	current    int
}

func stepScopeMatches(scope taskqueue.StepAttemptIdentity, job *model.JobRun, g *taskqueue.JobGeneration, planned taskqueue.RetryStepPlan, index int) bool {
	return scope.Identity == generationIdentity(job) && scope.GenerationID == g.GenerationID && scope.GenerationBindingDigest == g.BindingDigest && scope.RuntimeClaimID == g.RuntimeClaimID && scope.InputManifestDigest == job.InputManifestDigest && scope.RetryPlanDigest == job.RetryPlanDigest && scope.LogicalStepRunID == planned.AttemptRunIDs[0] && scope.StepRunID == planned.AttemptRunIDs[index] && scope.StepName == planned.Name && scope.StepIndex == planned.StepIndex && scope.DefinitionDigest == planned.DefinitionDigest && scope.Attempt == index+1 && scope.StepClaimID == agentidentity.StepClaimID(job.ClaimID, scope.StepRunID)
}

// 锁定父作业和固定代次后，仅通过完整连续不可变账本推导每个逻辑步骤的游标
func loadStepAttemptLedger(ctx context.Context, tx *gorm.DB, job *model.JobRun, g *taskqueue.JobGeneration, plan *taskqueue.JobRetryPlan) ([]admittedStepChain, error) {
	if plan.Policy.Mode != "failed_step" || plan.Version != 2 || g.Number != 1 || job.CurrentGeneration != 1 || job.CurrentGenerationID != g.GenerationID || !generationMatchesJob(g, job) {
		return nil, fmt.Errorf("failed_step requires its fixed initial generation")
	}
	var all []model.StepRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(stepAttemptColumns).Where("job_run_id = ?", job.JobRunID).Order("step_index ASC").Order("attempt ASC").Find(&all).Error; err != nil {
		return nil, err
	}
	if len(all) != len(plan.Steps)*int(plan.Policy.MaxAttempts) {
		return nil, fmt.Errorf("complete immutable step allocation is required")
	}
	byID := map[string]model.StepRun{}
	for _, row := range all {
		if _, exists := byID[row.StepRunID]; exists {
			return nil, fmt.Errorf("duplicate step identity")
		}
		byID[row.StepRunID] = row
	}
	policy, err := sonic.MarshalString(plan.Policy)
	if err != nil {
		return nil, err
	}
	var session *taskqueue.StepAttemptSession
	var lastBoundarySequence uint64
	nonces := map[string]bool{}
	admissions := map[string]bool{}
	chains := make([]admittedStepChain, 0, len(plan.Steps))
	for _, planned := range plan.Steps {
		chain := admittedStepChain{plan: planned, current: -1, rows: make([]model.StepRun, len(planned.AttemptRunIDs)), requests: make([]*taskqueue.StepAttemptTransition, len(planned.AttemptRunIDs)), admissions: make([]*taskqueue.StepAttemptAdmission, len(planned.AttemptRunIDs))}
		gap := false
		for index, id := range planned.AttemptRunIDs {
			row, ok := byID[id]
			previous := ""
			if index > 0 {
				previous = planned.AttemptRunIDs[index-1]
			}
			if !ok || row.JobRunID != job.JobRunID || row.PipelineID != job.PipelineID || row.PipelineRunID != job.PipelineRunID || row.AgentID != job.AgentID || row.Name != planned.Name || row.StepIndex != int(planned.StepIndex) || row.Attempt != index+1 || row.PreviousStepRunID != previous || row.RetryMode != "failed_step" || !sameRetryPolicy(row.RetryPolicy, policy) || row.Uses != planned.Uses || row.Action != planned.Action || (row.ContinueOnError != 0) != planned.ContinueOnError {
				return nil, fmt.Errorf("stored failed_step lineage differs from immutable plan")
			}
			chain.rows[index] = row
			if emptyStepAdmission(&row) {
				gap = true
				continue
			}
			if gap || row.AttemptAdmissionRequest == "" || row.AttemptAdmissionRequestDigest == "" || row.AttemptAdmission == "" {
				return nil, fmt.Errorf("step admission chain has a gap or incomplete ledger")
			}
			request, err := taskqueue.ParseStepAttemptTransition(row.AttemptAdmissionRequest)
			if err != nil {
				return nil, err
			}
			admission, err := taskqueue.ParseStepAttemptAdmission(row.AttemptAdmission)
			if err != nil {
				return nil, err
			}
			if err := admission.ValidateTransition(*request); err != nil {
				return nil, err
			}
			if request.RequestDigest != row.AttemptAdmissionRequestDigest || !stepScopeMatches(admission.Scope, job, g, planned, index) || admissions[admission.AdmissionID] {
				return nil, fmt.Errorf("stored step admission binding differs from immutable scope")
			}
			admissions[admission.AdmissionID] = true
			if session == nil {
				copy := admission.Scope.Session
				session = &copy
			} else if *session != admission.Scope.Session {
				return nil, fmt.Errorf("step admission changed original live session")
			}
			if index == 0 {
				if request.Operation != "admit" {
					return nil, fmt.Errorf("step chain requires explicit initial admission")
				}
			} else {
				prior := chain.admissions[index-1]
				failed := chain.rows[index-1]
				if prior == nil || request.Operation != "advance" || request.PreviousAdmissionID != prior.AdmissionID || request.PreviousAdmissionBindingDigest != prior.BindingDigest || request.PreviousStepRunID != failed.StepRunID || request.PreviousStepClaimID != prior.Scope.StepClaimID || request.FailedStepReceiptDigest != failed.TerminalReportDigest || failed.ClaimID != prior.Scope.StepClaimID || failed.GenerationID != g.GenerationID || failed.GenerationBindingDigest != g.BindingDigest || !failed.RetryableShellExit || !allowedReportedFailure(failed, plan.Policy) || (failed.Uses != "shell" && failed.Uses != "builtin:shell") {
					return nil, fmt.Errorf("step chain lacks exact committed pure-shell predecessor")
				}
				b := request.Boundary
				if b == nil || b.BoundarySequence <= lastBoundarySequence || nonces[b.BoundaryNonce] {
					return nil, fmt.Errorf("step boundary sequence or nonce was reused")
				}
				nonces[b.BoundaryNonce] = true
				lastBoundarySequence = b.BoundarySequence
				delay, _ := time.ParseDuration(plan.Policy.Delay)
				if admission.NotBeforeUnixMilli < b.CapturedUnixMilli+delay.Milliseconds() || admission.ExpiresUnixMilli > b.ExpiresUnixMilli {
					return nil, fmt.Errorf("step admission escaped its paused boundary lifetime")
				}
			}
			chain.requests[index] = request
			chain.admissions[index] = admission
			chain.current = index
		}
		if chain.current >= 0 && len(chains) > 0 {
			prior := &chains[len(chains)-1]
			if !stepChainAllowsNext(prior) {
				return nil, fmt.Errorf("later logical admission overlaps an unfinished predecessor")
			}
		}
		chains = append(chains, chain)
	}
	return chains, nil
}

func stepChainAllowsNext(chain *admittedStepChain) bool {
	if chain == nil || chain.current < 0 {
		return false
	}
	row := chain.rows[chain.current]
	admission := chain.admissions[chain.current]
	return admission != nil && (row.Status == model.JobRunStatusSuccess || (row.Status == model.JobRunStatusSkipped && chain.current == 0 && row.Attempt == 1 && !row.RetryableShellExit)) && artifactutil.Digest(row.TerminalReportDigest) == nil && row.ClaimID == admission.Scope.StepClaimID && row.GenerationID == admission.Scope.GenerationID && row.GenerationBindingDigest == admission.Scope.GenerationBindingDigest
}

func findStepAttemptChain(chains []admittedStepChain, id string) (int, int) {
	for i := range chains {
		for j := range chains[i].rows {
			if chains[i].rows[j].StepRunID == id {
				return i, j
			}
		}
	}
	return -1, -1
}
func stepAttemptDeadline(job *model.JobRun) (int64, error) {
	if job.Timeout == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(job.Timeout)
	if err != nil || duration <= 0 || job.StartTime == nil {
		return 0, fmt.Errorf("invalid original job deadline")
	}
	return job.StartTime.Add(duration).UnixMilli(), nil
}
func validateStepAttemptLive(ctx context.Context, tx *gorm.DB, job *model.JobRun, record *model.JobRunGeneration, g *taskqueue.JobGeneration, admission *taskqueue.StepAttemptAdmission, requireDelay bool) error {
	if record.State != "active" && record.State != "preparing" {
		return fmt.Errorf("step generation is no longer active")
	}
	if g.Number != 1 || g.GenerationID != job.CurrentGenerationID || g.Number != job.CurrentGeneration || !generationMatchesJob(g, job) {
		return fmt.Errorf("step generation changed")
	}
	if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if err := validateGenerationInputs(job, admission.Scope.InputManifestDigest, false); err != nil {
		return err
	}
	if err := validateRetryApprovalExecution(ctx, tx, job, true); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if now >= admission.ExpiresUnixMilli || (requireDelay && now < admission.NotBeforeUnixMilli) {
		return fmt.Errorf("step admission is expired or delayed")
	}
	return nil
}

// ReconcileStepAttempt 在父作业锁内按完整连续账本准入原步骤或唯一紧邻后继
// 准确重放保留原响应，历史观察和检查绝不恢复活动执行许可
func (r *JobRunRepo) ReconcileStepAttempt(ctx context.Context, request taskqueue.StepAttemptTransition) (*taskqueue.StepAttemptResult, error) {
	encoded, err := request.Canonical()
	if err != nil {
		return nil, err
	}
	var result *taskqueue.StepAttemptResult
	err = r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", request.Scope.Identity.JobRunID).First(&job).Error; err != nil {
			return err
		}
		if generationIdentity(&job) != request.Scope.Identity || !job.SourceExpectationCaptured || job.RetryPlanDigest != request.Scope.RetryPlanDigest || job.InputManifestDigest != request.Scope.InputManifestDigest {
			return fmt.Errorf("step attempt original scope changed")
		}
		plan, err := retryPlanForJob(&job)
		if err != nil {
			return err
		}
		if plan.Policy.Mode != "failed_step" || plan.Version != 2 || job.CurrentGenerationID == "" {
			return fmt.Errorf("step attempts require failed_step generation admission")
		}
		// 先验证不可变账本；历史/inspect 路径绝不获取共享 run/agent 锁
		// 消费者可能正持有共享 run 并等待本终态生产者的 job 锁
		// 只有下面真正申请执行的路径才在父 job 锁内验证当前 run/agent
		record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
		if err != nil {
			return err
		}
		if request.Scope.GenerationID != g.GenerationID || request.Scope.GenerationBindingDigest != g.BindingDigest || request.Scope.RuntimeClaimID != g.RuntimeClaimID {
			return fmt.Errorf("step transition generation scope mismatch")
		}
		chains, err := loadStepAttemptLedger(ctx, tx, &job, g, plan)
		if err != nil {
			return err
		}
		ci, ai := findStepAttemptChain(chains, request.Scope.StepRunID)
		if ci < 0 || !stepScopeMatches(request.Scope, &job, g, chains[ci].plan, ai) {
			return fmt.Errorf("step transition is outside frozen plan")
		}
		chain := &chains[ci]
		row := chain.rows[ai]
		// 任一已有逻辑准入固定全部资源，包括未发生失败的下一逻辑步骤
		var previousSequence uint64
		usedBoundaryNonces := map[string]bool{}
		for i := range chains {
			for j, a := range chains[i].admissions {
				if a == nil {
					continue
				}
				if a.Scope.Session != request.Scope.Session {
					return fmt.Errorf("original session resources changed")
				}
				if b := chains[i].requests[j].Boundary; b != nil {
					usedBoundaryNonces[b.BoundaryNonce] = true
					if b.BoundarySequence > previousSequence {
						previousSequence = b.BoundarySequence
					}
				}
			}
		}
		if admission := chain.admissions[ai]; admission != nil {
			if request.Operation != "inspect" && (row.AttemptAdmissionRequest != encoded || row.AttemptAdmissionRequestDigest != request.RequestDigest) {
				return fmt.Errorf("step admission request was already committed differently")
			}
			allowed := request.Operation != "inspect" && ai == chain.current && !model.IsJobRunTerminal(row.Status) && !model.IsJobRunTerminal(job.Status) && validateStepAttemptLive(ctx, tx, &job, record, g, admission, false) == nil && validateStepAttemptCommit(ctx, tx, &job, admission, request.Boundary) == nil
			for later := ci + 1; later < len(chains); later++ {
				if chains[later].current >= 0 {
					allowed = false
				}
			}
			result = &taskqueue.StepAttemptResult{Admission: admission, ExecutionAllowed: allowed}
			return nil
		}
		if request.Operation == "inspect" {
			return fmt.Errorf("step attempt has never been admitted")
		}
		if err := validateGenerationLive(ctx, tx, &job, request.Scope.Identity); err != nil {
			return err
		}
		if err := validateGenerationInputs(&job, request.Scope.InputManifestDigest, false); err != nil {
			return err
		}
		if err := validateRetryApprovalExecution(ctx, tx, &job, true); err != nil {
			return err
		}
		if record.State != "active" && record.State != "preparing" {
			return fmt.Errorf("step generation is not live")
		}
		for earlier := 0; earlier < ci; earlier++ {
			if !stepChainAllowsNext(&chains[earlier]) {
				return fmt.Errorf("earlier logical step has not committed its current successful or skipped outcome")
			}
		}
		for later := ci + 1; later < len(chains); later++ {
			if chains[later].current >= 0 {
				return fmt.Errorf("later logical step was already admitted")
			}
		}
		if row.Status < 1 || row.Status > 2 || row.ClaimID != "" || row.CancellationClaimID != "" || row.TerminalReportDigest != "" || row.GenerationID != "" || row.GenerationBindingDigest != "" || row.RetryableShellExit || !emptyStepAdmission(&row) {
			return fmt.Errorf("successor is no longer an untouched reserved attempt")
		}
		now := time.Now()
		notBefore := now.UnixMilli()
		expires := now.Add(taskqueue.StepAttemptAdmissionLifetime).UnixMilli()
		if request.Operation == "admit" {
			if ai != 0 || chain.current != -1 {
				return fmt.Errorf("initial admission is no longer available")
			}
		} else {
			if ai != chain.current+1 || chain.current < 0 {
				return fmt.Errorf("retry must target the immediate reserved successor")
			}
			failed := chain.rows[chain.current]
			prior := chain.admissions[chain.current]
			boundary := request.Boundary
			if prior == nil || failed.StepRunID != request.PreviousStepRunID || failed.ClaimID != request.PreviousStepClaimID || prior.AdmissionID != request.PreviousAdmissionID || prior.BindingDigest != request.PreviousAdmissionBindingDigest || failed.TerminalReportDigest != request.FailedStepReceiptDigest || failed.GenerationID != g.GenerationID || failed.GenerationBindingDigest != g.BindingDigest || !failed.RetryableShellExit || !allowedReportedFailure(failed, plan.Policy) || (failed.Uses != "shell" && failed.Uses != "builtin:shell") {
				return fmt.Errorf("retry requires exact current committed eligible shell failure")
			}
			if boundary == nil || usedBoundaryNonces[boundary.BoundaryNonce] || boundary.BoundarySequence <= previousSequence || boundary.CapturedUnixMilli > now.UnixMilli() || boundary.ExpiresUnixMilli <= now.UnixMilli() || boundary.CapturedUnixMilli < prior.NotBeforeUnixMilli {
				return fmt.Errorf("paused boundary is stale, future or reused")
			}
			delay, _ := time.ParseDuration(plan.Policy.Delay)
			notBefore = now.Add(delay).UnixMilli()
			expires = now.Add(delay + taskqueue.StepAttemptAdmissionLifetime).UnixMilli()
			if expires > boundary.ExpiresUnixMilli {
				expires = boundary.ExpiresUnixMilli
			}
		}
		deadline, err := stepAttemptDeadline(&job)
		if err != nil {
			return err
		}
		if deadline > 0 && expires > deadline {
			expires = deadline
		}
		approval, _, err := validateRetryApprovalBinding(ctx, tx, &job)
		if err != nil {
			return err
		}
		if approval != nil && expires > approval.ExpiresAt.UnixMilli() {
			expires = approval.ExpiresAt.UnixMilli()
		}
		if expires <= notBefore {
			return fmt.Errorf("original deadline or boundary expires before retry can begin")
		}
		admission := &taskqueue.StepAttemptAdmission{Version: 1, Scope: request.Scope, AdmissionID: uuid.NewString(), PreviousAdmissionID: request.PreviousAdmissionID, RequestDigest: request.RequestDigest, NotBeforeUnixMilli: notBefore, ExpiresUnixMilli: expires}
		admission.BindingDigest, err = admission.Digest()
		if err != nil {
			return err
		}
		body, err := admission.Canonical()
		if err != nil {
			return err
		}
		if err := reportedStatusResult(tx.Model(&model.StepRun{}).Where("step_run_id = ? AND job_run_id = ? AND status = ? AND claim_id = ? AND terminal_report_digest = ? AND cancellation_claim_id = ?", row.StepRunID, job.JobRunID, row.Status, "", "", "").Where("attempt_admission_request = ? AND attempt_admission_request_digest = ? AND attempt_admission = ?", "", "", "").Updates(map[string]any{"attempt_admission_request": encoded, "attempt_admission_request_digest": request.RequestDigest, "attempt_admission": body, "updated_at": time.Now()})); err != nil {
			return err
		}
		if err := validateStepAttemptCommit(ctx, tx, &job, admission, request.Boundary); err != nil {
			return err
		}
		result = &taskqueue.StepAttemptResult{Admission: admission, ExecutionAllowed: true}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateFailedStepReport(ctx context.Context, tx *gorm.DB, job *model.JobRun, step *model.StepRun, plan *taskqueue.JobRetryPlan, next int) error {
	proof := agentidentity.GenerationReportFromContext(ctx)
	binding := agentidentity.StepAttemptReportFromContext(ctx)
	planned := false
	policy, err := sonic.MarshalString(plan.Policy)
	if err != nil {
		return err
	}
	for _, item := range plan.Steps {
		if step.Attempt >= 1 && step.Attempt <= len(item.AttemptRunIDs) && item.AttemptRunIDs[step.Attempt-1] == step.StepRunID && item.Name == step.Name && int(item.StepIndex) == step.StepIndex && item.Uses == step.Uses && item.Action == step.Action && (step.ContinueOnError != 0) == item.ContinueOnError && step.RetryMode == "failed_step" && sameRetryPolicy(step.RetryPolicy, policy) {
			planned = true
			break
		}
	}
	if !planned {
		return fmt.Errorf("failed_step closure is outside the immutable plan")
	}
	if job.CurrentGenerationID == "" {
		if unusedGenerationClosure(next) && step.Status < 3 && step.ClaimID == "" && emptyStepAdmission(step) && proof.ID == "" && proof.BindingDigest == "" && binding == "" {
			return nil
		}
		return fmt.Errorf("initial generation and step admission are required")
	}
	record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
	if err != nil {
		return err
	}
	if proof.ID != g.GenerationID || proof.BindingDigest != g.BindingDigest || (record.State != "active" && record.State != "preparing") {
		return fmt.Errorf("failed_step report requires exact active original generation")
	}
	if err := validateInputExecution(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	chains, err := loadStepAttemptLedger(ctx, tx, job, g, plan)
	if err != nil {
		return err
	}
	ci, ai := findStepAttemptChain(chains, step.StepRunID)
	if ci < 0 {
		return fmt.Errorf("report step is outside failed_step plan")
	}
	chain := &chains[ci]
	current := &chain.rows[ai]
	if current.Status != step.Status || current.ClaimID != step.ClaimID || current.TerminalReportDigest != step.TerminalReportDigest || current.GenerationID != step.GenerationID || current.GenerationBindingDigest != step.GenerationBindingDigest || !sameStepAdmission(current, step) {
		return fmt.Errorf("failed_step report snapshot changed")
	}
	admission := chain.admissions[ai]
	if admission == nil {
		if !unusedGenerationClosure(next) || current.Status < 1 || current.Status > 2 || current.ClaimID != "" || current.GenerationID != "" || current.GenerationBindingDigest != "" || binding != "" {
			return fmt.Errorf("unadmitted attempt permits only unused nonexecution closure")
		}
		return nil
	}
	if ai != chain.current || admission.BindingDigest != binding {
		return fmt.Errorf("only exact current admitted step may report new effects")
	}
	for later := ci + 1; later < len(chains); later++ {
		if chains[later].current >= 0 {
			return fmt.Errorf("later logical step already owns execution")
		}
	}
	if current.GenerationID != "" && (current.GenerationID != g.GenerationID || current.GenerationBindingDigest != g.BindingDigest) {
		return fmt.Errorf("admitted step generation changed")
	}
	if next == model.JobRunStatusSkipped && (ai != 0 || current.Status == model.JobRunStatusRunning || current.ClaimID != "") {
		return fmt.Errorf("only an untouched admitted first attempt may condition-skip")
	}
	if next == model.JobRunStatusSuccess && (current.Status != model.JobRunStatusRunning || current.ClaimID != admission.Scope.StepClaimID) {
		return fmt.Errorf("successful step receipt requires the exact admitted RUNNING claim")
	}
	if next == model.JobRunStatusRunning {
		return validateStepAttemptLive(ctx, tx, job, record, g, admission, true)
	}
	// 准入后未启动的拒绝、超时或取消可结束自身，绝不扩展过期准入的执行许可
	return nil
}

func validateFailedStepCancellation(ctx context.Context, tx *gorm.DB, job *model.JobRun, step *model.StepRun, plan *taskqueue.JobRetryPlan, g *taskqueue.JobGeneration) error {
	chains, err := loadStepAttemptLedger(ctx, tx, job, g, plan)
	if err != nil {
		return err
	}
	ci, ai := findStepAttemptChain(chains, step.StepRunID)
	if ci < 0 {
		return fmt.Errorf("cancelled step is outside failed_step plan")
	}
	row := &chains[ci].rows[ai]
	admission := chains[ci].admissions[ai]
	binding := agentidentity.StepAttemptReportFromContext(ctx)
	if !sameStepAdmission(row, step) || row.Status != step.Status || row.ClaimID != step.ClaimID {
		return fmt.Errorf("cancelled step admission snapshot changed")
	}
	if admission == nil {
		if binding != "" || step.ClaimID != "" || step.GenerationID != "" || step.GenerationBindingDigest != "" {
			return fmt.Errorf("unused cancelled attempt cannot claim admission")
		}
		return nil
	}
	if ai != chains[ci].current || binding != admission.BindingDigest {
		return fmt.Errorf("cancelled attempt must match its current immutable admission")
	}
	if step.ClaimID != "" && step.ClaimID != admission.Scope.StepClaimID {
		return fmt.Errorf("cancelled step original claim changed")
	}
	if step.GenerationID != "" && (step.GenerationID != g.GenerationID || step.GenerationBindingDigest != g.BindingDigest) {
		return fmt.Errorf("cancelled step original generation changed")
	}
	return nil
}

func validateFailedStepSuccess(ctx context.Context, tx *gorm.DB, job *model.JobRun, g *taskqueue.JobGeneration, plan *taskqueue.JobRetryPlan) error {
	chains, err := loadStepAttemptLedger(ctx, tx, job, g, plan)
	if err != nil {
		return err
	}
	for index := range chains {
		chain := &chains[index]
		if !stepChainAllowsNext(chain) {
			return fmt.Errorf("job success requires every current admitted logical step success or condition skip")
		}
		for attempt, row := range chain.rows {
			if !model.IsJobRunTerminal(row.Status) || artifactutil.Digest(row.TerminalReportDigest) != nil || row.ClaimID != agentidentity.StepClaimID(job.ClaimID, row.StepRunID) || row.GenerationID != g.GenerationID || row.GenerationBindingDigest != g.BindingDigest {
				return fmt.Errorf("job success requires all exact attempt closure receipts")
			}
			if attempt > chain.current && (chain.admissions[attempt] != nil || row.Status != model.JobRunStatusSkipped || row.RetryableShellExit) {
				return fmt.Errorf("unused future attempts cannot become execution winners")
			}
		}
	}
	return nil
}

func validateFailedStepArtifact(ctx context.Context, tx *gorm.DB, job *model.JobRun, stepID string) error {
	plan, err := retryPlanForJob(job)
	if err != nil {
		return err
	}
	_, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
	if err != nil {
		return err
	}
	chains, err := loadStepAttemptLedger(ctx, tx, job, g, plan)
	if err != nil {
		return err
	}
	ci, ai := findStepAttemptChain(chains, stepID)
	if ci < 0 || ai != chains[ci].current {
		return fmt.Errorf("artifact must come from current admitted logical upload winner")
	}
	chain := &chains[ci]
	row := chain.rows[ai]
	admission := chain.admissions[ai]
	if admission == nil || row.Status != model.JobRunStatusSuccess || artifactutil.Digest(row.TerminalReportDigest) != nil || row.ClaimID != admission.Scope.StepClaimID || row.GenerationID != g.GenerationID || row.GenerationBindingDigest != g.BindingDigest || row.Uses != "builtin:artifacts" || row.Action != "upload" {
		return fmt.Errorf("artifact requires exact successful admitted upload receipt")
	}
	return nil
}

// 写入之后重新检查不可续期的启动界限，过期必须回滚而不是留下可重放的新准入
func validateStepAttemptCommit(ctx context.Context, tx *gorm.DB, job *model.JobRun, admission *taskqueue.StepAttemptAdmission, boundary *taskqueue.PausedStepBoundary) error {
	if err := validateRetryApprovalCommit(ctx, tx, job, model.JobRunStatusRunning); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	deadline, err := stepAttemptDeadline(job)
	if err != nil {
		return err
	}
	if admission == nil || now >= admission.ExpiresUnixMilli || (deadline > 0 && now >= deadline) || (boundary != nil && now >= boundary.ExpiresUnixMilli) {
		return fmt.Errorf("step admission start bound expired during commit")
	}
	return nil
}
