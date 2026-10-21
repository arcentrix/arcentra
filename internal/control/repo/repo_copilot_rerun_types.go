// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

var ErrCopilotRerunInvalid = errors.New("invalid whole-job re-execution binding")

const CopilotRerunMaxDefinitionBytes = 512 * 1024
const CopilotRerunMaxSteps = 32

// AES-GCM v1: 12-byte nonce, 16-byte tag, at least one plaintext byte.
const CopilotRerunMinCiphertextBytes = 29
const CopilotRerunDefinitionTTL = 30 * 24 * time.Hour

// This public layout contains scheduling metadata only. Executable code and
// template references remain in authenticated encrypted definition content.
type CopilotRerunStepLayout struct {
	Name            string `json:"name"`
	Index           int32  `json:"index"`
	Uses            string `json:"uses"`
	Action          string `json:"action"`
	ContinueOnError bool   `json:"continueOnError"`
}
type CopilotRerunLayout struct {
	Version                int                      `json:"version"`
	JobName                string                   `json:"jobName"`
	Runtime                taskqueue.RuntimeRequest `json:"runtime"`
	Retry                  *pipelinev1.Retry        `json:"retry"`
	ApprovalTimeoutSeconds int64                    `json:"approvalTimeoutSeconds"`
	Steps                  []CopilotRerunStepLayout `json:"steps"`
}

func rerunUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}
func rerunUniqueUUIDs(ids ...string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if !rerunUUID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func rerunDefinitionRevision(revision string) bool {
	return len(revision) == 40 && sourceprovenance.ValidCommit(revision) && strings.ToLower(revision) == revision
}
func rerunName(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func rerunHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && hex.EncodeToString(b) == s
}
func rerunDigest(domain string, value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(append([]byte(domain+"\x00"), b...))
	return "v1:" + hex.EncodeToString(h[:])
}
func rerunCipherDigest(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }
func (v *CopilotRerunLayout) Canonical() (string, error) {
	if v == nil || v.Version != 1 || !rerunName(v.JobName) || v.Runtime.BindingDigest != "" || v.ApprovalTimeoutSeconds < 1 || v.ApprovalTimeoutSeconds > 3600 || len(v.Steps) < 1 || len(v.Steps) > CopilotRerunMaxSteps || taskqueue.ValidateWholeJobRetryPolicy(v.Retry) != nil {
		return "", ErrCopilotRerunInvalid
	}
	// Validate the immutable runtime descriptor without pretending precreation
	// has already allocated a job or Agent identity.
	if _, e := taskqueue.RuntimeBindingDigest("layout", "layout", "layout", "layout", &v.Runtime); e != nil {
		return "", ErrCopilotRerunInvalid
	}
	names := map[string]bool{}
	shells, uploading := 0, false
	for i, s := range v.Steps {
		if s.Index != int32(i) || !rerunName(s.Name) || names[s.Name] || s.ContinueOnError {
			return "", ErrCopilotRerunInvalid
		}
		switch {
		case taskqueue.RerunShellStep(s.Uses, s.Action):
			if uploading {
				return "", ErrCopilotRerunInvalid
			}
			shells++
		case taskqueue.RerunArtifactUploadStep(s.Uses, s.Action):
			if shells == 0 {
				return "", ErrCopilotRerunInvalid
			}
			uploading = true
		default:
			return "", ErrCopilotRerunInvalid
		}
		names[s.Name] = true
	}
	if shells == 0 {
		return "", ErrCopilotRerunInvalid
	}
	b, e := json.Marshal(v)
	if e != nil || len(b) > 64*1024 {
		return "", ErrCopilotRerunInvalid
	}
	return string(b), nil
}
func (v *CopilotRerunLayout) Digest() (string, error) {
	if _, e := v.Canonical(); e != nil {
		return "", e
	}
	return rerunDigest("arcentra-copilot-rerun-layout-v1", v), nil
}
func ParseCopilotRerunLayout(body string) (*CopilotRerunLayout, error) {
	if len(body) > 64*1024 {
		return nil, ErrCopilotRerunInvalid
	}
	var v CopilotRerunLayout
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil {
		return nil, ErrCopilotRerunInvalid
	}
	canonical, e := v.Canonical()
	if e != nil || canonical != body {
		return nil, ErrCopilotRerunInvalid
	}
	return &v, nil
}

type CopilotDefinitionSnapshotInput struct {
	ProjectID                                                                                            string // Exact project from the verified creator's locked scope.
	DefinitionRepository, DefinitionPath, DefinitionRevision, ResolvedSpecDigest, SecretReferencesDigest string
	RepositoryConfigDigest, TriggerVariablesDigest                                                       string
	PayloadSchemaVersion                                                                                 uint32
	Layout                                                                                               CopilotRerunLayout
	CipherVersion                                                                                        uint32
	ExpiresAt                                                                                            time.Time
}

// NewCopilotDefinitionSnapshotMetadata validates a prospective value only. Production
// insertion MUST share the original verified manual-run creation transaction;
// no public independently committing capture/backfill method is provided.
func NewCopilotDefinitionSnapshotMetadata(run *model.PipelineRun, in CopilotDefinitionSnapshotInput, now time.Time) (*model.CopilotDefinitionSnapshot, error) {
	if run == nil || !historyID(run.RunID) || !historyID(run.PipelineID) || !historyID(in.ProjectID) || (run.ProjectID != "" && run.ProjectID != in.ProjectID) || run.Status != model.PipelineStatusPending || run.StartTime != nil || run.EndTime != nil || run.AllowDeployments || run.TotalJobs > 1 {
		return nil, ErrCopilotRerunInvalid
	}
	variablesDigest, variablesErr := CopilotRerunTriggerVariablesDigest(run.Env)
	if variablesErr != nil || variablesDigest != in.TriggerVariablesDigest || in.PayloadSchemaVersion != 1 || !sourceprovenance.ValidBindingDigest(in.RepositoryConfigDigest) {
		return nil, ErrCopilotRerunInvalid
	}
	if run.DefinitionCommitSha != in.DefinitionRevision || run.DefinitionPath != in.DefinitionPath || !rerunDefinitionRevision(in.DefinitionRevision) || !sourceprovenance.ValidBindingDigest(in.ResolvedSpecDigest) || !sourceprovenance.ValidBindingDigest(in.SecretReferencesDigest) {
		return nil, ErrCopilotRerunInvalid
	}
	repository, e := sourceprovenance.CanonicalRepository(in.DefinitionRepository)
	if e != nil || repository != in.DefinitionRepository || !rerunDefinitionPath(in.DefinitionPath) {
		return nil, ErrCopilotRerunInvalid
	}
	layout, e := in.Layout.Canonical()
	if e != nil {
		return nil, e
	}
	policyDigest, _ := in.Layout.Digest()
	context, e := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if e != nil {
		return nil, ErrCopilotRerunInvalid
	}
	contextDigest, e := context.Digest()
	if e != nil || contextDigest != run.RetryApprovalContextDigest || context.ProjectID != in.ProjectID || context.PipelineID != run.PipelineID || context.PipelineRunID != run.RunID || context.ActorID != run.TriggeredBy || context.DefinitionCommitSHA != in.DefinitionRevision || context.DefinitionPath != in.DefinitionPath || context.ResolvedDefinitionDigest != in.ResolvedSpecDigest || len(context.GatedJobs) != 1 || context.GatedJobs[0] != in.Layout.JobName || context.Policy.TimeoutSeconds != in.Layout.ApprovalTimeoutSeconds {
		return nil, ErrCopilotRerunInvalid
	}
	if in.CipherVersion != 1 || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(CopilotRerunDefinitionTTL)) {
		return nil, ErrCopilotRerunInvalid
	}
	out := &model.CopilotDefinitionSnapshot{RetryApprovalContext: run.RetryApprovalContext, RetryApprovalContextDigest: run.RetryApprovalContextDigest, RepositoryConfigDigest: in.RepositoryConfigDigest, TriggerVariablesDigest: in.TriggerVariablesDigest, PayloadSchemaVersion: in.PayloadSchemaVersion, SnapshotID: uuid.NewString(), RunID: run.RunID, PipelineID: run.PipelineID, ProjectID: in.ProjectID, JobName: in.Layout.JobName, DefinitionRepository: repository, DefinitionPath: in.DefinitionPath, DefinitionRevision: in.DefinitionRevision, ResolvedSpecDigest: in.ResolvedSpecDigest, RuntimePolicyDigest: policyDigest, SecretReferencesDigest: in.SecretReferencesDigest, LayoutJSON: layout, CipherVersion: in.CipherVersion, CreatedAt: now.UTC(), ExpiresAt: in.ExpiresAt.UTC()}
	out.ContentBinding = CopilotDefinitionContentBinding(out)
	return out, nil
}

// FinalizeCopilotDefinitionSnapshot attaches sealed bytes only after the caller
// has encrypted against the server-generated SnapshotID and ContentBinding.
// Callers recompute this metadata digest before decrypting stored content.
func FinalizeCopilotDefinitionSnapshot(metadata *model.CopilotDefinitionSnapshot, ciphertext []byte) (*model.CopilotDefinitionSnapshot, error) {
	if metadata == nil || metadata.ContentBinding != CopilotDefinitionContentBinding(metadata) || metadata.BindingDigest != "" || metadata.CiphertextDigest != "" || len(metadata.PayloadCiphertext) != 0 {
		return nil, ErrCopilotRerunInvalid
	}
	out := *metadata
	out.PayloadCiphertext = bytes.Clone(ciphertext)
	out.CiphertextDigest = rerunCipherDigest(ciphertext)
	out.BindingDigest = rerunSnapshotDigest(&out)
	if ValidateCopilotDefinitionSnapshot(&out) != nil {
		return nil, ErrCopilotRerunInvalid
	}
	return &out, nil
}

// CopilotDefinitionContentBinding excludes ciphertext and final binding digest.
func CopilotDefinitionContentBinding(v *model.CopilotDefinitionSnapshot) string {
	if v == nil {
		return ""
	}
	return rerunDigest("arcentra-copilot-definition-content-v1", []any{v.RetryApprovalContext, v.RetryApprovalContextDigest, v.RepositoryConfigDigest, v.TriggerVariablesDigest, v.PayloadSchemaVersion, v.SnapshotID, v.RunID, v.PipelineID, v.ProjectID, v.JobName, v.DefinitionRepository, v.DefinitionPath, v.DefinitionRevision, v.ResolvedSpecDigest, v.RuntimePolicyDigest, v.SecretReferencesDigest, v.LayoutJSON, v.CipherVersion, v.CreatedAt.UnixMilli(), v.ExpiresAt.UnixMilli()})
}
func rerunDefinitionPath(p string) bool {
	return p != "" && len(p) <= 1024 && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n")
}
func rerunSnapshotDigest(v *model.CopilotDefinitionSnapshot) string {
	return rerunDigest("arcentra-copilot-definition-snapshot-v1", []any{CopilotDefinitionContentBinding(v), v.CiphertextDigest})
}
func ValidateCopilotDefinitionSnapshot(v *model.CopilotDefinitionSnapshot) error {
	if v == nil || v.ContentBinding != CopilotDefinitionContentBinding(v) || v.PayloadSchemaVersion != 1 || !sourceprovenance.ValidBindingDigest(v.RepositoryConfigDigest) || !sourceprovenance.ValidBindingDigest(v.TriggerVariablesDigest) || !rerunUUID(v.SnapshotID) || !historyID(v.RunID) || !historyID(v.PipelineID) || !historyID(v.ProjectID) || !rerunDefinitionRevision(v.DefinitionRevision) || !sourceprovenance.ValidBindingDigest(v.ResolvedSpecDigest) || !sourceprovenance.ValidBindingDigest(v.SecretReferencesDigest) || !rerunDefinitionPath(v.DefinitionPath) || v.CipherVersion != 1 || len(v.PayloadCiphertext) < CopilotRerunMinCiphertextBytes || len(v.PayloadCiphertext) > CopilotRerunMaxDefinitionBytes || v.CiphertextDigest != rerunCipherDigest(v.PayloadCiphertext) || !v.ExpiresAt.After(v.CreatedAt) || v.ExpiresAt.After(v.CreatedAt.Add(CopilotRerunDefinitionTTL)) {
		return ErrCopilotRerunInvalid
	}
	context, err := retryapproval.ParseRunContext(v.RetryApprovalContext)
	if err != nil {
		return ErrCopilotRerunInvalid
	}
	contextDigest, err := context.Digest()
	if err != nil || contextDigest != v.RetryApprovalContextDigest || context.ProjectID != v.ProjectID || context.PipelineID != v.PipelineID || context.PipelineRunID != v.RunID || context.DefinitionCommitSHA != v.DefinitionRevision || context.DefinitionPath != v.DefinitionPath || context.ResolvedDefinitionDigest != v.ResolvedSpecDigest || len(context.GatedJobs) != 1 || context.GatedJobs[0] != v.JobName {
		return ErrCopilotRerunInvalid
	}
	repo, e := sourceprovenance.CanonicalRepository(v.DefinitionRepository)
	if e != nil || repo != v.DefinitionRepository {
		return ErrCopilotRerunInvalid
	}
	layout, e := ParseCopilotRerunLayout(v.LayoutJSON)
	if e != nil || layout.JobName != v.JobName || context.Policy.TimeoutSeconds != layout.ApprovalTimeoutSeconds {
		return ErrCopilotRerunInvalid
	}
	digest, _ := layout.Digest()
	if digest != v.RuntimePolicyDigest || v.BindingDigest != rerunSnapshotDigest(v) {
		return ErrCopilotRerunInvalid
	}
	return nil
}

// Original evidence is captured only after verified terminal checkout/input
// results exist. No approval decision or delivered environment is copied.
type CopilotRerunOriginalEvidence struct {
	Version              int                        `json:"version"`
	SnapshotID           string                     `json:"snapshotId"`
	SnapshotDigest       string                     `json:"snapshotDigest"`
	Identity             taskqueue.JobInputIdentity `json:"identity"`
	Source               sourceprovenance.Binding   `json:"source"`
	SourceCommit         string                     `json:"sourceCommit"`
	InputManifestDigest  string                     `json:"inputManifestDigest"`
	RetryPlanDigest      string                     `json:"retryPlanDigest"`
	TerminalReportDigest string                     `json:"terminalReportDigest"`
}

func BuildCopilotRerunOriginalEvidence(snapshot *model.CopilotDefinitionSnapshot, run *model.PipelineRun, job *model.JobRun, jobCount int, now time.Time) (*CopilotRerunOriginalEvidence, error) {
	if ValidateCopilotDefinitionSnapshot(snapshot) != nil || run == nil || job == nil || jobCount != 1 || run.TotalJobs != 1 || run.Status != model.PipelineStatusFailed || job.Status != model.JobRunStatusFailed || run.EndTime == nil || job.EndTime == nil || !now.Before(snapshot.ExpiresAt) || run.RunID != snapshot.RunID || run.PipelineID != snapshot.PipelineID || run.ProjectID != snapshot.ProjectID || job.PipelineRunID != run.RunID || job.PipelineID != run.PipelineID || job.JobName != snapshot.JobName || run.DefinitionCommitSha != snapshot.DefinitionRevision || run.DefinitionPath != snapshot.DefinitionPath || run.RetryApprovalContext != snapshot.RetryApprovalContext || run.RetryApprovalContextDigest != snapshot.RetryApprovalContextDigest || !rerunHash(job.TerminalReportDigest) || !job.RetryApprovalRequired || !job.SourceExpectationCaptured || job.SourceType != "git" || job.SourceProvenanceState != "reported" || !sourceprovenance.ValidCommit(job.SourceResolvedCommit) {
		return nil, ErrCopilotRerunInvalid
	}
	context, contextErr := retryapproval.ParseRunContext(snapshot.RetryApprovalContext)
	if contextErr != nil || context.ActorID != run.TriggeredBy {
		return nil, ErrCopilotRerunInvalid
	}
	identity := generationIdentity(job)
	if identity.Validate() != nil {
		return nil, ErrCopilotRerunInvalid
	}
	source, e := sourceprovenance.NewBinding(job.PipelineID, job.PipelineRunID, job.JobRunID, job.SourceType, job.SourceRepository, job.SourceRef)
	if e != nil || source.Digest != job.SourceBindingDigest {
		return nil, ErrCopilotRerunInvalid
	}
	manifest, e := taskqueue.ParseJobInputManifest(job.InputManifest)
	if e != nil || len(manifest.Artifacts) != 0 || manifest.SourceCommit != job.SourceResolvedCommit || job.InputManifestState != "pinned" {
		return nil, ErrCopilotRerunInvalid
	}
	digest, e := manifest.Digest(identity)
	if e != nil || digest != job.InputManifestDigest {
		return nil, ErrCopilotRerunInvalid
	}
	layout, e := ParseCopilotRerunLayout(snapshot.LayoutJSON)
	if e != nil {
		return nil, e
	}
	runtime := taskqueue.RuntimeRequest{Type: job.RuntimeType, Image: job.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: job.RuntimeCPU, Memory: job.RuntimeMemory}}
	if runtime != layout.Runtime {
		return nil, ErrCopilotRerunInvalid
	}
	runtimeDigest, e := taskqueue.RuntimeBindingDigest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, &runtime)
	if e != nil || runtimeDigest != job.RuntimeBindingDigest {
		return nil, ErrCopilotRerunInvalid
	}
	plan, e := taskqueue.ParseJobRetryPlan(job.RetryPlan)
	if e != nil || taskqueue.ValidateWholeJobRetryPolicy(plan.Policy) != nil || !proto.Equal(plan.Policy, layout.Retry) || !plan.RetryApprovalRequired || plan.RetryApprovalTimeoutSeconds != layout.ApprovalTimeoutSeconds || len(plan.Steps) != len(layout.Steps) {
		return nil, ErrCopilotRerunInvalid
	}
	pd, e := plan.Digest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, job.RuntimeBindingDigest)
	if e != nil || pd != job.RetryPlanDigest {
		return nil, ErrCopilotRerunInvalid
	}
	for i, s := range plan.Steps {
		expected := layout.Steps[i]
		if s.Name != expected.Name || s.StepIndex != expected.Index || s.Uses != expected.Uses || s.Action != expected.Action || s.ContinueOnError != expected.ContinueOnError {
			return nil, ErrCopilotRerunInvalid
		}
	}
	return &CopilotRerunOriginalEvidence{Version: 1, SnapshotID: snapshot.SnapshotID, SnapshotDigest: snapshot.BindingDigest, Identity: identity, Source: *source, SourceCommit: job.SourceResolvedCommit, InputManifestDigest: job.InputManifestDigest, RetryPlanDigest: job.RetryPlanDigest, TerminalReportDigest: job.TerminalReportDigest}, nil
}
func (v *CopilotRerunOriginalEvidence) Digest() (string, error) {
	if v == nil || v.Version != 1 || !rerunUUID(v.SnapshotID) || !sourceprovenance.ValidBindingDigest(v.SnapshotDigest) || v.Identity.Validate() != nil || !sourceprovenance.ValidCommit(v.SourceCommit) || !sourceprovenance.ValidBindingDigest(v.InputManifestDigest) || !sourceprovenance.ValidBindingDigest(v.RetryPlanDigest) || !rerunHash(v.TerminalReportDigest) {
		return "", ErrCopilotRerunInvalid
	}
	source, e := sourceprovenance.NewBinding(v.Identity.PipelineID, v.Identity.PipelineRunID, v.Identity.JobRunID, v.Source.Type, v.Source.Repository, v.Source.Ref)
	if e != nil || *source != v.Source || v.Source.Digest != v.Identity.SourceBindingDigest {
		return "", ErrCopilotRerunInvalid
	}
	return rerunDigest("arcentra-copilot-rerun-original-evidence-v1", v), nil
}

// CopilotRerunTriggerVariablesDigest binds the original trigger input mapping.
// A service envelope must seal that same mapping with the resolved parsed spec;
// dispatch must never substitute a later read of mutable PipelineRun.Env.
func CopilotRerunTriggerVariablesDigest(body string) (string, error) {
	if len(body) > 64*1024 {
		return "", ErrCopilotRerunInvalid
	}
	variables, ok := automaticEnvironment(body)
	if !ok || len(variables) > 256 {
		return "", ErrCopilotRerunInvalid
	}
	for key, value := range variables {
		if !rerunName(key) || len(value) > 16*1024 {
			return "", ErrCopilotRerunInvalid
		}
	}
	return rerunDigest("arcentra-copilot-rerun-trigger-variables-v1", variables), nil
}
