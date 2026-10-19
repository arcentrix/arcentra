// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"errors"
	"time"
)

var (
	ErrCopilotHistoryUnavailable = errors.New("history_unavailable")
	ErrCopilotHistoryContent     = errors.New("history_content_unavailable")
	ErrCopilotHistoryConflict    = errors.New("history_conflict")
	ErrCopilotHistoryTooLarge    = errors.New("selected_history_too_large")
	ErrCopilotPlanUnavailable    = errors.New("plan_unavailable")
)

const copilotHistoryDays = 30
const copilotHistoryMaxTurns = 6
const copilotHistoryMaxBytes = 8192
const copilotHistorySchema = 1

type CopilotCreateConversationRequest struct {
	RunID                string `json:"runId"`
	RequestID            string `json:"requestId"`
	AcknowledgeRetention bool   `json:"acknowledgeRetention"`
}
type CopilotConversation struct {
	ConversationID string    `json:"conversationId"`
	ProjectID      string    `json:"projectId"`
	PipelineID     string    `json:"pipelineId"`
	RunID          string    `json:"runId"`
	State          string    `json:"state"`
	Version        uint64    `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
}
type CopilotConversationList struct {
	Conversations []CopilotConversation `json:"conversations"`
	NextCursor    string                `json:"nextCursor,omitempty"`
}
type CopilotConversationDetail struct {
	Conversation      CopilotConversation `json:"conversation"`
	Turns             []CopilotTurn       `json:"turns"`
	NextAfterSequence uint64              `json:"nextAfterSequence,omitempty"`
}
type CopilotTurn struct {
	ContentAvailableUntil *time.Time        `json:"contentAvailableUntil,omitempty"`
	TurnID                string            `json:"turnId"`
	RequestID             string            `json:"requestId"`
	Sequence              uint64            `json:"sequence"`
	State                 string            `json:"state"`
	Version               uint64            `json:"version"`
	Question              string            `json:"question,omitempty"`
	Text                  string            `json:"text,omitempty"`
	Evidence              []CopilotEvidence `json:"evidence"`
	Warnings              []string          `json:"warnings"`
	ContentAvailable      bool              `json:"contentAvailable"`
	UnavailableReason     string            `json:"unavailableReason,omitempty"`
	CreatedAt             time.Time         `json:"createdAt"`
	ExpiresAt             time.Time         `json:"expiresAt"`
}
type CopilotHistoryTurnRequest struct {
	CopilotRequest
	HistoryTurnIDs []string `json:"historyTurnIds"`
}
type CopilotHistoryPreview struct {
	CopilotPreview
	HistoryTurnIDs []string `json:"historyTurnIds"`
	HistoryBytes   int      `json:"historyBytes"`
}

type CopilotPlanRequest struct {
	RequestID     string                        `json:"requestId"`
	TurnID        string                        `json:"turnId"`
	Kind          string                        `json:"kind"`
	CodePatch     *CopilotCodePatchInput        `json:"codePatch,omitempty"`
	RetryApproval *CopilotRetryReviewInput      `json:"retryApproval,omitempty"`
	Deployment    *CopilotDeploymentReviewInput `json:"deployment,omitempty"`
}
type CopilotCodePatchInput struct {
	ArtifactID string             `json:"artifactId,omitempty"`
	SourceFile *CopilotSourceFile `json:"sourceFile,omitempty"`
	Patch      string             `json:"patch"`
}
type CopilotRetryReviewInput struct {
	ApprovalID string `json:"approvalId"`
}
type CopilotDeploymentReviewInput struct {
	EnvironmentID string `json:"environmentId"`
	ArtifactID    string `json:"artifactId"`
	ReleaseID     string `json:"releaseId,omitempty"`
}
type CopilotPlanReviewRequest struct {
	ExpectedVersion        uint64 `json:"expectedVersion"`
	PlanDigest             string `json:"planDigest"`
	AcknowledgeNotExecuted bool   `json:"acknowledgeNotExecuted"`
}
type CopilotPlanCancelRequest struct {
	ExpectedVersion uint64 `json:"expectedVersion"`
}
type CopilotPlan struct {
	ContentAvailableUntil *time.Time          `json:"contentAvailableUntil,omitempty"`
	PlanID                string              `json:"planId"`
	RequestID             string              `json:"requestId"`
	TurnID                string              `json:"turnId"`
	Kind                  string              `json:"kind"`
	State                 string              `json:"state"`
	Version               uint64              `json:"version"`
	PlanDigest            string              `json:"planDigest"`
	ExecutionEnabled      bool                `json:"executionEnabled"`
	ExecutionStatus       string              `json:"executionStatus"`
	Eligible              bool                `json:"eligible"`
	UnavailableReason     string              `json:"unavailableReason,omitempty"`
	Payload               *CopilotPlanPayload `json:"payload,omitempty"`
	CreatedAt             time.Time           `json:"createdAt"`
	ExpiresAt             time.Time           `json:"expiresAt"`
}
type CopilotPlanPayload struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Kind          string                    `json:"kind"`
	Label         string                    `json:"label"`
	CodePatch     *CopilotCodePatchPlan     `json:"codePatch,omitempty"`
	NewRun        *CopilotNewRunPlan        `json:"newRun,omitempty"`
	RetryApproval *CopilotRetryApprovalPlan `json:"retryApproval,omitempty"`
	Deployment    *CopilotDeploymentPlan    `json:"deployment,omitempty"`
}
type CopilotCodePatchPlan struct {
	ArtifactID   string `json:"artifactId,omitempty"`
	JobRunID     string `json:"jobRunId,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Path         string `json:"path"`
	BlobSHA      string `json:"blobSHA,omitempty"`
	BaseDigest   string `json:"baseDigest"`
	PatchDigest  string `json:"patchDigest"`
	ResultDigest string `json:"resultDigest"`
	Patch        string `json:"patch"`
	SnapshotOnly bool   `json:"snapshotOnly"`
}
type CopilotNewRunPlan struct {
	DefinitionSource   string `json:"definitionSource"`
	DefinitionVerified bool   `json:"definitionVerified"`
	PipelineID         string `json:"pipelineId"`
	DiagnosticRunID    string `json:"diagnosticRunId"`
	DefinitionRevision string `json:"definitionRevision,omitempty"`
	DefinitionDigest   string `json:"definitionDigest,omitempty"`
}
type CopilotRetryApprovalPlan struct {
	SourceBindingDigest string     `json:"sourceBindingDigest,omitempty"`
	InputManifestDigest string     `json:"inputManifestDigest"`
	RetryPlanDigest     string     `json:"retryPlanDigest"`
	PolicyFingerprint   string     `json:"policyFingerprint"`
	ApprovalID          string     `json:"approvalId"`
	JobRunID            string     `json:"jobRunId"`
	BindingKind         string     `json:"bindingKind"`
	EvidenceDigest      string     `json:"evidenceDigest"`
	PolicyID            string     `json:"policyId"`
	Deadline            *time.Time `json:"deadline"`
}
type CopilotDeploymentPlan struct {
	ArtifactExpiresAt  *time.Time `json:"artifactExpiresAt,omitempty"`
	PipelineID         string     `json:"pipelineId"`
	SourceRunID        string     `json:"sourceRunId"`
	DefinitionRevision string     `json:"definitionRevision"`
	TargetRef          string     `json:"targetRef"`
	Tier               string     `json:"tier"`
	PolicyDigest       string     `json:"policyDigest"`
	ReleaseOrigin      string     `json:"releaseOrigin,omitempty"`
	ReleaseState       string     `json:"releaseState,omitempty"`
	EnvironmentID      string     `json:"environmentId"`
	ArtifactID         string     `json:"artifactId"`
	ArtifactDigest     string     `json:"artifactDigest"`
	ReleaseID          string     `json:"releaseId,omitempty"`
	ReleaseVersion     int64      `json:"releaseVersion,omitempty"`
	TargetFingerprint  string     `json:"targetFingerprint"`
	Namespace          string     `json:"namespace"`
	Workload           string     `json:"workload"`
	Container          string     `json:"container"`
	ImageDigest        string     `json:"imageDigest"`
	ApprovalRequired   bool       `json:"approvalRequired"`
}
type CopilotPlanList struct {
	Plans     []CopilotPlan `json:"plans"`
	NextAfter uint64        `json:"nextAfter,omitempty"`
}
