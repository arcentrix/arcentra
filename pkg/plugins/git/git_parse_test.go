package git

import (
	"testing"
	"time"
)

func TestParseCommitRecords(t *testing.T) {
	stdout := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x1fAlice\x1falice@example.com\x1f2026-02-09T00:00:00+08:00\x1fmsg1\x1e" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\x1fBob\x1fbob@example.com\x1f2026-02-09T01:02:03Z\x1fmsg2\x1e"

	commits := parseCommitRecords(stdout)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if commits[0].CommitId != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected commit id: %s", commits[0].CommitId)
	}
	if commits[0].AuthorName != "Alice" {
		t.Fatalf("unexpected author: %s", commits[0].AuthorName)
	}
	if commits[0].Message != "msg1" {
		t.Fatalf("unexpected message: %s", commits[0].Message)
	}
	if commits[1].AuthorEmail != "bob@example.com" {
		t.Fatalf("unexpected email: %s", commits[1].AuthorEmail)
	}
	if commits[1].CommittedAt.IsZero() {
		t.Fatalf("expected committedAt to be set")
	}
}

func TestParseCommitLine(t *testing.T) {
	line := "cccccccccccccccccccccccccccccccccccccccc\x1fCarol\x1fcarol@example.com\x1f2026-02-09T01:02:03Z\x1fhello"
	commit, err := parseCommitLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commit.AuthorName != "Carol" {
		t.Fatalf("unexpected author: %s", commit.AuthorName)
	}
	if !commit.CommittedAt.Equal(time.Date(2026, 2, 9, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("unexpected time: %v", commit.CommittedAt)
	}
}
