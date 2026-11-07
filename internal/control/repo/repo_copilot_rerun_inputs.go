// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
)

// GetOrRecordJobInputsWithSourceExpectation is the protected existing-claim
// preparation path. An expectation is never used to synthesize an observation.
// Like ordinary inputs, this checks the enabled original Agent and execution
// identity, not current human requester/candidate permissions. After requester
// revocation it may return only the expected SHA or create a pending approval.
// Neither result admits commands: the protected startup adapter must separately
// apply existing live requester/candidate/policy and approval admission checks.
func (r *JobRunRepo) GetOrRecordJobInputsWithSourceExpectation(ctx context.Context, identity taskqueue.JobInputIdentity, expectationDigest string, proposed *taskqueue.JobInputManifest) (*model.JobRun, *taskqueue.SourceCheckoutExpectation, error) {
	var result model.JobRun
	var expectation *taskqueue.SourceCheckoutExpectation
	err := executionTransaction(ctx, r.Database(), identity.JobRunID, func(tx *gorm.DB) error {
		current, intent, schedule, err := lockCopilotRerunInputOperation(ctx, tx, identity)
		if err != nil {
			return err
		}
		expectation = &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: schedule.ScheduleID, SealID: *schedule.SealID, ExpectedSourceCommit: current.ExpectedSourceCommit}
		digest, err := expectation.Digest(identity)
		if err != nil || digest != expectationDigest || expectation.ValidateObservation(proposed) != nil {
			return ErrCopilotRerunConflict
		}
		if err := requireCopilotRerunInputProof(tx, current); err != nil {
			return err
		}
		if err := recordJobInputsLocked(ctx, tx, current, identity, proposed, &result); err != nil {
			return err
		}
		return finishCopilotRerunInputOperation(ctx, tx, &result, intent, schedule)
	})
	if err != nil {
		return nil, nil, rerunError(err)
	}
	return &result, expectation, nil
}
