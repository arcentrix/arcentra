// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"gorm.io/gorm"
)

var ErrCopilotRerunUnavailable = errors.New("whole-job re-execution persistence unavailable")

// CopilotDefinitionRepositoryConfigDigest binds the credential-free configuration
// actually supplied to the loader. It is not proof that loading occurred and is
// not a credential/secret version: those remain separate live checks. Only the
// trusted service loader may construct the encrypted resolved-content envelope.
func CopilotDefinitionRepositoryConfigDigest(pipeline *model.Pipeline, project *model.Project) (string, error) {
	if pipeline == nil || project == nil || !historyID(pipeline.PipelineID) || !historyID(project.ProjectID) || pipeline.ProjectID != project.ProjectID || !rerunDefinitionPath(pipeline.PipelineFilePath) || pipeline.DefaultBranch == "" || len(pipeline.DefaultBranch) > 255 || strings.TrimSpace(pipeline.DefaultBranch) != pipeline.DefaultBranch || strings.ContainsAny(pipeline.DefaultBranch, "\x00\r\n") {
		return "", ErrCopilotRerunInvalid
	}
	repository, err := sourceprovenance.CanonicalRepository(pipeline.RepoURL)
	if err != nil {
		return "", ErrCopilotRerunInvalid
	}
	projectRepository := ""
	if project.RepoURL != "" {
		projectRepository, err = sourceprovenance.CanonicalRepository(project.RepoURL)
		if err != nil {
			return "", ErrCopilotRerunInvalid
		}
	}
	if len(project.RepoType) > 32 || strings.TrimSpace(project.RepoType) != project.RepoType || strings.ContainsAny(project.RepoType, "\x00\r\n") {
		return "", ErrCopilotRerunInvalid
	}
	return rerunDigest("arcentra-copilot-definition-repository-config-v1", []any{pipeline.PipelineID, pipeline.ProjectID, project.OrgID, pipeline.RepoURL, repository, pipeline.DefaultBranch, pipeline.PipelineFilePath, project.RepoURL, projectRepository, project.RepoType}), nil
}

// IVerifiedManualDefinitionCreator is deliberately separate from the old API.
// Callers must supply a prospective, finalized encrypted definition produced by
// the verified loader, never a historical run or browser-selected ciphertext.
// This interface is not wired to a route or enabled by the value foundation.
type IVerifiedManualDefinitionCreator interface {
	CreateVerifiedManualTriggerRunWithDefinition(context.Context, string, *model.PipelineRun, *model.CopilotDefinitionSnapshot, VerifiedManualTriggerAuthorize) error
}

func (r *PipelineRepo) CreateVerifiedManualTriggerRunWithDefinition(ctx context.Context, actor string, run *model.PipelineRun, snapshot *model.CopilotDefinitionSnapshot, authorize VerifiedManualTriggerAuthorize) error {
	if snapshot == nil || ValidateCopilotDefinitionSnapshot(snapshot) != nil {
		return ErrCopilotRerunInvalid
	}
	copy := *snapshot
	copy.PayloadCiphertext = append([]byte(nil), snapshot.PayloadCiphertext...)
	err := r.createVerifiedManualTriggerRun(ctx, actor, run, authorize, &copy)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrManualTriggerForbidden) || errors.Is(err, ErrManualTriggerConflict) || errors.Is(err, ErrCopilotRerunInvalid) {
		return err
	}
	return ErrCopilotRerunUnavailable
}

// Called only inside the original creator transaction, after scope/actor checks
// and before any run/snapshot writes. No independently committing backfill API.
func validateCopilotDefinitionCapture(snapshot *model.CopilotDefinitionSnapshot, run *model.PipelineRun, pipeline *model.Pipeline, project *model.Project, now time.Time) error {
	if ValidateCopilotDefinitionSnapshot(snapshot) != nil || run == nil || pipeline == nil || project == nil || snapshot.ID != 0 || run.TotalJobs != 1 || run.AllowDeployments || run.DeploymentActorID != "" || run.DeploymentPlan != "" || run.DeploymentPlanHash != "" || run.DeploymentTargetFingerprint != "" || snapshot.CreatedAt.After(now) || !now.Before(snapshot.ExpiresAt) || snapshot.RunID != run.RunID || snapshot.PipelineID != run.PipelineID || snapshot.ProjectID != project.ProjectID || run.DefinitionCommitSha != snapshot.DefinitionRevision || run.DefinitionPath != snapshot.DefinitionPath || run.Branch != pipeline.DefaultBranch || run.RetryApprovalContext != snapshot.RetryApprovalContext || run.RetryApprovalContextDigest != snapshot.RetryApprovalContextDigest {
		return ErrCopilotRerunInvalid
	}
	if strings.EqualFold(snapshot.SnapshotID, run.TriggeredBy) {
		return ErrCopilotRerunInvalid
	}
	digest, err := CopilotDefinitionRepositoryConfigDigest(pipeline, project)
	if err != nil || digest != snapshot.RepositoryConfigDigest {
		return ErrCopilotRerunInvalid
	}
	repository, err := sourceprovenance.CanonicalRepository(pipeline.RepoURL)
	if err != nil || repository != snapshot.DefinitionRepository || pipeline.PipelineFilePath != snapshot.DefinitionPath {
		return ErrCopilotRerunInvalid
	}
	variables, err := CopilotRerunTriggerVariablesDigest(run.Env)
	if err != nil || variables != snapshot.TriggerVariablesDigest {
		return ErrCopilotRerunInvalid
	}
	return nil
}

func insertCopilotDefinitionSnapshotTx(tx *gorm.DB, snapshot *model.CopilotDefinitionSnapshot) error {
	if snapshot == nil {
		return nil
	}
	// Exact unique database constraints are mandatory; UUID case aliases to old
	// run/pipeline/project identities are refused before inserting any new row.
	for _, id := range []string{snapshot.RunID, snapshot.PipelineID, snapshot.ProjectID} {
		if strings.EqualFold(snapshot.SnapshotID, id) {
			return ErrCopilotRerunInvalid
		}
	}
	if err := tx.Create(snapshot).Error; err != nil {
		return ErrCopilotRerunUnavailable
	}
	return nil
}
