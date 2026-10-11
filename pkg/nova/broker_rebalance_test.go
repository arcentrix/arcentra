package nova

import (
	"errors"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"testing"
)

func TestLostMembershipReturnsToPolling(t *testing.T) {
	for _, code := range []ckafka.ErrorCode{ckafka.ErrIllegalGeneration, ckafka.ErrUnknownMemberID, ckafka.ErrRebalanceInProgress, ckafka.ErrAssignmentLost, ckafka.ErrState} {
		if !lostConsumerAssignment(ckafka.NewError(code, "lost assignment", false)) {
			t.Fatalf("membership error %v would retry forever", code)
		}
	}
	if lostConsumerAssignment(errors.New("network temporarily unavailable")) {
		t.Fatal("ordinary errors must remain retryable")
	}
}
