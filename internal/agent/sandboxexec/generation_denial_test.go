// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package sandboxexec

import (
	"context"
	"reflect"
	"testing"
)

func TestRejectedAdvanceObservationPersistsWithoutResumeAuthority(t *testing.T) {
	ctx := context.Background()
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	previous := testGeneration(t, nil)
	session := allocateGeneration(t, c, previous)
	receipt := closeGeneration(t, session)
	request := generationTransition(t, previous, receipt)
	if err := c.RecordGenerationIntent(ctx, request); err != nil {
		t.Fatal(err)
	}
	observed := previous
	observed.ExecutionAllowed = false
	observed.PreparationOnly = false
	if err := c.RecordGenerationAdmission(ctx, request, observed); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordGenerationAdmission(ctx, request, observed); err != nil {
		t.Fatal("identical observation replay failed", err)
	}
	record := c.records[session.recordID]
	if record.TransitionIntent == nil || !reflect.DeepEqual(*record.TransitionIntent, request) || record.CleanupReceipt == nil || record.GenerationObservation == nil || *record.GenerationObservation != observed || record.SuccessorAdmission != nil || record.FinalAcknowledged {
		t.Fatal("observation removed or rewrote original evidence")
	}
	simulateCrash(t, c)
	reopened := openTestController(t, backend, opts)
	if reopened.Ready() {
		t.Fatal("restart treated rejection observation as completed execution")
	}
	next := testGeneration(t, &previous)
	if _, err := reopened.AllocateGeneration(ctx, durableOptions(), next); err == nil {
		t.Fatal("restart resumed rejected successor")
	}
	if err := reopened.RecordGenerationIntent(ctx, request); err == nil {
		t.Fatal("restart replayed rejected transition as live execution")
	}
	if len(reopened.Recoveries()) == 0 {
		t.Fatal("restart lost transition evidence")
	}
	// 模拟已单独验证的原身份最终终态 ACK；观察本身绝不调用此入口
	if err := reopened.AcknowledgeCompletion(ctx, previous.OriginalClaimID); err != nil {
		t.Fatal(err)
	}
	if reopened.HasJob(previous.JobRunID) {
		t.Fatal("verified final ACK did not release original journal")
	}
	finishController(t, reopened, nil)
}
