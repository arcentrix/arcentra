package service

import (
	"context"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/metadata"
	"testing"
	"time"
)

func TestAgentExecutionStatusBoundaries(t *testing.T) {
	for _, tc := range []struct {
		current, next int
		step, valid   bool
	}{
		{2, 3, false, true}, {3, 4, false, true}, {3, 5, false, true}, {3, 6, false, true}, {3, 7, false, true},
		{3, 8, true, true}, {3, 8, false, false}, {4, 3, false, false}, {6, 4, false, false}, {4, 4, false, true}, {2, 0, false, false},
	} {
		err := validateReportedStatus(tc.current, tc.next, tc.step)
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if _, err := authenticatedAgent(context.Background(), "a"); err == nil {
		t.Fatal("missing authentication accepted")
	}
	ctx := agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: "a"})
	if _, err := authenticatedAgent(ctx, "b"); err == nil {
		t.Fatal("another agent accepted")
	}
	if _, err := authenticatedAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(100, 0)
	updates := executionStatusUpdates(4, "", 0, 103, &start)
	if updates["duration"] != int64(3000) {
		t.Fatalf("unexpected duration: %v", updates)
	}
}

func TestRunningClaimRetryRequiresSameAttempt(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-arcentra-claim-id", "attempt-a"))
	if claim, retry, err := reportClaim(ctx, 2, 3, ""); err != nil || retry || claim != "attempt-a" {
		t.Fatalf("initial claim: %s %v %v", claim, retry, err)
	}
	if _, retry, err := reportClaim(ctx, 3, 3, "attempt-a"); err != nil || !retry {
		t.Fatalf("lost response retry rejected: %v", err)
	}
	if _, _, err := reportClaim(ctx, 3, 3, "attempt-b"); err == nil {
		t.Fatal("another attempt took over running job")
	}
	if _, _, err := reportClaim(context.Background(), 2, 3, ""); err == nil {
		t.Fatal("missing claim accepted")
	}
	if _, _, err := reportClaim(ctx, 3, 4, "attempt-b"); err == nil {
		t.Fatal("stale attempt reported terminal state")
	}
}
