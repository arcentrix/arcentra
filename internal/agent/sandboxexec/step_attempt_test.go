// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

type liveDurableFake struct {
	*durableFake
	ioIdentity string
	live       *liveFake
}
type liveFake struct {
	backend             *durableFake
	identity            sandbox.LiveSessionIdentity
	ref                 sandbox.ResourceRef
	boundary            *sandbox.FailedStepBoundary
	executions, resumes int
	failFirst           bool
	resumeErr           error
	boundaryErr         error
}

func (f *liveDurableFake) RuntimeIOIdentity() (string, error) {
	if f.ioIdentity != "" {
		return f.ioIdentity, nil
	}
	return strings.Repeat("a", 64), nil
}
func (f *liveDurableFake) CreateLiveSession(ctx context.Context, o *sandbox.CreateOptions, id sandbox.LiveSessionIdentity) (sandbox.LiveSession, error) {
	if _, err := f.durableFake.Create(ctx, o); err != nil {
		return nil, err
	}
	f.live = &liveFake{backend: f.durableFake, identity: id, ref: *o.Resource, failFirst: true}
	return f.live, nil
}
func (f *liveFake) Identity() sandbox.LiveSessionIdentity { return f.identity }
func (f *liveFake) Resource() sandbox.ResourceRef         { return f.ref }
func (f *liveFake) Start(ctx context.Context) error       { return f.backend.Start(ctx, f.ref.ID) }
func (f *liveFake) CheckBoundary(ctx context.Context, b *sandbox.FailedStepBoundary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.boundaryErr != nil {
		return f.boundaryErr
	}
	if b == nil || b != f.boundary {
		return errors.New("stale opaque boundary")
	}
	return nil
}
func (f *liveFake) ExecuteStep(ctx context.Context, _ sandbox.StepIdentity, _ []string, o *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, *sandbox.FailedStepBoundary, error) {
	f.executions++
	f.backend.event("live-execute")
	if o.Stdout != nil {
		_, _ = o.Stdout.Write([]byte("streamed"))
	}
	if f.failFirst && f.executions == 1 {
		f.boundary = &sandbox.FailedStepBoundary{}
		return &sandbox.ExecuteResult{ExitCode: 7}, f.boundary, nil
	}
	return &sandbox.ExecuteResult{}, nil, nil
}
func (f *liveFake) ResumeStep(ctx context.Context, b *sandbox.FailedStepBoundary, i sandbox.StepIdentity, cmd []string, o *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, *sandbox.FailedStepBoundary, error) {
	if err := f.CheckBoundary(ctx, b); err != nil {
		return nil, nil, err
	}
	f.resumes++
	f.backend.event("live-resume")
	f.boundary = nil
	if f.resumeErr != nil {
		return nil, nil, f.resumeErr
	}
	return f.ExecuteStep(ctx, i, cmd, o)
}
func newStepSession(t *testing.T, slots int, kinds ...string) (*Controller, *Session, *liveDurableFake, taskpayload.JobGeneration) {
	t.Helper()
	ctx := context.Background()
	f := &liveDurableFake{durableFake: newDurableFake()}
	c, err := OpenController(ctx, f, controllerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	g := testGeneration(t, nil)
	maxAttempts := 2
	if slots%2 != 0 {
		maxAttempts = 3
	}
	plan := &taskpayload.JobRetryPlan{Version: 2, Policy: &pipelinev1.Retry{Mode: "failed_step", MaxAttempts: int32(maxAttempts), Idempotent: true, AllowedExitCodes: []int32{7}}, DefinitionDigest: "v1:" + strings.Repeat("e", 64)}
	for i := 0; i < slots/maxAttempts; i++ {
		ids := make([]string, maxAttempts)
		for j := range ids {
			ids[j] = uuid.NewString()
		}
		uses, action := "shell", ""
		if i < len(kinds) && kinds[i] == "upload" {
			uses, action = "builtin:artifacts", "upload"
		}
		plan.Steps = append(plan.Steps, taskpayload.RetryStepPlan{Action: action, Name: fmt.Sprintf("shell-%d", i), StepIndex: int32(i), Uses: uses, AttemptRunIDs: ids, DefinitionDigest: "v1:" + strings.Repeat("a", 64)})
	}
	g.RetryPlanDigest, err = plan.Digest(g.PipelineID, g.PipelineRunID, g.JobRunID, g.AgentID, g.RuntimeBindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	bindGeneration(t, &g)
	request := initialGenerationRequest(t, g)
	if err := c.RecordGenerationIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordGenerationAdmission(ctx, request, g); err != nil {
		t.Fatal(err)
	}
	opts := durableOptions()
	opts.StepAttemptSlots = slots
	opts.StepRetryPlan = plan
	s, err := c.AllocateStepRetryGeneration(ctx, opts, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()); _ = c.Close(context.Background()) })
	return c, s, f, g
}
func stepIntent(t *testing.T, s *Session, g taskpayload.JobGeneration, index int32) taskpayload.StepAttemptTransition {
	t.Helper()
	identity, err := s.StepAttemptSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	planned := s.stepPlan.Steps[index]
	id := planned.AttemptRunIDs[0]
	request := taskpayload.StepAttemptTransition{Version: 1, Operation: "admit", Scope: taskpayload.StepAttemptIdentity{Identity: taskpayload.JobInputIdentity{PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest}, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, InputManifestDigest: g.InputManifestDigest, RetryPlanDigest: g.RetryPlanDigest, Session: identity, LogicalStepRunID: id, StepRunID: id, StepName: planned.Name, StepIndex: index, DefinitionDigest: "v1:" + strings.Repeat("a", 64), Attempt: 1, StepClaimID: taskpayload.StepAttemptClaimID(g.OriginalClaimID, id)}}
	request.RequestDigest, err = request.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}
func stepAdmission(t *testing.T, request taskpayload.StepAttemptTransition) taskpayload.StepAttemptAdmission {
	t.Helper()
	now := time.Now()
	a := taskpayload.StepAttemptAdmission{Version: 1, Scope: request.Scope, AdmissionID: uuid.NewString(), PreviousAdmissionID: request.PreviousAdmissionID, RequestDigest: request.RequestDigest, NotBeforeUnixMilli: now.Add(-time.Second).UnixMilli(), ExpiresUnixMilli: now.Add(time.Minute).UnixMilli()}
	var err error
	a.BindingDigest, err = a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateTransition(request); err != nil {
		t.Fatal(err)
	}
	return a
}
func admitStep(t *testing.T, s *Session, request taskpayload.StepAttemptTransition) taskpayload.StepAttemptAdmission {
	t.Helper()
	if err := s.RecordStepAttemptIntent(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	a := stepAdmission(t, request)
	if err := s.RecordStepAttemptAdmission(context.Background(), request, a); err != nil {
		t.Fatal(err)
	}
	return a
}
func stepRequest(a taskpayload.StepAttemptAdmission, s *Session) *executor.ExecutionRequest {
	return &executor.ExecutionRequest{PipelineRunID: a.Scope.Identity.PipelineRunID, JobRunID: a.Scope.Identity.JobRunID, AgentID: a.Scope.Identity.AgentID, StepRunID: a.Scope.StepRunID, StepIndex: a.Scope.StepIndex, Workspace: s.Workspace(), Step: &executor.StepInfo{Name: a.Scope.StepName, Uses: "shell", Args: map[string]any{"command": "true"}}}
}
func advanceStep(t *testing.T, s *Session, a taskpayload.StepAttemptAdmission) taskpayload.StepAttemptTransition {
	t.Helper()
	b, err := s.PausedStepBoundary(context.Background(), a, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	r := taskpayload.StepAttemptTransition{Version: 1, Operation: "advance", Scope: a.Scope, PreviousStepRunID: a.Scope.StepRunID, PreviousStepClaimID: a.Scope.StepClaimID, PreviousAdmissionID: a.AdmissionID, PreviousAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: b.FailedStepReceiptDigest, Boundary: &b}
	r.Scope.Attempt++
	planned, err := s.plannedStep(a.Scope)
	if err != nil {
		t.Fatal(err)
	}
	r.Scope.StepRunID = planned.AttemptRunIDs[r.Scope.Attempt-1]
	r.Scope.StepClaimID = taskpayload.StepAttemptClaimID(a.Scope.Identity.ClaimID, r.Scope.StepRunID)
	r.RequestDigest, _ = r.Digest()
	return r
}
func TestLiveStepRetainsWorkspaceAndConsumesOnce(t *testing.T) {
	c, s, f, g := newStepSession(t, 3)
	ctx := context.Background()
	request := stepIntent(t, s, g, 0)
	a := admitStep(t, s, request)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.Workspace(), "partial")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	req := stepRequest(a, s)
	logs := ""
	req.OutputHandler = func(_, v string) { logs += v }
	if _, err := s.Execute(ctx, req); err == nil {
		t.Fatal("legacy execute bypassed live admission")
	}
	if _, err := s.ExecuteStepAttempt(ctx, req, a); !IsCommandExit(err) {
		t.Fatalf("expected pure exit: %v", err)
	}
	if logs != "streamed" {
		t.Fatal("manager output callback lost")
	}
	r := advanceStep(t, s, a)
	next := admitStep(t, s, r)
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "retained" {
		t.Fatal("failed attempt mutations lost")
	}
	if f.live.executions != 2 || f.live.resumes != 1 {
		t.Fatal("unexpected command effects")
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
		t.Fatal("consumed admission replay executed")
	}
	if f.live.executions != 2 || f.live.resumes != 1 {
		t.Fatal("duplicate command effects")
	}
	if len(c.stepAttempts) != 2 {
		t.Fatal("attempt audit missing")
	}
}

func TestLiveStepJournalReopenIsCleanupOnly(t *testing.T) {
	for _, phase := range []string{"intent", "admission", "consumed", "failed", "successor-intent", "successor-admission", "successor-consumed"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			c, s, f, g := newStepSession(t, 3)
			r := stepIntent(t, s, g, 0)
			if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
				t.Fatal(err)
			}
			a := stepAdmission(t, r)
			if phase != "intent" {
				if err := s.RecordStepAttemptAdmission(ctx, r, a); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "consumed" {
				if err := s.RecordStepAttemptConsumed(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "failed" || strings.HasPrefix(phase, "successor-") {
				if err := s.Start(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
					t.Fatal(err)
				}
				if strings.HasPrefix(phase, "successor-") {
					r = advanceStep(t, s, a)
					if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
						t.Fatal(err)
					}
					a = stepAdmission(t, r)
					if phase != "successor-intent" {
						if err := s.RecordStepAttemptAdmission(ctx, r, a); err != nil {
							t.Fatal(err)
						}
					}
					if phase == "successor-consumed" {
						if err := s.acquire(ctx); err != nil {
							t.Fatal(err)
						}
						err := s.consumeStepAttempt(ctx, a, false)
						s.release()
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			beforeExec, beforeResume := 0, 0
			if f.live != nil {
				beforeExec, beforeResume = f.live.executions, f.live.resumes
			}
			opts := c.opts
			if err := c.closeHandles(); err != nil {
				t.Fatal(err)
			}
			recovered, err := OpenController(ctx, f, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close(ctx)
			if recovered.Ready() {
				t.Fatal("recovery became executable without authoritative final ACK")
			}
			if len(recovered.sessions) != 0 {
				t.Fatal("recovery reconstructed live session")
			}
			if f.live != nil && (f.live.executions != beforeExec || f.live.resumes != beforeResume) {
				t.Fatal("recovery replayed a runtime effect")
			}
			if len(recovered.stepAttempts) == 0 {
				t.Fatal("recovery discarded immutable audit")
			}
			if err := recovered.AcknowledgeCompletion(ctx, g.OriginalClaimID); err != nil {
				t.Fatal(err)
			}
			if len(recovered.stepAttempts) != 0 || !recovered.Ready() {
				t.Fatal("final ACK did not release retained audit")
			}
		})
	}
}

func TestLiveStepPersistenceFailuresPrecedeEffects(t *testing.T) {
	for _, phase := range []string{"intent", "admission", "consumed"} {
		for _, point := range []string{"record.write", "record.sync", "record.rename", "directory.sync"} {
			t.Run(phase+"/"+point, func(t *testing.T) {
				ctx := context.Background()
				c, s, f, g := newStepSession(t, 2)
				r := stepIntent(t, s, g, 0)
				a := stepAdmission(t, r)
				if phase != "intent" {
					if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "consumed" {
					if err := s.RecordStepAttemptAdmission(ctx, r, a); err != nil {
						t.Fatal(err)
					}
					if err := s.Start(ctx); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				c.hook = func(p string) error {
					if p == point {
						calls++
						if phase == "consumed" && calls == 1 {
							return nil
						}
						return errors.New("injected durable write failure")
					}
					return nil
				}
				var err error
				switch phase {
				case "intent":
					err = s.RecordStepAttemptIntent(ctx, r)
				case "admission":
					err = s.RecordStepAttemptAdmission(ctx, r, a)
				case "consumed":
					_, err = s.ExecuteStepAttempt(ctx, stepRequest(a, s), a)
				}
				if err == nil {
					t.Fatal("injected persistence fault ignored")
				}
				if f.live != nil && (f.live.executions != 0 || f.live.resumes != 0) {
					t.Fatal("effect preceded durable consumption")
				}
				if c.Ready() {
					t.Fatal("uncertain journal reported ready")
				}
				entries, readErr := os.ReadDir(c.opts.JournalRoot)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(entries) < 3 {
					t.Fatal("fault removed evidence")
				}
			})
		}
	}
}

func TestLiveStepResumeUncertaintyNeverReplays(t *testing.T) {
	ctx := context.Background()
	_, s, f, g := newStepSession(t, 3)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	next := admitStep(t, s, advanceStep(t, s, a))
	f.live.resumeErr = errors.New("lost resume ACK")
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil || IsCommandExit(err) {
		t.Fatal("uncertain resume classified as retryable")
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
		t.Fatal("uncertain resume replayed")
	}
	if f.live.resumes != 1 || f.live.executions != 1 {
		t.Fatal("uncertain resume produced repeated effect")
	}
}

func TestLiveStepAuditRejectsMutationAndExpiry(t *testing.T) {
	for _, mutation := range []string{"workspace", "root", "runtime-io", "boundary", "admission", "expired"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			_, s, f, g := newStepSession(t, 3)
			a := admitStep(t, s, stepIntent(t, s, g, 0))
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
				t.Fatal(err)
			}
			r := advanceStep(t, s, a)
			if mutation == "boundary" {
				r.Boundary.BoundaryNonce = uuid.NewString()
				r.RequestDigest, _ = r.Digest()
				if err := s.RecordStepAttemptIntent(ctx, r); err == nil {
					t.Fatal("mutated boundary accepted")
				}
				return
			}
			next := admitStep(t, s, r)
			switch mutation {
			case "workspace":
				if err := os.Rename(s.workspace, s.workspace+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(s.workspace, 0700); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Rename(s.rootPath, s.rootPath+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(s.rootPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "runtime-io":
				f.ioIdentity = strings.Repeat("b", 64)
			case "admission":
				next.AdmissionID = uuid.NewString()
				next.BindingDigest, _ = next.Digest()
			case "expired":
				s.boundaryAudit.ExpiresUnixMilli = time.Now().Add(-time.Second).UnixMilli()
			}
			if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
				t.Fatal("mutated boundary executed")
			}
			if f.live.resumes != 0 || f.live.executions != 1 {
				t.Fatal("mutation crossed runtime boundary")
			}
		})
	}
}

func TestLiveStepQuotaReservedBeforeRuntime(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 14)
	if f.live != nil {
		t.Fatal("allocation created a runtime")
	}
	if c.reservedJournalCount() != 16 {
		t.Fatal("future attempt slots were not reserved")
	}
	other := testGeneration(t, nil)
	other.JobRunID = "other-job"
	other.OriginalClaimID = uuid.NewString()
	bindGeneration(t, &other)
	if err := c.RecordGenerationIntent(ctx, initialGenerationRequest(t, other)); err == nil {
		t.Fatal("another job stole reserved attempt capacity")
	}
	_ = s
	_ = g
}

func TestLiveStepSealedUploadsArePlanBound(t *testing.T) {
	ctx := context.Background()
	_, s, _, g := newStepSession(t, 6, "upload", "upload", "shell")
	first := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStepAttemptConsumed(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := admitStep(t, s, stepIntent(t, s, g, 1))
	if err := s.RecordStepAttemptConsumed(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStepAttemptIntent(ctx, stepIntent(t, s, g, 2)); err == nil {
		t.Fatal("sealed session accepted a planned shell")
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(second, s), second); err == nil {
		t.Fatal("sealed artifact admission authorized shell")
	}
}

func TestLiveStepConsumedSyncCrossingExpiryDoesNotResume(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 3)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	r := advanceStep(t, s, a)
	if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
		t.Fatal(err)
	}
	next := stepAdmission(t, r)
	next.ExpiresUnixMilli = time.Now().Add(100 * time.Millisecond).UnixMilli()
	next.BindingDigest, _ = next.Digest()
	if err := s.RecordStepAttemptAdmission(ctx, r, next); err != nil {
		t.Fatal(err)
	}
	count := 0
	c.hook = func(point string) error {
		if point == "record.sync" {
			count++
			if count == 2 {
				time.Sleep(time.Until(time.UnixMilli(next.ExpiresUnixMilli).Add(5 * time.Millisecond)))
			}
		}
		return nil
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
		t.Fatal("expired admission resumed after slow durable consume")
	}
	if f.live.resumes != 0 || f.live.executions != 1 {
		t.Fatal("expiry crossed runtime effect gate")
	}
}

func TestLiveStepBoundaryUsesOriginalJobDeadline(t *testing.T) {
	ctx := context.Background()
	_, s, _, g := newStepSession(t, 3)
	s.jobDeadline = time.Now().Add(10 * time.Minute)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.ExecuteStepAttempt(commandCtx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	b, err := s.PausedStepBoundary(ctx, a, strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	if b.ExpiresUnixMilli != s.jobDeadline.UnixMilli() || b.ExpiresUnixMilli < time.Now().Add(60*time.Second).UnixMilli() {
		t.Fatal("command timeout replaced original job bound")
	}
}

func TestLiveStepDeniedObservationCannotBeConsumed(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 2)
	r := stepIntent(t, s, g, 0)
	if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
		t.Fatal(err)
	}
	a := stepAdmission(t, r)
	if err := s.RecordStepAttemptObservation(ctx, r, a); err != nil {
		t.Fatal(err)
	}
	row := c.stepAttempts[stepAttemptFilename(r)]
	if row.Observation == nil || row.Admission != nil || row.Consumed {
		t.Fatal("denied observation changed executable authority")
	}
	if err := s.RecordStepAttemptAdmission(ctx, r, a); err == nil {
		t.Fatal("denied descriptor became executable")
	}
	if err := s.RecordStepAttemptConsumed(ctx, a); err == nil {
		t.Fatal("denied descriptor was consumed")
	}
	if f.live != nil {
		t.Fatal("denied descriptor started runtime")
	}
}

func TestLiveStepDrainCancelsConsumedEffect(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 3)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	next := admitStep(t, s, advanceStep(t, s, a))
	c.BeginStepRetryDrain()
	if c.Ready() {
		t.Fatal("draining controller reported ready")
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
		t.Fatal("draining session resumed")
	}
	if f.live.resumes != 0 {
		t.Fatal("drain crossed resume gate")
	}
}

type artifactEffectFake struct {
	calls  int
	before func(context.Context)
}

func (f *artifactEffectFake) Name() string                               { return "artifact-test" }
func (f *artifactEffectFake) CanExecute(*executor.ExecutionRequest) bool { return true }
func (f *artifactEffectFake) Execute(ctx context.Context, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	if f.before != nil {
		f.before(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.calls++
	result := executor.NewExecutionResult(f.Name())
	result.Complete(true, 0, nil)
	return result, nil
}
func artifactRequest(a taskpayload.StepAttemptAdmission, s *Session) *executor.ExecutionRequest {
	r := stepRequest(a, s)
	r.Step.Uses = "builtin:artifacts"
	r.Step.Action = "upload"
	return r
}

func TestLiveStepArtifactDeniedDrainAndExpiryNoEffect(t *testing.T) {
	for _, condition := range []string{"denied", "drain", "expiry"} {
		t.Run(condition, func(t *testing.T) {
			ctx := context.Background()
			c, s, _, g := newStepSession(t, 2, "upload")
			r := stepIntent(t, s, g, 0)
			if err := s.RecordStepAttemptIntent(ctx, r); err != nil {
				t.Fatal(err)
			}
			a := stepAdmission(t, r)
			if condition == "expiry" {
				a.ExpiresUnixMilli = time.Now().Add(100 * time.Millisecond).UnixMilli()
				a.BindingDigest, _ = a.Digest()
			}
			if err := s.RecordStepAttemptAdmission(ctx, r, a); err != nil {
				t.Fatal(err)
			}
			if err := s.Seal(ctx); err != nil {
				t.Fatal(err)
			}
			if condition == "denied" {
				if err := s.RecordStepAttemptObservation(ctx, r, a); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "drain" {
				c.BeginStepRetryDrain()
			}
			if condition == "expiry" {
				c.hook = func(point string) error {
					if point == "record.sync" {
						time.Sleep(time.Until(time.UnixMilli(a.ExpiresUnixMilli).Add(5 * time.Millisecond)))
					}
					return nil
				}
			}
			effect := &artifactEffectFake{}
			if _, err := s.ExecuteArtifactStepAttempt(ctx, artifactRequest(a, s), a, effect); err == nil {
				t.Fatal("refused artifact executed")
			}
			if effect.calls != 0 {
				t.Fatal("refused artifact reached external effect")
			}
		})
	}
}

func TestLiveStepArtifactDrainCancelsActiveContext(t *testing.T) {
	ctx := context.Background()
	c, s, _, g := newStepSession(t, 2, "upload")
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	effect := &artifactEffectFake{before: func(context.Context) { c.BeginStepRetryDrain() }}
	if _, err := s.ExecuteArtifactStepAttempt(ctx, artifactRequest(a, s), a, effect); err == nil {
		t.Fatal("drain did not cancel artifact context")
	}
	if effect.calls != 0 {
		t.Fatal("drain crossed artifact effect")
	}
}

func TestLiveStepJournalRejectsUnknownAndDuplicateFields(t *testing.T) {
	for _, mutation := range []string{"unknown", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			c, s, _, g := newStepSession(t, 2)
			r := stepIntent(t, s, g, 0)
			admitStep(t, s, r)
			name := stepAttemptFilename(r)
			path := filepath.Join(c.opts.JournalRoot, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			addition := `"unrecognized":true,`
			if mutation == "duplicate" {
				addition = `"version":3,`
			}
			data = append(append([]byte{'{'}, []byte(addition)...), data[1:]...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.readStepAttempt(context.Background(), name); err == nil {
				t.Fatal("noncanonical audit accepted")
			}
		})
	}
}

func TestLiveStepStartupPhasesReopenOnlyCleanup(t *testing.T) {
	for _, phase := range []string{phaseAllocated, phaseCreating, phaseCreated, phaseStarting, phaseRunning, phaseExecuting, phaseSealing, phaseSealed, phaseRemoving} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			c, s, f, g := newStepSession(t, 2)
			admitStep(t, s, stepIntent(t, s, g, 0))
			if phase != phaseAllocated {
				if err := s.advance(ctx, phaseCreating); err != nil {
					t.Fatal(err)
				}
			}
			if phase != phaseAllocated && phase != phaseCreating {
				r, err := s.durableRecord(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.createRuntime(ctx, r); err != nil {
					t.Fatal(err)
				}
				if phase != phaseCreated && phase != phaseStarting {
					if err := s.startRuntime(ctx, r.Resource.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if phase == phaseSealed {
				if err := f.SealOwned(ctx, s.resourceRef); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.advance(ctx, phase); err != nil {
				t.Fatal(err)
			}
			if err := c.closeHandles(); err != nil {
				t.Fatal(err)
			}
			recovered, err := OpenController(ctx, f, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close(ctx)
			if recovered.Ready() || len(recovered.sessions) != 0 {
				t.Fatal("startup recovered execution authority")
			}
			if f.live != nil && (f.live.executions != 0 || f.live.resumes != 0) {
				t.Fatal("startup recovery had execution effects")
			}
			if len(recovered.stepAttempts) != 1 {
				t.Fatal("startup recovery discarded admission audit")
			}
		})
	}
}

func TestLiveStepCleanupFailurePreservesAudit(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 2)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	f.removeOwned = func(context.Context, sandbox.ResourceRef) error { return errors.New("injected cleanup failure") }
	if err := s.Close(ctx); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if len(c.stepAttempts) != 1 || len(c.records) != 1 || c.Ready() {
		t.Fatal("cleanup failure discarded audit or reported readiness")
	}
	if _, err := os.Stat(s.Workspace()); err != nil {
		t.Fatal("failed cleanup discarded workspace evidence")
	}
	if err := c.AcknowledgeCompletion(ctx, g.OriginalClaimID); err == nil {
		t.Fatal("final ACK discarded uncleared resources")
	}
	f.removeOwned = nil
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.stepAttempts) != 1 {
		t.Fatal("cleanup erased pre-final audit")
	}
	if err := c.AcknowledgeCompletion(ctx, g.OriginalClaimID); err != nil {
		t.Fatal(err)
	}
}

func TestLiveStepAdmissionDelayAndMutationDoNotExecute(t *testing.T) {
	for _, kind := range []string{"delay", "runtime-claim", "step", "input", "generation", "claim"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			_, s, f, g := newStepSession(t, 2)
			r := stepIntent(t, s, g, 0)
			a := admitStep(t, s, r)
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "delay":
				a.NotBeforeUnixMilli = time.Now().Add(time.Minute).UnixMilli()
				a.ExpiresUnixMilli = time.Now().Add(2 * time.Minute).UnixMilli()
			case "runtime-claim":
				a.Scope.RuntimeClaimID = uuid.NewString()
			case "step":
				a.Scope.StepRunID = uuid.NewString()
			case "input":
				a.Scope.InputManifestDigest = "v1:" + strings.Repeat("f", 64)
			case "generation":
				a.Scope.GenerationID = uuid.NewString()
			case "claim":
				a.Scope.Identity.ClaimID = uuid.NewString()
			}
			a.BindingDigest, _ = a.Digest()
			if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); err == nil {
				t.Fatal("changed admission executed")
			}
			if f.live.executions != 0 || f.live.resumes != 0 {
				t.Fatal("changed admission reached runtime effect")
			}
		})
	}
}

func TestLiveStepConsumedSyncWorkspaceMutationDoesNotResume(t *testing.T) {
	ctx := context.Background()
	c, s, f, g := newStepSession(t, 3)
	a := admitStep(t, s, stepIntent(t, s, g, 0))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(a, s), a); !IsCommandExit(err) {
		t.Fatal(err)
	}
	next := admitStep(t, s, advanceStep(t, s, a))
	count := 0
	c.hook = func(point string) error {
		if point == "record.sync" {
			count++
			if count == 2 {
				if err := os.Rename(s.workspace, s.workspace+"-retained"); err != nil {
					return err
				}
				return os.Mkdir(s.workspace, 0700)
			}
		}
		return nil
	}
	if _, err := s.ExecuteStepAttempt(ctx, stepRequest(next, s), next); err == nil {
		t.Fatal("replaced workspace resumed after consumed write")
	}
	if f.live.resumes != 0 || f.live.executions != 1 {
		t.Fatal("workspace mutation crossed resume effect")
	}
}
