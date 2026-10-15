// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var jobInputColumns = []string{"input_expectation", "input_manifest_state", "input_manifest", "input_manifest_digest"}

func inputField(key string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(key), "_", ""), "input")
}
func emptyJobInputs(j *model.JobRun) bool {
	return j.InputExpectation == "" && j.InputManifest == "" && j.InputManifestDigest == "" && (j.InputManifestState == "" || j.InputManifestState == "unknown")
}
func jobInputsEqual(a, b *model.JobRun) bool {
	return a.InputExpectation == b.InputExpectation && a.InputManifestState == b.InputManifestState && a.InputManifest == b.InputManifest && a.InputManifestDigest == b.InputManifestDigest
}

// CaptureRuntimeInputExpectation 原子固定控制面选定的完整制品输入及运行时派发身份
func (r *JobRunRepo) CaptureRuntimeInputExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, source *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference) error {
	if runtime == nil {
		return fmt.Errorf("runtime expectation is required")
	}
	copy := *runtime
	digest, err := taskqueue.RuntimeBindingDigest(pipelineID, runID, jobID, agentID, &copy)
	if err != nil || digest != copy.BindingDigest {
		return fmt.Errorf("invalid runtime dispatch binding")
	}
	inputs, err := taskqueue.NewJobInputManifest("", uris, refs)
	if err != nil {
		return err
	}
	return r.captureDispatchExpectation(ctx, pipelineID, runID, jobID, agentID, source, &copy, inputs)
}

// GetOrRecordJobInputs 在同一事务内校验活动执行身份，首次固定输入，重复请求只读取原值
func (r *JobRunRepo) GetOrRecordJobInputs(ctx context.Context, identity taskqueue.JobInputIdentity, proposed *taskqueue.JobInputManifest) (*model.JobRun, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	var result model.JobRun
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", identity.JobRunID).First(&current).Error; err != nil {
			return err
		}
		if err := validateInputExecution(ctx, tx, &current, identity); err != nil {
			return err
		}
		expected, err := taskqueue.ParseJobInputManifest(current.InputExpectation)
		if err != nil || expected.SourceCommit != "" {
			return fmt.Errorf("immutable dispatched inputs are unknown")
		}
		if err := validateRegisteredInputArtifacts(ctx, tx, &current, expected); err != nil {
			return err
		}
		if current.InputManifestState == "pinned" {
			stored, err := taskqueue.ParseJobInputManifest(current.InputManifest)
			if err != nil {
				return err
			}
			digest, err := stored.Digest(identity)
			if err != nil || digest != current.InputManifestDigest {
				return fmt.Errorf("stored input manifest is invalid")
			}
			if err := validateInputProposal(&current, stored); err != nil {
				return err
			}
			if proposed != nil {
				encoded, err := proposed.Canonical()
				if err != nil || encoded != current.InputManifest {
					return fmt.Errorf("input manifest is already pinned to different inputs")
				}
			}
			if err := pinRetryApproval(ctx, tx, &current); err != nil {
				return err
			}
			result = current
			return nil
		}
		if current.InputManifestState != "unknown" || current.InputManifest != "" || current.InputManifestDigest != "" {
			return fmt.Errorf("input manifest state is invalid")
		}
		if proposed == nil {
			result = current
			return nil
		}
		if err := validateInputProposal(&current, proposed); err != nil {
			return err
		}
		encoded, err := proposed.Canonical()
		if err != nil {
			return err
		}
		digest, err := proposed.Digest(identity)
		if err != nil {
			return err
		}
		updates := map[string]any{"input_manifest_state": "pinned", "input_manifest": encoded, "input_manifest_digest": digest, "updated_at": time.Now()}
		if err := reportedStatusResult(reportedJobQuery(tx.Model(&model.JobRun{}), &current).Updates(updates)); err != nil {
			return err
		}
		current.InputManifestState, current.InputManifest, current.InputManifestDigest = "pinned", encoded, digest
		if err := pinRetryApproval(ctx, tx, &current); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func validateInputProposal(job *model.JobRun, manifest *taskqueue.JobInputManifest) error {
	if manifest == nil {
		return fmt.Errorf("input manifest is required")
	}
	if job.SourceBindingDigest != "" {
		if !sourceprovenance.ValidCommit(manifest.SourceCommit) {
			return fmt.Errorf("full initial source commit is required")
		}
	} else if manifest.SourceCommit != "" {
		return fmt.Errorf("source-free job cannot acquire a source commit")
	}
	copy := *manifest
	copy.SourceCommit = ""
	encoded, err := copy.Canonical()
	if err != nil || encoded != job.InputExpectation {
		return fmt.Errorf("input artifacts do not match immutable dispatch expectation")
	}
	return nil
}

func validateInputExecution(ctx context.Context, tx *gorm.DB, job *model.JobRun, identity taskqueue.JobInputIdentity) error {
	if job.JobRunID != identity.JobRunID || job.PipelineID != identity.PipelineID || job.PipelineRunID != identity.PipelineRunID || job.AgentID != identity.AgentID || job.ClaimID != identity.ClaimID || job.Status != model.JobRunStatusRunning || job.CancellationClaimID != "" || job.TerminalReportDigest != "" || !job.SourceExpectationCaptured || job.RuntimeBindingDigest != identity.RuntimeBindingDigest || job.SourceBindingDigest != identity.SourceBindingDigest {
		return fmt.Errorf("input operation requires the original active execution")
	}
	runtime := &taskqueue.RuntimeRequest{Type: job.RuntimeType, Image: job.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: job.RuntimeCPU, Memory: job.RuntimeMemory}}
	digest, err := taskqueue.RuntimeBindingDigest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, runtime)
	if err != nil || digest != identity.RuntimeBindingDigest {
		return fmt.Errorf("stored runtime binding is invalid")
	}
	if job.SourceBindingDigest != "" {
		binding, err := sourceprovenance.NewBinding(job.PipelineID, job.PipelineRunID, job.JobRunID, job.SourceType, job.SourceRepository, job.SourceRef)
		if err != nil || binding.Type != "git" || binding.Digest != identity.SourceBindingDigest {
			return fmt.Errorf("stored source binding is invalid")
		}
	} else if job.SourceType != "" || job.SourceRepository != "" || job.SourceRef != "" {
		return fmt.Errorf("stored source absence is invalid")
	}
	var run model.PipelineRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id", "pipeline_id", "status").Where("run_id = ?", identity.PipelineRunID).First(&run).Error; err != nil {
		return err
	}
	if run.RunID != identity.PipelineRunID || run.PipelineID != identity.PipelineID || run.Status != model.PipelineStatusRunning {
		return fmt.Errorf("input operation requires a running pipeline")
	}
	var agents []model.Agent
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("agent_id", "is_enabled").Where("agent_id = ?", identity.AgentID).Limit(2).Find(&agents).Error; err != nil {
		return err
	}
	if len(agents) != 1 || agents[0].AgentID != identity.AgentID || agents[0].IsEnabled != 1 {
		return fmt.Errorf("input operation requires an enabled original agent")
	}
	return nil
}

func validateRegisteredInputArtifacts(ctx context.Context, tx *gorm.DB, job *model.JobRun, inputs *taskqueue.JobInputManifest) error {
	if _, err := inputs.Canonical(); err != nil {
		return err
	}
	if len(inputs.Artifacts) == 0 {
		return nil
	}
	var pipeline model.Pipeline
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id", "project_id").Where("pipeline_id = ? AND is_deleted = ?", job.PipelineID, 0).First(&pipeline).Error; err != nil {
		return err
	}
	if pipeline.PipelineID != job.PipelineID || pipeline.ProjectID == "" {
		return fmt.Errorf("input project identity is unavailable")
	}
	var projects []model.Project
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id").Where("project_id = ?", pipeline.ProjectID).Limit(2).Find(&projects).Error; err != nil {
		return err
	}
	if len(projects) != 1 || projects[0].ProjectID != pipeline.ProjectID {
		return fmt.Errorf("input project identity is unavailable")
	}
	for _, file := range inputs.Artifacts {
		var records []model.ProjectArtifact
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "pipeline_id", "pipeline_run_id", "job_run_id", "step_run_id", "name", "storage_id", "storage_fingerprint", "storage_path", "sha256", "size", "expires_at").Where("pipeline_run_id = ? AND storage_path = ?", job.PipelineRunID, file.Key).Limit(2).Find(&records).Error; err != nil {
			return err
		}
		if len(records) != 1 {
			return fmt.Errorf("input artifact has no unique registered producer")
		}
		artifact := records[0]
		expiry := int64(0)
		if artifact.ExpiresAt != nil {
			expiry = artifact.ExpiresAt.Unix()
		}
		key, err := artifactutil.RemotePath(job.PipelineRunID, artifact.JobRunID, artifact.StepRunID, artifact.SHA256, artifact.Name)
		if err != nil || artifact.ProjectID != pipeline.ProjectID || artifact.PipelineID != job.PipelineID || artifact.PipelineRunID != job.PipelineRunID || artifact.JobRunID == job.JobRunID || artifact.StoragePath != file.Key || key != file.Key || artifact.StorageID != file.Reference.StorageID || artifact.StorageFingerprint != file.Reference.Fingerprint || artifact.SHA256 != file.Reference.SHA256 || artifact.Size != *file.Reference.Size || expiry != file.Reference.ExpiresAt || (expiry > 0 && expiry <= time.Now().Unix()) {
			return fmt.Errorf("input artifact does not match a live registered producer")
		}
		var producer model.JobRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "job_name", "pipeline_id", "pipeline_run_id", "status", "terminal_report_digest").Where("job_run_id = ?", artifact.JobRunID).First(&producer).Error; err != nil {
			return err
		}
		if artifactutil.Name(producer.JobName) != nil || file.Name != producer.JobName+"/"+artifact.Name || producer.JobRunID != artifact.JobRunID || producer.PipelineID != job.PipelineID || producer.PipelineRunID != job.PipelineRunID || producer.Status != model.JobRunStatusSuccess {
			return fmt.Errorf("input producer is not a successful job in this run")
		}
		var step model.StepRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "job_run_id", "pipeline_id", "pipeline_run_id", "status").Where("step_run_id = ?", artifact.StepRunID).First(&step).Error; err != nil {
			return err
		}
		if step.StepRunID != artifact.StepRunID || step.JobRunID != artifact.JobRunID || step.PipelineID != job.PipelineID || step.PipelineRunID != job.PipelineRunID || step.Status != model.JobRunStatusSuccess {
			return fmt.Errorf("input producer step does not match registered output")
		}
	}
	return nil
}

func validatePinnedSourceUpdates(job *model.JobRun, updates map[string]any) error {
	next, _ := updates["status"].(int)
	if job.InputExpectation != "" && next == model.JobRunStatusSuccess && job.InputManifestState != "pinned" {
		return fmt.Errorf("success requires pinned container inputs")
	}
	if job.InputManifestState != "pinned" {
		return nil
	}
	manifest, err := taskqueue.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		return err
	}
	identity := taskqueue.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	digest, err := manifest.Digest(identity)
	if err != nil || digest != job.InputManifestDigest {
		return fmt.Errorf("stored input manifest identity is invalid")
	}
	if err := validateInputProposal(job, manifest); err != nil {
		return err
	}
	state, _ := updates["source_provenance_state"].(string)
	commit, _ := updates["source_resolved_commit"].(string)
	if (state == "reported" || commit != "") && (commit != manifest.SourceCommit || commit == "") {
		return fmt.Errorf("terminal source result contradicts pinned input")
	}
	if next == model.JobRunStatusSuccess && manifest.SourceCommit != "" && (state != "reported" || commit != manifest.SourceCommit) {
		return fmt.Errorf("successful source result must match pinned input")
	}
	return nil
}
