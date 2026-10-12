package taskqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalEnrollmentPreflightDoesNotCreateJournal(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "not-created")
	if err := CheckTerminalOutboxForEnrollment(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only preflight created workspace: %v", err)
	}
}

func TestTerminalEnrollmentPreflightRejectsAnyExistingState(t *testing.T) {
	for _, kind := range []string{"empty-directory", "regular-file", "dangling-symlink", "bound-journal"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			directory := filepath.Join(workspace, ".terminal-report-outbox")
			switch kind {
			case "empty-directory":
				if err := os.Mkdir(directory, 0o750); err != nil {
					t.Fatal(err)
				}
			case "regular-file":
				if err := os.WriteFile(directory, []byte("incomplete journal"), 0o640); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(workspace, "missing"), directory); err != nil {
					t.Fatal(err)
				}
			case "bound-journal":
				outbox := openTestTerminalOutbox(t, directory)
				if err := outbox.put(context.Background(), terminalTestRecord(t, "step", "pending")); err != nil {
					t.Fatal(err)
				}
				if err := outbox.close(); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(directory)
			if err != nil {
				t.Fatal(err)
			}
			err = CheckTerminalOutboxForEnrollment(context.Background(), workspace)
			if err == nil || !strings.Contains(err.Error(), "original agent.id and grpc.token") {
				t.Fatalf("existing state did not block enrollment: %v", err)
			}
			after, err := os.Lstat(directory)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("read-only preflight changed journal state")
			}
			if kind == "bound-journal" {
				outbox := openTestTerminalOutbox(t, directory)
				defer outbox.close()
				if !outbox.has("step", "pending") {
					t.Fatal("blocked enrollment discarded pending report")
				}
			}
		})
	}
}

func TestTerminalEnrollmentPreflightFailsClosedOnInspectionError(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(workspace, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckTerminalOutboxForEnrollment(context.Background(), workspace); err == nil {
		t.Fatal("inspection error allowed enrollment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckTerminalOutboxForEnrollment(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight did not stop: %v", err)
	}
}

func TestTerminalEnrollmentPreflightUsesDefaultWorkspace(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.Mkdir(".terminal-report-outbox", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckTerminalOutboxForEnrollment(context.Background(), ""); err == nil {
		t.Fatal("default-workspace journal did not block enrollment")
	}
}
