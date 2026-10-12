package artifact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArtifactPathsAndRetention(t *testing.T) {
	for _, name := range []string{"../secret", "x/../secret", "/secret", "a\\b", "a\nb", "a//b", ".", "x/./y", "https://host/key"} {
		if Name(name) == nil {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
	p, err := LocalPath("project", "run", "job", "step", "dist/app.tar")
	if err != nil || p != "project/run/job/step/dist/app.tar" {
		t.Fatalf("path %q %v", p, err)
	}
	if ID("p", "r", "j", "s1", "file") == ID("p", "r", "j", "s2", "file") {
		t.Fatal("different steps collide")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, bad := range []string{"0d", "-1h", "3651d", "9999999999999d", "forever", "0"} {
		if _, err := ParseExpiry(bad, now); err == nil {
			t.Errorf("invalid retention accepted %q", bad)
		}
	}
	expires, err := ParseExpiry("7d", now)
	if err != nil || !expires.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("expiry %v %v", expires, err)
	}
	if value, err := ParseExpiry("", now); err != nil || value != nil {
		t.Fatalf("empty retention %v %v", value, err)
	}
	a := StorageFingerprint("s3", "host", "bucket", "region", "prefix", true)
	if a == StorageFingerprint("s3", "host", "other", "region", "prefix", true) {
		t.Fatal("bucket identity collision")
	}
}

func TestOpenLocalRejectsLinksAndNonFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "p", "r"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "p", "r", "file"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := OpenLocal(ctx, root, "p/r/file")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	for name, target := range map[string]string{"external": outside, "internal": "p", "file-link": "p/r/file"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"external/secret", "internal/r/file", "file-link", "p/r", "../secret"} {
		if file, err := OpenLocal(ctx, root, name); err == nil {
			_ = file.Close()
			t.Fatalf("unsafe file opened: %s", name)
		}
	}
	linked := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenLocal(ctx, linked, "p/r/file"); err == nil {
		_ = file.Close()
		t.Fatal("symlink root accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := OpenLocal(cancelled, root, "p/r/file"); err == nil {
		t.Fatal("cancelled read opened")
	}
	if _, err := RemotePath("run", "job", "step", strings.Repeat("a", 64), "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemotePath("run", "job", "step", "../key", "name"); err == nil {
		t.Fatal("invalid digest accepted")
	}
}
