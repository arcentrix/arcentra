// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"slices"
)

func (e *Process) validateRetryApprovalRun(run *model.PipelineRun, parsed *spec.Pipeline) (bool, error) {
	names, err := validation.RetryApprovalJobNames(parsed, e.offlineRuntimePolicy)
	if err != nil {
		return false, err
	}
	if len(names) == 0 {
		if run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
			return false, fmt.Errorf("retry approval gate was removed")
		}
		return false, nil
	}
	captured, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return false, fmt.Errorf("verified retry approval context required: %w", err)
	}
	digest, err := captured.Digest()
	if err != nil {
		return false, err
	}
	definition, err := validation.ResolvedDefinitionDigest(parsed)
	if err != nil {
		return false, err
	}
	slices.Sort(names)
	if run.TriggerType != 1 || run.TriggeredBy != captured.ActorID || run.RunID != captured.PipelineRunID || run.PipelineID != captured.PipelineID || run.DefinitionCommitSha != captured.DefinitionCommitSHA || run.DefinitionPath != captured.DefinitionPath || definition != captured.ResolvedDefinitionDigest || digest != run.RetryApprovalContextDigest || !slices.Equal(names, captured.GatedJobs) {
		return false, fmt.Errorf("immutable retry approval run identity mismatch")
	}
	if e.appConf == nil || e.appConf.Pipeline.RetryApproval == nil {
		return false, fmt.Errorf("trusted retry approval policy unavailable")
	}
	configured := *e.appConf.Pipeline.RetryApproval
	configured.Candidates = slices.Clone(configured.Candidates)
	slices.Sort(configured.Candidates)
	fingerprint, err := configured.Digest()
	if err != nil || fingerprint != captured.Policy.Fingerprint {
		return false, fmt.Errorf("retry approval operator policy changed")
	}
	return true, nil
}
