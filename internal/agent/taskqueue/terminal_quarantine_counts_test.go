package taskqueue

import "testing"

func TestTerminalQuarantineCountsDoNotTreatReviewAsAcceptance(t *testing.T) {
	outbox := &terminalOutbox{entries: map[string]terminalEntry{
		"pending":    {record: terminalRecord{}},
		"unreviewed": {record: terminalRecord{Quarantine: &terminalQuarantine{}}},
		"reviewed":   {record: terminalRecord{Quarantine: &terminalQuarantine{Reviews: []TerminalQuarantineReview{{Disposition: "investigated"}}}}},
	}}
	total, unreviewed := outbox.quarantineCounts()
	if total != 2 || unreviewed != 1 {
		t.Fatalf("got %d/%d want 2/1", total, unreviewed)
	}
	if len(outbox.entries) != 3 {
		t.Fatal("inspection changed retained records")
	}
}
