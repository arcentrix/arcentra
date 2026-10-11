package nova

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeliveryRetryNeverAdvancesBeforeSuccess(t *testing.T) {
	attempts := 0
	err := retryDelivery(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := retryDelivery(ctx, func() error { return errors.New("unavailable") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown not propagated: %v", err)
	}
}

func TestExplicitAgentQueueOverridesDefault(t *testing.T) {
	q := &TaskQueueImpl{priorityQueues: map[Priority]string{PriorityNormal: "SHARED_PRIORITY_NORMAL"}}
	if got := q.getQueueName("ARCENTRA_AGENT_abc_PRIORITY_NORMAL", PriorityNormal); got != "ARCENTRA_AGENT_ABC_PRIORITY_NORMAL" {
		t.Fatalf("agent routing ignored: %s", got)
	}
	if got := q.getQueueName("", PriorityNormal); got != "SHARED_PRIORITY_NORMAL" {
		t.Fatalf("default priority routing changed: %s", got)
	}
}

func TestPermanentDeliveryStopsRetry(t *testing.T) {
	attempts := 0
	err := retryDelivery(context.Background(), func() error { attempts++; return permanentDeliveryError{cause: errors.New("malformed envelope")} })
	if err == nil || attempts != 1 {
		t.Fatalf("poison message retried: %d %v", attempts, err)
	}
}
