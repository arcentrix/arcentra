//go:build unix

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
)

type durableLogTransportFunc func(context.Context, *logstream.BuildLogMessage) error

func (f durableLogTransportFunc) Publish(ctx context.Context, msg *logstream.BuildLogMessage) error {
	return f(ctx, msg)
}

func durableTestTransport() LogPublisher {
	return durableLogTransportFunc(func(ctx context.Context, _ *logstream.BuildLogMessage) error { <-ctx.Done(); return ctx.Err() })
}

func newDurableTestPublisher(t *testing.T, transport LogPublisher, path string, limit int64) *DurableLogPublisher {
	t.Helper()
	p, err := NewDurableLogPublisher(context.Background(), transport, path, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Errorf("close publisher: %v", err)
		}
	})
	return p
}

func durableTestRecords(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".record") {
			names = append(names, filepath.Join(path, entry.Name()))
		}
	}
	return names
}

func waitDurable(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for durable log delivery")
}

func TestDurableLogPublisherPersistThenReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	original := &logstream.BuildLogMessage{StepRunID: "step-test", Content: "token=***", Stream: "stdout"}
	for _, content := range []string{original.Content, "second line"} {
		msg := *original
		msg.Content = content
		if err := p.Publish(ctx, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.EventID != "" {
			t.Fatal("publisher mutated caller message")
		}
	}
	files := durableTestRecords(t, path)
	if len(files) != 2 {
		t.Fatalf("records = %d", len(files))
	}
	var ids []string
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("record permissions = %v", info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record durableLogRecord
		if err := sonic.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Message.EventID == "" {
			t.Fatal("record has no event ID")
		}
		ids = append(ids, record.Message.EventID)
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct records share event ID")
	}
	for _, dir := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("directory permissions = %v", info.Mode().Perm())
		}
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan logstream.BuildLogMessage, 2)
	second := newDurableTestPublisher(t, durableLogTransportFunc(func(_ context.Context, msg *logstream.BuildLogMessage) error { got <- *msg; return nil }), path, 1<<20)
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"token=***", "second line"} {
		select {
		case msg := <-got:
			if msg.Content != content || msg.EventID != ids[i] {
				t.Errorf("replay changed record: %+v", msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("replay did not deliver")
		}
	}
	waitDurable(t, func() bool { return len(durableTestRecords(t, path)) == 0 })
}

func TestDurableLogPublisherCrashRecovery(t *testing.T) {
	const childPathKey = "ARCENTRA_TEST_DURABLE_LOG_CHILD_PATH"
	if path := os.Getenv(childPathKey); path != "" {
		p, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", EventID: "crash-record", Content: "persisted before process exit"}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // 模拟没有 Close 的进程退出，锁由操作系统释放
	}
	path := filepath.Join(t.TempDir(), "logs")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDurableLogPublisherCrashRecovery$")
	cmd.Env = append(os.Environ(), childPathKey+"="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child exited: %v: %s", err, output)
	}
	got := make(chan logstream.BuildLogMessage, 1)
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(_ context.Context, msg *logstream.BuildLogMessage) error { got <- *msg; return nil }), path, 1<<20)
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if msg.EventID != "crash-record" {
			t.Errorf("unexpected replay ID: %s", msg.EventID)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitDurable(t, func() bool { return len(durableTestRecords(t, path)) == 0 })
}

func TestDurableLogPublisherRetainsUntilBrokerAcknowledgment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(ctx context.Context, _ *logstream.BuildLogMessage) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not start")
	}
	if len(durableTestRecords(t, path)) != 1 {
		t.Fatal("record deleted before broker ACK")
	}
	close(release)
	waitDurable(t, func() bool { return len(durableTestRecords(t, path)) == 0 })
}

func TestDurableLogPublisherAckLossReplaysSameIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs")
	attempts := make(chan string, 4)
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(_ context.Context, msg *logstream.BuildLogMessage) error {
		attempts <- msg.EventID
		return errors.New("broker accepted but ACK was lost")
	}), path, 1<<20)
	if err := p.Publish(ctx, &logstream.BuildLogMessage{StepRunID: "step-test", Content: "once logically"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var first string
	select {
	case first = <-attempts:
	case <-time.After(5 * time.Second):
		t.Fatal("no first attempt")
	}
	select {
	case again := <-attempts:
		if again != first {
			t.Fatal("retry changed identity")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed transport was not retried")
	}
	waitDurable(t, func() bool { return p.LastError() != nil })
	if len(durableTestRecords(t, path)) != 1 {
		t.Fatal("failed ACK discarded record")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 1)
	restarted := newDurableTestPublisher(t, durableLogTransportFunc(func(_ context.Context, msg *logstream.BuildLogMessage) error { delivered <- msg.EventID; return nil }), path, 1<<20)
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-delivered:
		if id != first {
			t.Fatal("restart changed identity")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not replay")
	}
	waitDurable(t, func() bool { return len(durableTestRecords(t, path)) == 0 && restarted.LastError() == nil })
}

func TestDurableLogPublisherBoundsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1)
	msg := &logstream.BuildLogMessage{StepRunID: "step-test", Content: "too large"}
	if err := p.Publish(context.Background(), msg); !errors.Is(err, ErrBuildLogSpoolFull) {
		t.Fatalf("full result = %v", err)
	}
	if len(durableTestRecords(t, path)) != 0 {
		t.Fatal("full spool wrote a record")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Publish(ctx, msg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result = %v", err)
	}
	if err := p.Publish(context.Background(), nil); err == nil {
		t.Fatal("nil message accepted")
	}
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", EventID: "invalid/id"}); err == nil {
		t.Fatal("invalid event ID accepted")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(context.Background(), msg); !errors.Is(err, ErrBuildLogSpoolClosed) {
		t.Fatalf("closed result = %v", err)
	}
}

func TestDurableLogPublisherBudgetReclaimedOnlyAfterAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(context.Context, *logstream.BuildLogMessage) error { return nil }), path, 1<<20)
	msg := &logstream.BuildLogMessage{StepRunID: "step-test", EventID: "same-length", Content: "bounded"}
	if err := p.Publish(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.maxBytes = p.bytes
	p.mu.Unlock()
	if err := p.Publish(context.Background(), msg); !errors.Is(err, ErrBuildLogSpoolFull) {
		t.Fatalf("second append = %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitDurable(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.bytes == 0 })
	if err := p.Publish(context.Background(), msg); err != nil {
		t.Fatalf("budget not reclaimed: %v", err)
	}
}

func TestDurableLogPublisherExclusiveLockAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	if second, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20); err == nil {
		second.Close(context.Background())
		t.Fatal("second writer acquired spool")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
}

func TestDurableLogPublisherRejectsSymlinksAndUnexpectedFiles(t *testing.T) {
	for _, kind := range []string{"directory", "ancestor", "record", "lock", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "logs")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				link := filepath.Join(base, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, "logs")
			case "record", "lock":
				name := ".lock"
				if kind == "record" {
					name = "00000000000000000001.record"
				}
				if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(path, name)); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.WriteFile(filepath.Join(path, "unexpected"), []byte("keep me"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20)
			if err == nil {
				p.Close(context.Background())
				t.Fatal("unsafe spool accepted")
			}
		})
	}
}

func TestDurableLogPublisherCorruptRecordIsNotDiscarded(t *testing.T) {
	for _, corrupt := range []string{"invalid-json", "checksum"} {
		t.Run(corrupt, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "logs")
			p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
			if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "original"}); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			file := durableTestRecords(t, path)[0]
			data := []byte("broken-json")
			if corrupt == "checksum" {
				var err error
				data, err = os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.ReplaceAll(string(data), "original", "tampered"))
			}
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if recovered, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20); err == nil {
				recovered.Close(context.Background())
				t.Fatal("corrupt record accepted")
			}
			remaining, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(remaining) != string(data) {
				t.Fatal("corrupt record modified or discarded")
			}
		})
	}
}

func TestDurableLogPublisherSynchronousDurabilityAndDiskFailures(t *testing.T) {
	for _, failure := range []string{"none", "file-sync", "directory-sync"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "logs")
			p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
			var order []string
			p.syncFile = func(file *os.File) error {
				info, err := file.Stat()
				if err != nil {
					return err
				}
				kind := "file"
				if info.IsDir() {
					kind = "directory"
				}
				order = append(order, kind)
				if kind+"-sync" == failure {
					return syscall.ENOSPC
				}
				return file.Sync()
			}
			err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", EventID: "stable-disk-failure", Content: "redacted ***"})
			if failure == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Join(order, ",") != "file,directory" {
					t.Fatalf("sync order = %v", order)
				}
				return
			}
			if !errors.Is(err, syscall.ENOSPC) || p.LastError() == nil {
				t.Fatalf("disk failure not surfaced: %v", err)
			}
			if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "do not grow failed spool"}); err == nil {
				t.Fatal("failed spool accepted more records")
			}
			want := 0
			if failure == "directory-sync" {
				want = 1
			}
			if count := len(durableTestRecords(t, path)); count != want {
				t.Fatalf("records after failure = %d, want %d", count, want)
			}
			if err := p.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if want == 1 {
				got := make(chan string, 1)
				recovered := newDurableTestPublisher(t, durableLogTransportFunc(func(_ context.Context, msg *logstream.BuildLogMessage) error { got <- msg.EventID; return nil }), path, 1<<20)
				if err := recovered.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				select {
				case id := <-got:
					if id != "stable-disk-failure" {
						t.Fatal("failed commit identity changed")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("committed record not recovered")
				}
			}
		})
	}
}

func TestDurableLogPublisherRecoversInterruptedTemporaryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(path, "00000000000000000001.record.tmp")
	if err := os.WriteFile(tmp, []byte("partial unacknowledged append"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete temp file retained: %v", err)
	}
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "new append"}); err != nil {
		t.Fatal(err)
	}
}

func TestDurableLogPublisherCloseCancelsInFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	started := make(chan struct{})
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(ctx context.Context, _ *logstream.BuildLogMessage) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "retained at shutdown"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(durableTestRecords(t, path)) != 1 {
		t.Fatal("shutdown lost pending record")
	}
	_ = newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
}

func TestDurableLogPublisherConcurrentPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step-test", Content: "parallel"}); err != nil {
				t.Errorf("publish: %v", err)
			}
		})
	}
	wg.Wait()
	if count := len(durableTestRecords(t, path)); count != 25 {
		t.Errorf("stored %d records", count)
	}
}

func TestDurableLogPublisherCloseTimeoutKeepsLockUntilTransportExits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(context.Context, *logstream.BuildLogMessage) error {
		close(entered)
		<-release // 模拟不响应取消的错误传输实现
		return errors.New("still unacknowledged")
	}), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step", Content: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close timeout = %v", err)
	}
	if second, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20); err == nil {
		second.Close(context.Background())
		t.Fatal("timed-out close released active spool lock")
	}
	close(release)
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
}

func TestDurableLogPublisherRejectsMissingRoutingAndHardlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: " \t ", Content: "unrouteable"}); err == nil {
		t.Fatal("missing step run ID accepted")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(path), "external-record")
	if err := os.WriteFile(outside, []byte("external private file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(path, "00000000000000000001.record")); err != nil {
		t.Fatal(err)
	}
	if recovered, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1<<20); err == nil {
		recovered.Close(context.Background())
		t.Fatal("hard-linked record accepted")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatal("external hard link permissions changed")
	}
}

func TestDurableLogPublisherRecoveryRejectsSmallerBudgetWithoutEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	p := newDurableTestPublisher(t, durableTestTransport(), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step", Content: "retained"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recovered, err := NewDurableLogPublisher(context.Background(), durableTestTransport(), path, 1); !errors.Is(err, ErrBuildLogSpoolFull) {
		if recovered != nil {
			recovered.Close(context.Background())
		}
		t.Fatalf("smaller budget error = %v", err)
	}
	if len(durableTestRecords(t, path)) != 1 {
		t.Fatal("recovery evicted an accepted record")
	}
}

func TestDurableLogPublisherFatalErrorCannotBeClearedByInFlightAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newDurableTestPublisher(t, durableLogTransportFunc(func(context.Context, *logstream.BuildLogMessage) error { close(entered); <-release; return nil }), path, 1<<20)
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step", Content: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	p.mu.Lock()
	p.syncFile = func(*os.File) error { return syscall.ENOSPC }
	p.mu.Unlock()
	if err := p.Publish(context.Background(), &logstream.BuildLogMessage{StepRunID: "step", Content: "second"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("disk error = %v", err)
	}
	close(release)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not fail closed")
	}
	if !errors.Is(p.LastError(), syscall.ENOSPC) {
		t.Fatalf("fatal diagnostic cleared: %v", p.LastError())
	}
	if len(durableTestRecords(t, path)) != 1 {
		t.Fatal("acknowledged record should remain conservatively replayable after fatal disk failure")
	}
}
