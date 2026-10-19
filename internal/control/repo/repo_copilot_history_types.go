// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"time"
)

var (
	ErrCopilotHistoryInvalid     = errors.New("invalid history request")
	ErrCopilotHistoryUnavailable = errors.New("history unavailable")
	ErrCopilotHistoryNotFound    = errors.New("history unavailable or not found")
	ErrCopilotHistoryForbidden   = errors.New("current history access denied")
	ErrCopilotHistoryConflict    = errors.New("history request conflict")
	ErrCopilotHistoryCapacity    = errors.New("history capacity reached")
)

const (
	CopilotHistoryBodyTTL       = 30 * 24 * time.Hour
	CopilotHistoryPlanTTL       = 24 * time.Hour
	CopilotHistoryTombstoneTTL  = 90 * 24 * time.Hour
	CopilotHistoryMaxCiphertext = 128 * 1024
	CopilotHistoryMaxPageBytes  = 256 * 1024
	CopilotHistoryMaxTurns      = 200
	CopilotHistoryMaxPlans      = 100
)

// Authorize receives a primary transaction-scoped, locked view. It must validate
// enabled human identity and live project:read and (when nonempty) pipeline:read.
// It must not contact providers/SCM or start transactions. A nil project requests
// only actor validation. The repository always obtains that exact actor lock.
type CopilotHistoryAuthorize func(context.Context, *Repositories, string, *model.Project, string) error

type CopilotHistoryScope struct{ OwnerUserID, ProjectID, PipelineID, RunID string }
type CopilotHistoryConversationPage struct {
	List           []*model.CopilotConversation
	NextCursor     string
	ScanIncomplete bool
}
type CopilotHistoryTurnPage struct {
	List      []*model.CopilotTurn
	NextAfter uint64
}
type CopilotHistoryPlanPage struct {
	List      []*model.CopilotActionPlan
	NextAfter uint64
}
type CopilotHistoryReserveInput struct {
	OwnerUserID, ProjectID, ConversationID, TurnID, RequestID                                   string
	ConversationVersion                                                                         uint64
	ProviderID, Model, ProviderBinding, InputFingerprint, ContextDigest, RedactionPolicyVersion string
	PayloadCiphertext                                                                           []byte
	CipherVersion                                                                               uint32
	LeaseUntil                                                                                  time.Time
}
type CopilotHistoryDispatchInput struct {
	OwnerUserID, ProjectID, ConversationID, TurnID string
	Version                                        uint64
	PayloadCiphertext                              []byte
	CipherVersion                                  uint32
	LeaseUntil                                     time.Time
}
type CopilotHistoryFinalizeInput struct {
	OwnerUserID, ProjectID, ConversationID, TurnID string
	Version                                        uint64
	State, ErrorCode                               string
	PayloadCiphertext                              []byte
	CipherVersion                                  uint32
}
type CopilotHistoryPlanInput struct {
	OwnerUserID, ProjectID, ConversationID, TurnID, PlanID, RequestID, Kind, PlanDigest string
	ConversationVersion, TurnVersion                                                    uint64
	SchemaVersion, CipherVersion                                                        uint32
	PayloadCiphertext                                                                   []byte
}
type CopilotHistoryPlanTransitionInput struct {
	OwnerUserID, ProjectID, ConversationID, PlanID, PlanDigest string
	Version                                                    uint64
	State, ReasonCode                                          string
}
