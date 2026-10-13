//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package taskqueue

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestArtifactOpenRejectsReplacedSpecialFiles(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink", "directory", "different-file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "artifact")
			if err := os.WriteFile(name, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			expected, err := root.Lstat("artifact")
			if err != nil {
				t.Fatal(err)
			}
			// Keep the original inode allocated, so a replacement cannot reuse it.
			if err := os.Rename(name, filepath.Join(dir, "original")); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "fifo":
				err = unix.Mkfifo(name, 0600)
			case "symlink":
				err = os.Symlink("original", name)
			case "directory":
				err = os.Mkdir(name, 0700)
			case "different-file":
				err = os.WriteFile(name, []byte("replacement"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			file, err := openArtifactRegularFile(root, "artifact", expected)
			if file != nil {
				file.Close()
				t.Fatal("replacement was opened")
			}
			if err == nil {
				t.Fatal("replacement accepted")
			}
		})
	}
}

func TestArtifactOpenAcceptsOriginalRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "artifact"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("artifact")
	if err != nil {
		t.Fatal(err)
	}
	file, err := openArtifactRegularFile(root, "artifact", expected)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
