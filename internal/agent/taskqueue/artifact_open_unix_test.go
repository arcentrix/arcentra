//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package taskqueue

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestArtifactDownloadRejectsSpecialDestinationsBeforeAndAfterStreaming(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink", "directory"} {
		for _, replaceDuringDownload := range []bool{false, true} {
			t.Run(kind+"/during="+map[bool]string{false: "false", true: "true"}[replaceDuringDownload], func(t *testing.T) {
				workspace := t.TempDir()
				artifactTestWrite(t, workspace, "original", "original")
				name := filepath.Join(workspace, "destination")
				createSpecial := func() {
					t.Helper()
					var err error
					switch kind {
					case "fifo":
						err = unix.Mkfifo(name, 0600)
					case "symlink":
						err = os.Symlink("original", name)
					case "directory":
						err = os.Mkdir(name, 0700)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if replaceDuringDownload {
					artifactTestWrite(t, workspace, "destination", "original-destination")
				} else {
					createSpecial()
				}
				st := &memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) {
					if !replaceDuringDownload {
						t.Fatal("unsafe destination caused a storage request")
					}
					if err := os.Rename(name, filepath.Join(workspace, "saved-destination")); err != nil {
						t.Fatal(err)
					}
					createSpecial()
					return io.NopCloser(strings.NewReader("replacement")), nil
				}}
				root, err := openArtifactWorkspace(context.Background(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if err := downloadArtifactFile(context.Background(), root, "destination", "object", artifactTestContentReference("replacement"), st); err == nil {
					t.Fatal("special destination was replaced")
				}
				if artifactTestRead(t, workspace, "original") != "original" {
					t.Fatal("symlink target changed")
				}
				if replaceDuringDownload && artifactTestRead(t, workspace, "saved-destination") != "original-destination" {
					t.Fatal("previous destination changed")
				}
				info, err := os.Lstat(name)
				if err != nil || info.Mode().IsRegular() {
					t.Fatalf("special destination was replaced: %v %v", info, err)
				}
				entries, err := os.ReadDir(workspace)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".arcentra-artifact-") {
						t.Fatalf("temporary download leaked: %s", entry.Name())
					}
				}
			})
		}
	}
}
