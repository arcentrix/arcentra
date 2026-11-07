// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rerunGateLockProof struct {
	operation                        executionLockMode
	pool                             gorm.ConnPool
	gate, run, job, intent, schedule string
}

const rerunGateLockProofKey = "arcentra.copilot.rerun.private-gate-lock"

func lockCopilotRerunTargetGate(ctx context.Context, tx *gorm.DB, intent *model.CopilotRerunIntent) (*model.CopilotRerunAdmissionGate, error) {
	return lockCopilotRerunTargetGateMode(ctx, tx, intent, false)
}
func lockCopilotRerunTargetGateMode(ctx context.Context, tx *gorm.DB, intent *model.CopilotRerunIntent, allowHistorical bool) (*model.CopilotRerunAdmissionGate, error) {
	return lockCopilotRerunGateForOperation(ctx, tx, intent, allowHistorical, executionLockRerunSeal)
}
func lockCopilotRerunGateForOperation(ctx context.Context, tx *gorm.DB, intent *model.CopilotRerunIntent, allowHistorical bool, operation executionLockMode) (*model.CopilotRerunAdmissionGate, error) {
	if operation != executionLockRerunSeal && operation != executionLockRerunClaim {
		return nil, ErrCopilotRerunInvalid
	}
	if tx == nil || tx.Statement == nil || intent == nil {
		return nil, ErrCopilotRerunInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, ErrCopilotRerunInvalid
	}
	var rows []model.CopilotRerunAdmissionGate
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("gate_id = ?", intent.GateID).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrCopilotRerunConflict
	}
	gate := &rows[0]
	if ValidateCopilotRerunGate(gate) != nil || gate.GateID != intent.GateID || gate.ScheduleID != intent.ScheduleID || gate.RunID != intent.NewRunID || gate.JobRunID != intent.NewJobRunID || gate.ProjectID != intent.ProjectID || gate.PipelineID != intent.PipelineID || (gate.State != "unclaimed" && !(allowHistorical && (gate.State == "closed" || gate.State == "claimed")) && !(operation == executionLockRerunClaim && gate.State == "claimed")) {
		return nil, ErrCopilotRerunConflict
	}
	tx.Statement.Settings.Store(rerunGateLockProofKey, rerunGateLockProof{operation, tx.Statement.ConnPool, gate.GateID, gate.RunID, gate.JobRunID, intent.IntentID, gate.ScheduleID})
	return gate, nil
}
func rerunGateProofMatches(tx *gorm.DB, gate, run, job, intent string) bool {
	return rerunGateProofMatchesOperation(tx, executionLockRerunSeal, gate, run, job, intent)
}
func rerunGateProofMatchesOperation(tx *gorm.DB, operation executionLockMode, gate, run, job, intent string) bool {
	if operation != executionLockRerunSeal && operation != executionLockRerunClaim {
		return false
	}
	if tx == nil || tx.Statement == nil {
		return false
	}
	value, ok := tx.Statement.Settings.Load(rerunGateLockProofKey)
	proof, valid := value.(rerunGateLockProof)
	return ok && valid && proof.operation == operation && proof.pool == tx.Statement.ConnPool && proof.gate == gate && proof.run == run && proof.job == job && proof.intent == intent && rerunUUID(gate) && rerunUUID(intent) && rerunUUID(proof.schedule)
}
