// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

func testGeneration(t *testing.T, previous *taskpayload.JobGeneration) taskpayload.JobGeneration {
	t.Helper()
	o := durableOptions()
	g := taskpayload.JobGeneration{Version: 1, PipelineID: "pipeline-definition-one", PipelineRunID: o.PipelineRunID, JobRunID: o.JobRunID,
		AgentID: "agent-one", OriginalClaimID: o.ExecutionClaimID, RuntimeBindingDigest: o.RuntimeBindingDigest,
		InputManifestDigest: "v1:" + strings.Repeat("b", 64), RetryPlanDigest: "v1:" + strings.Repeat("c", 64),
		GenerationID: uuid.NewString(), RuntimeClaimID: uuid.NewString(), Number: 1, ExecutionAllowed: true}
	if previous != nil {
		g.Number = previous.Number + 1
		g.PreviousGenerationID = previous.GenerationID
	}
	bindGeneration(t, &g)
	return g
}
func bindGeneration(t *testing.T, g *taskpayload.JobGeneration) {
	t.Helper()
	var err error
	g.BindingDigest, err = g.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}
func allocateGeneration(t *testing.T, c *Controller, g taskpayload.JobGeneration) *Session {
	t.Helper()
	if g.Number == 1 {
		request := initialGenerationRequest(t, g)
		if err := c.RecordGenerationIntent(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if err := c.RecordGenerationAdmission(context.Background(), request, g); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.AllocateGeneration(context.Background(), durableOptions(), g)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func closeGeneration(t *testing.T, s *Session) taskpayload.JobGenerationCleanupReceipt {
	t.Helper()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.CleanupReceipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receipt.Digest(); err != nil {
		t.Fatal(err)
	}
	return receipt
}
func generationTransition(t *testing.T, g taskpayload.JobGeneration, receipt taskpayload.JobGenerationCleanupReceipt) taskpayload.JobGenerationTransition {
	t.Helper()
	r := taskpayload.JobGenerationTransition{Version: 1, Operation: "advance", Identity: taskpayload.JobInputIdentity{
		PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID,
		RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest},
		InputManifestDigest: "v1:" + strings.Repeat("b", 64), RetryPlanDigest: g.RetryPlanDigest, PreviousGenerationID: g.GenerationID,
		FailedStepRunID: "failed-step-one", FailedStepReceiptDigest: strings.Repeat("d", 64), CleanupReceipt: &receipt}
	r.RequestDigest, _ = r.Digest()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}
func admitSuccessor(t *testing.T, c *Controller, request taskpayload.JobGenerationTransition, generation taskpayload.JobGeneration) {
	t.Helper()
	if err := c.RecordGenerationIntent(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordGenerationAdmission(context.Background(), request, generation); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationFreshResourcesAndRetainedReceiptsUntilFinalACK(t *testing.T) {
	ctx := context.Background()
	backend := newDurableFake()
	c := openTestController(t, backend, controllerOptions(t))
	first := testGeneration(t, nil)
	s1 := allocateGeneration(t, c, first)
	if _, err := s1.CleanupReceipt(ctx); err == nil {
		t.Fatal("live generation produced cleanup receipt")
	}
	if err := s1.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s1.Workspace(), "previous-marker"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	r1 := closeGeneration(t, s1)
	if r1.SessionID != first.RuntimeClaimID || r1.ResourceOwnerDigest != s1.resourceRef.Owner || r1.WorkspaceInode == 0 {
		t.Fatal("cleanup lost exact identity")
	}
	request := generationTransition(t, first, r1)
	second := testGeneration(t, &first)
	if _, err := c.AllocateGeneration(ctx, durableOptions(), second); err == nil {
		t.Fatal("unadmitted successor allocated")
	}
	if err := c.RecordGenerationAdmission(ctx, request, second); err == nil {
		t.Fatal("response accepted without durable request")
	}
	if err := c.RecordGenerationIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	if c.Ready() {
		t.Fatal("ambiguous transition advertised readiness")
	}
	if err := c.RecordGenerationIntent(ctx, request); err != nil {
		t.Fatal("exact request replay failed", err)
	}
	changed := request
	changed.FailedStepRunID = "different-step"
	changed.RequestDigest, _ = changed.Digest()
	if err := c.RecordGenerationIntent(ctx, changed); err == nil {
		t.Fatal("request identity changed during ACK replay")
	}
	if err := c.RecordGenerationAdmission(ctx, request, second); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordGenerationAdmission(ctx, request, second); err != nil {
		t.Fatal("exact admission replay failed", err)
	}
	if !c.Ready() {
		t.Fatal("durable handoff did not resolve admission uncertainty")
	}
	s2 := allocateGeneration(t, c, second)
	if s1.Workspace() == s2.Workspace() || s1.resourceRef.ID == s2.resourceRef.ID || s1.resourceRef.Owner == s2.resourceRef.Owner {
		t.Fatal("generation reused prior resources")
	}
	if _, err := os.Stat(filepath.Join(s2.Workspace(), "previous-marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace state crossed generation", err)
	}
	if _, err := c.AllocateGeneration(ctx, durableOptions(), second); err == nil {
		t.Fatal("duplicate allocation succeeded")
	}
	if _, err := c.AllocateGeneration(ctx, durableOptions(), first); err == nil {
		t.Fatal("stale generation allocated")
	}
	if err := c.AcknowledgeCompletion(ctx, first.OriginalClaimID); err == nil {
		t.Fatal("final ACK deleted a live generation")
	}
	if len(c.records) != 2 {
		t.Fatal("intermediate handoff deleted audit evidence")
	}
	if err := s2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	r2 := closeGeneration(t, s2)
	if r1.GenerationID == r2.GenerationID || reflect.DeepEqual(r1, r2) {
		t.Fatal("distinct generation receipts lost")
	}
	if len(c.Recoveries()) != 2 {
		t.Fatal("generation recovery evidence lost")
	}
	if err := c.AcknowledgeCompletion(ctx, first.OriginalClaimID); err != nil {
		t.Fatal(err)
	}
	if c.HasJob(first.JobRunID) || len(c.records) != 0 {
		t.Fatal("real final ACK did not release all generations")
	}
	finishController(t, c, nil)
}

func TestGenerationPreparationOnlyAndImmutablePredecessorBinding(t *testing.T) {
	ctx := context.Background()
	c := openTestController(t, newDurableFake(), controllerOptions(t))
	first := testGeneration(t, nil)
	first.InputManifestDigest = ""
	first.PreparationOnly = true
	first.ExecutionAllowed = true
	bindGeneration(t, &first)
	readOnly := first
	readOnly.ExecutionAllowed = false
	if _, err := c.AllocateGeneration(ctx, durableOptions(), readOnly); err == nil {
		t.Fatal("read-only preparation observation allowed allocation")
	}
	s1 := allocateGeneration(t, c, first)
	r := closeGeneration(t, s1)
	request := generationTransition(t, first, r)
	second := testGeneration(t, &first)
	admitSuccessor(t, c, request, second)
	s2 := allocateGeneration(t, c, second)
	if c.records[s1.recordID].Generation.BindingDigest != first.BindingDigest || c.records[s1.recordID].Generation.InputManifestDigest != "" {
		t.Fatal("pinned inputs rewrote original generation binding")
	}
	closeGeneration(t, s2)
	if err := c.AcknowledgeCompletion(ctx, first.OriginalClaimID); err != nil {
		t.Fatal(err)
	}
	finishController(t, c, nil)
}

func TestGenerationRejectsChangedAdmissionAndExactCleanup(t *testing.T) {
	for _, kind := range []string{"claim", "agent", "runtime", "plan", "inputs", "number", "previous", "owner", "workspace", "absence"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c := openTestController(t, newDurableFake(), controllerOptions(t))
			first := testGeneration(t, nil)
			s := allocateGeneration(t, c, first)
			receipt := closeGeneration(t, s)
			request := generationTransition(t, first, receipt)
			next := testGeneration(t, &first)
			defer finishController(t, c, nil)
			switch kind {
			case "owner":
				request.CleanupReceipt.ResourceOwnerDigest = strings.Repeat("e", 64)
			case "workspace":
				request.CleanupReceipt.WorkspaceInode++
			case "absence":
				request.CleanupReceipt.ProcessesAbsent = false
			default:
				if err := c.RecordGenerationIntent(ctx, request); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "claim":
					next.OriginalClaimID = "foreign-claim"
				case "agent":
					next.AgentID = "foreign-agent"
				case "runtime":
					next.RuntimeBindingDigest = "v1:" + strings.Repeat("e", 64)
				case "plan":
					next.RetryPlanDigest = "v1:" + strings.Repeat("e", 64)
				case "inputs":
					next.InputManifestDigest = "v1:" + strings.Repeat("e", 64)
				case "number":
					next.Number++
				case "previous":
					next.PreviousGenerationID = uuid.NewString()
				}
				bindGeneration(t, &next)
				if err := c.RecordGenerationAdmission(ctx, request, next); err == nil {
					t.Fatal("mismatched admission accepted")
				}
				return
			}
			request.RequestDigest, _ = request.Digest()
			if err := c.RecordGenerationIntent(ctx, request); err == nil {
				t.Fatal("mismatched cleanup receipt accepted")
			}
		})
	}
}

func TestGenerationRestartCleansNeverExecutesAdmittedSuccessor(t *testing.T) {
	for _, boundary := range []string{"active", "closed", "intent", "admitted", "allocated-successor", "active-successor", "removed-runtime", "removed-workspace"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			first := testGeneration(t, nil)
			session := allocateGeneration(t, c, first)
			if err := session.Start(ctx); err != nil {
				t.Fatal(err)
			}
			next := testGeneration(t, &first)
			if boundary == "removed-runtime" || boundary == "removed-workspace" {
				if err := session.advance(ctx, phaseRemoving); err != nil {
					t.Fatal(err)
				}
				if err := backend.RemoveOwned(ctx, session.resourceRef); err != nil {
					t.Fatal(err)
				}
				if boundary == "removed-workspace" {
					if err := c.workspaceRoot.RemoveAll(session.directory); err != nil {
						t.Fatal(err)
					}
				}
			} else if boundary != "active" {
				receipt := closeGeneration(t, session)
				req := generationTransition(t, first, receipt)
				if boundary != "closed" {
					if err := c.RecordGenerationIntent(ctx, req); err != nil {
						t.Fatal(err)
					}
				}
				if boundary != "closed" && boundary != "intent" {
					if err := c.RecordGenerationAdmission(ctx, req, next); err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "allocated-successor" || boundary == "active-successor" {
					session = allocateGeneration(t, c, next)
					if boundary == "active-successor" {
						if err := session.Start(ctx); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			starts, creates := countEvent(backend.events, "start"), countEvent(backend.events, "create")
			simulateCrash(t, c)
			reopened := openTestController(t, backend, opts)
			if reopened.Ready() {
				t.Fatal("restart discarded unresolved generation outcome")
			}
			if countEvent(backend.events, "start") != starts || countEvent(backend.events, "create") != creates {
				t.Fatal("restart executed an admitted generation")
			}
			if _, err := reopened.AllocateGeneration(ctx, durableOptions(), next); err == nil {
				t.Fatal("restart resumed execution")
			}
			if _, err := os.Stat(session.Workspace()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("recovery left owned workspace", err)
			}
			if len(reopened.Recoveries()) == 0 {
				t.Fatal("recovery lost evidence")
			}
			if err := reopened.AcknowledgeCompletion(ctx, first.OriginalClaimID); err != nil {
				t.Fatal(err)
			}
			finishController(t, reopened, nil)
		})
	}
}

type incompleteGenerationCleanup struct {
	*durableFake
	remaining sandbox.ResourceState
}

func (b *incompleteGenerationCleanup) RemoveOwned(_ context.Context, ref sandbox.ResourceRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[ref.ID] = b.remaining
	return nil
}
func TestGenerationCleanupIndependentlyChecksEveryResource(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining sandbox.ResourceState
	}{
		{"container", sandbox.ResourceState{ContainerExists: true}},
		{"task", sandbox.ResourceState{TaskExists: true}},
		{"snapshot", sandbox.ResourceState{SnapshotExists: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			backend := &incompleteGenerationCleanup{newDurableFake(), tc.remaining}
			c, err := OpenController(ctx, backend, controllerOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			s := allocateGeneration(t, c, testGeneration(t, nil))
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(ctx); err == nil {
				t.Fatal("backend success trusted despite remaining resources")
			}
			if _, err := s.CleanupReceipt(ctx); err == nil {
				t.Fatal("incomplete cleanup produced a receipt")
			}
			if c.Ready() {
				t.Fatal("incomplete cleanup advertised readiness")
			}
			if c.records[s.recordID].CleanupReceipt != nil {
				t.Fatal("false cleanup evidence persisted")
			}
			backend.remaining = sandbox.ResourceState{}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			finishController(t, c, nil)
		})
	}
}

func TestGenerationCleanupFaultPreservesEvidenceAndAllowsExactCleanupRetry(t *testing.T) {
	for _, point := range []string{"workspace.remove", "workspace.sync", "cleanup.verify"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			c := openTestController(t, newDurableFake(), controllerOptions(t))
			g := testGeneration(t, nil)
			s := allocateGeneration(t, c, g)
			c.hook = func(p string) error {
				if p == point {
					return errors.New("injected cleanup failure")
				}
				return nil
			}
			if err := s.Close(ctx); err == nil {
				t.Fatal("cleanup fault ignored")
			}
			if c.records[s.recordID].CleanupReceipt != nil {
				t.Fatal("failed cleanup had success receipt")
			}
			c.hook = nil
			receipt := closeGeneration(t, s)
			if receipt.GenerationID != g.GenerationID {
				t.Fatal("cleanup retry changed owner")
			}
			finishController(t, c, nil)
		})
	}
}

func TestGenerationOldJournalCanonicalCompatibilityAndDowngradeFailsClosed(t *testing.T) {
	ctx := context.Background()
	opts := controllerOptions(t)
	backend := newDurableFake()
	c := openTestController(t, backend, opts)
	legacy := allocateTestSession(t, c, durableOptions())
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.JournalRoot, legacy.recordID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old journalDocument
	if err := sonic.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if old.Version != 1 {
		t.Fatal("legacy journal was needlessly upgraded")
	}
	simulateCrash(t, c)
	c = openTestController(t, backend, opts)
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("legacy terminal evidence was rewritten")
	}
	if err := c.AcknowledgeCompletion(ctx, "claim-one"); err != nil {
		t.Fatal(err)
	}
	g := testGeneration(t, nil)
	s := allocateGeneration(t, c, g)
	closeGeneration(t, s)
	path = filepath.Join(opts.JournalRoot, s.recordID+".json")
	data, _ := os.ReadFile(path)
	var doc journalDocument
	if err := sonic.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 2 {
		t.Fatal("generation journal has ambiguous legacy version")
	}
	doc.Version = 1
	data, err = encodeJournal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, c)
	removes := countEvent(backend.events, "remove-owned")
	if reopened, err := OpenController(ctx, backend, opts); err == nil || reopened != nil {
		t.Fatal("downgraded generation record accepted")
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(data, after) || countEvent(backend.events, "remove-owned") != removes {
		t.Fatal("ambiguous version modified evidence or resources")
	}
}

func initialGenerationRequest(t *testing.T, g taskpayload.JobGeneration) taskpayload.JobGenerationTransition {
	t.Helper()
	r := taskpayload.JobGenerationTransition{Version: 1, Operation: "admit", Identity: taskpayload.JobInputIdentity{
		PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID,
		RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest}, InputManifestDigest: g.InputManifestDigest, RetryPlanDigest: g.RetryPlanDigest}
	r.RequestDigest, _ = r.Digest()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGenerationInitialIntentCrashWindowsPreserveOriginalClaim(t *testing.T) {
	for _, boundary := range []string{"before-rpc", "after-rpc-before-save", "admitted", "inspection-only"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			g := testGeneration(t, nil)
			request := initialGenerationRequest(t, g)
			if _, err := c.AllocateGeneration(ctx, durableOptions(), g); err == nil {
				t.Fatal("initial allocation skipped durable RPC handoff")
			}
			if err := c.RecordGenerationAdmission(ctx, request, g); err == nil {
				t.Fatal("initial response skipped durable request")
			}
			if err := c.RecordGenerationIntent(ctx, request); err != nil {
				t.Fatal(err)
			}
			if !c.HasJob(g.JobRunID) || c.Ready() {
				t.Fatal("pending admission was not treated as owned unresolved work")
			}
			if boundary == "admitted" {
				if err := c.RecordGenerationAdmission(ctx, request, g); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "inspection-only" {
				observed := g
				observed.ExecutionAllowed = false
				if err := c.RecordGenerationAdmission(ctx, request, observed); err != nil {
					t.Fatal(err)
				}
				if c.Ready() {
					t.Fatal("inspection made runtime ready")
				}
			}
			simulateCrash(t, c)
			c = openTestController(t, backend, opts)
			if c.Ready() || countEvent(backend.events, "prepare") != 0 || len(c.records) != 0 {
				t.Fatal("admission-only recovery fabricated or executed resources")
			}
			recoveries := c.Recoveries()
			if len(recoveries) != 1 || recoveries[0].ExecutionClaimID != g.OriginalClaimID || !recoveries[0].CleanupComplete || recoveries[0].CleanupReceipt != nil {
				t.Fatalf("initial handoff recovery lost exact identity: %+v", recoveries)
			}
			if _, err := c.AllocateGeneration(ctx, durableOptions(), g); err == nil {
				t.Fatal("restart ran admitted first generation")
			}
			if err := c.RecordGenerationAdmission(ctx, request, g); err == nil {
				t.Fatal("restart turned initial handoff into execution authority")
			}
			if err := c.AcknowledgeCompletion(ctx, g.OriginalClaimID); err != nil {
				t.Fatal(err)
			}
			if c.HasJob(g.JobRunID) || !c.Ready() || len(c.admissions) != 0 {
				t.Fatal("final ACK failed to reconcile admission-only record")
			}
			finishController(t, c, nil)
		})
	}
}

func TestGenerationFinalACKPartialDeletionRemainsRecoverable(t *testing.T) {
	for _, point := range []string{"record.remove", "admission.remove"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			g1 := testGeneration(t, nil)
			s1 := allocateGeneration(t, c, g1)
			r1 := closeGeneration(t, s1)
			g2 := testGeneration(t, &g1)
			admitSuccessor(t, c, generationTransition(t, g1, r1), g2)
			s2 := allocateGeneration(t, c, g2)
			closeGeneration(t, s2)
			calls := 0
			c.hook = func(p string) error {
				if p == point {
					calls++
					if point == "admission.remove" || calls == 2 {
						return errors.New("lost final ACK journal removal")
					}
				}
				return nil
			}
			if err := c.AcknowledgeCompletion(ctx, g1.OriginalClaimID); err == nil {
				t.Fatal("injected partial ACK deletion succeeded")
			}
			for _, r := range c.records {
				if !r.FinalAcknowledged {
					t.Fatal("partial deletion lost authoritative final ACK marker")
				}
			}
			simulateCrash(t, c)
			c = openTestController(t, backend, opts)
			if c.Ready() {
				t.Fatal("partial final ACK deletion silently discarded evidence")
			}
			if err := c.AcknowledgeCompletion(ctx, g1.OriginalClaimID); err != nil {
				t.Fatal(err)
			}
			if !c.Ready() || c.HasJob(g1.JobRunID) {
				t.Fatal("replayed final ACK failed to finish owned cleanup")
			}
			finishController(t, c, nil)
		})
	}
}

func TestGenerationHandoffPersistenceFaultsNeverAllocate(t *testing.T) {
	for _, boundary := range []string{"initial-intent", "initial-response", "successor-intent", "successor-response"} {
		for _, point := range []string{"record.write", "record.sync", "record.rename", "directory.sync"} {
			t.Run(boundary+"/"+point, func(t *testing.T) {
				ctx := context.Background()
				backend := newDurableFake()
				opts := controllerOptions(t)
				c := openTestController(t, backend, opts)
				g := testGeneration(t, nil)
				request := initialGenerationRequest(t, g)
				if boundary == "initial-response" {
					if err := c.RecordGenerationIntent(ctx, request); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(boundary, "successor") {
					s := allocateGeneration(t, c, g)
					receipt := closeGeneration(t, s)
					request = generationTransition(t, g, receipt)
					g = testGeneration(t, &g)
					if boundary == "successor-response" {
						if err := c.RecordGenerationIntent(ctx, request); err != nil {
							t.Fatal(err)
						}
					}
				}
				c.hook = func(p string) error {
					if p == point {
						return errors.New("injected handoff durability failure")
					}
					return nil
				}
				var err error
				if strings.HasSuffix(boundary, "intent") {
					err = c.RecordGenerationIntent(ctx, request)
				} else {
					err = c.RecordGenerationAdmission(ctx, request, g)
				}
				if err == nil {
					t.Fatal("durability fault was ignored")
				}
				if c.Ready() {
					t.Fatal("uncertain persistence advertised readiness")
				}
				if _, err := c.AllocateGeneration(ctx, durableOptions(), g); err == nil {
					t.Fatal("persistence failure allowed allocation")
				}
				starts := countEvent(backend.events, "start")
				simulates := countEvent(backend.events, "create")
				simulateCrash(t, c)
				reopened, err := OpenController(ctx, backend, opts)
				if point == "directory.sync" {
					if err != nil {
						t.Fatal("fully renamed record failed safe recovery", err)
					}
					if reopened.Ready() {
						t.Fatal("restart admitted work from uncertain handoff")
					}
					finishController(t, reopened, nil)
				} else if err == nil || reopened != nil {
					t.Fatal("interrupted staging file was silently discarded")
				}
				if countEvent(backend.events, "start") != starts || countEvent(backend.events, "create") != simulates {
					t.Fatal("recovery executed uncertain admission")
				}
			})
		}
	}
}

func TestGenerationRevalidatesRetainedEvidenceBeforeAllocation(t *testing.T) {
	for _, target := range []string{"initial-admission", "predecessor-record", "workspace-root", "workspace-root-during-inspect", "reappeared-workspace", "reappeared-snapshot"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			first := testGeneration(t, nil)
			s := allocateGeneration(t, c, first)
			receipt := closeGeneration(t, s)
			request := generationTransition(t, first, receipt)
			second := testGeneration(t, &first)
			admitSuccessor(t, c, request, second)
			switch target {
			case "initial-admission":
				if err := os.WriteFile(filepath.Join(opts.JournalRoot, admissionFilenameForGeneration(first)), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "predecessor-record":
				if err := os.WriteFile(filepath.Join(opts.JournalRoot, s.recordID+".json"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "workspace-root":
				if err := os.Rename(opts.WorkspaceRoot, opts.WorkspaceRoot+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(opts.WorkspaceRoot, 0700); err != nil {
					t.Fatal(err)
				}
			case "workspace-root-during-inspect":
				replaced := false
				backend.inspect = func(context.Context, sandbox.ResourceRef) error {
					if replaced {
						return nil
					}
					replaced = true
					if err := os.Rename(opts.WorkspaceRoot, opts.WorkspaceRoot+"-original"); err != nil {
						return err
					}
					return os.Mkdir(opts.WorkspaceRoot, 0700)
				}
			case "reappeared-workspace":
				if err := os.Mkdir(s.Workspace(), 0700); err != nil {
					t.Fatal(err)
				}
			case "reappeared-snapshot":
				backend.states[s.resourceRef.ID] = sandbox.ResourceState{SnapshotExists: true}
			}
			before := countEvent(backend.events, "prepare")
			if _, err := c.AllocateGeneration(ctx, durableOptions(), second); err == nil {
				t.Fatal("changed ownership evidence allowed successor")
			}
			if c.Ready() || countEvent(backend.events, "prepare") != before {
				t.Fatal("changed evidence did not fail before resource allocation")
			}
			finishController(t, c, nil)
		})
	}
}

func TestGenerationInspectionDoesNotAuthorizeSuccessor(t *testing.T) {
	ctx := context.Background()
	backend := newDurableFake()
	c := openTestController(t, backend, controllerOptions(t))
	g := testGeneration(t, nil)
	s := allocateGeneration(t, c, g)
	receipt := closeGeneration(t, s)
	req := generationTransition(t, g, receipt)
	next := testGeneration(t, &g)
	if err := c.RecordGenerationIntent(ctx, req); err != nil {
		t.Fatal(err)
	}
	observed := next
	observed.ExecutionAllowed = false
	if err := c.RecordGenerationAdmission(ctx, req, observed); err != nil {
		t.Fatal(err)
	}
	if c.Ready() {
		t.Fatal("inspection bypassed pending admission")
	}
	if _, err := c.AllocateGeneration(ctx, durableOptions(), observed); err == nil {
		t.Fatal("inspection-only descriptor allocated")
	}
	if _, err := c.AllocateGeneration(ctx, durableOptions(), next); err == nil {
		t.Fatal("caller flipped inspection authority bit")
	}
	if err := c.RecordGenerationAdmission(ctx, req, next); err != nil {
		t.Fatal(err)
	}
	s2 := allocateGeneration(t, c, next)
	closeGeneration(t, s2)
	if c.records[s.recordID].GenerationObservation == nil {
		t.Fatal("runnable response discarded prior inspection evidence")
	}
	finishController(t, c, nil)
}

func TestGenerationConcurrentDuplicateAllocationHasOneOwner(t *testing.T) {
	ctx := context.Background()
	backend := newDurableFake()
	c := openTestController(t, backend, controllerOptions(t))
	g := testGeneration(t, nil)
	request := initialGenerationRequest(t, g)
	if err := c.RecordGenerationIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordGenerationAdmission(ctx, request, g); err != nil {
		t.Fatal(err)
	}
	type result struct {
		session *Session
		err     error
	}
	results := make(chan result, 8)
	for range 8 {
		safe.Go(func() { s, err := c.AllocateGeneration(ctx, durableOptions(), g); results <- result{s, err} })
	}
	var owner *Session
	successes := 0
	for range 8 {
		r := <-results
		if r.err == nil {
			owner = r.session
			successes++
		} else if r.session != nil {
			t.Fatal("failed concurrent allocation returned another session")
		}
	}
	if successes != 1 || len(c.records) != 1 || countEvent(backend.events, "prepare") != 1 {
		t.Fatal("concurrent duplicate allocation created multiple owners")
	}
	closeGeneration(t, owner)
	finishController(t, c, nil)
}
