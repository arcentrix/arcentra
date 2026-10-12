//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package identitystore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestUnsafeDirectoryNeverRepaired(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o770, 0o777, 0o500, 0o700 | os.ModeSticky, 0o700 | os.ModeSetgid} {
		t.Run(mode.String(), func(t *testing.T) {
			cfg := testConfig(t)
			require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
			require.NoError(t, os.Chmod(cfg.Directory, mode))
			_, err := Open(context.Background(), cfg)
			require.Error(t, err)
			info, err := os.Stat(cfg.Directory)
			require.NoError(t, err)
			require.Equal(t, mode, info.Mode()&(os.ModePerm|os.ModeSticky|os.ModeSetgid))
			entries, err := os.ReadDir(cfg.Directory)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestUnsafeFilesNeverRepaired(t *testing.T) {
	for _, name := range []string{recordName, lockName, stageName, guardName} {
		for _, kind := range []string{"permissive", "readonly", "symlink", "hardlink", "directory", "fifo", "wrong_owner"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				cfg := testConfig(t)
				require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
				path := filepath.Join(cfg.Directory, name)
				switch kind {
				case "permissive":
					require.NoError(t, os.WriteFile(path, nil, 0o644))
				case "readonly":
					require.NoError(t, os.WriteFile(path, nil, 0o400))
				case "symlink":
					target := filepath.Join(filepath.Dir(cfg.Directory), "target")
					require.NoError(t, os.WriteFile(target, nil, 0o600))
					require.NoError(t, os.Symlink(target, path))
				case "hardlink":
					target := filepath.Join(filepath.Dir(cfg.Directory), "target")
					require.NoError(t, os.WriteFile(target, nil, 0o600))
					require.NoError(t, os.Link(target, path))
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				case "fifo":
					require.NoError(t, unix.Mkfifo(path, 0o600))
				case "wrong_owner":
					if os.Geteuid() != 0 {
						t.Skip("requires synthetic fixture chown")
					}
					require.NoError(t, os.WriteFile(path, nil, 0o600))
					require.NoError(t, os.Chown(path, 65534, 65534))
				}
				before, err := os.Lstat(path)
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, err = Open(ctx, cfg)
				require.Error(t, err)
				require.NotErrorIs(t, err, context.DeadlineExceeded)
				after, err := os.Lstat(path)
				require.NoError(t, err)
				require.True(t, os.SameFile(before, after))
				require.Equal(t, before.Mode(), after.Mode())
				require.Equal(t, before.Sys().(*syscall.Stat_t).Uid, after.Sys().(*syscall.Stat_t).Uid)
			})
		}
	}
}

func TestUnsafeAncestorAndMissingParent(t *testing.T) {
	for _, kind := range []string{"symlink_leaf", "symlink_ancestor", "permissive_parent", "wrong_owner_parent", "wrong_owner_directory", "missing_parent", "regular_ancestor"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			base := filepath.Dir(cfg.Directory)
			switch kind {
			case "symlink_leaf":
				target := filepath.Join(base, "target")
				require.NoError(t, os.Mkdir(target, 0o700))
				require.NoError(t, os.Symlink(target, cfg.Directory))
			case "symlink_ancestor":
				target := filepath.Join(base, "target")
				require.NoError(t, os.Mkdir(target, 0o700))
				link := filepath.Join(base, "alias")
				require.NoError(t, os.Symlink(target, link))
				cfg.Directory = filepath.Join(link, "identity")
			case "permissive_parent":
				parent := filepath.Join(base, "unsafe")
				require.NoError(t, os.Mkdir(parent, 0o777))
				require.NoError(t, os.Chmod(parent, 0o777))
				cfg.Directory = filepath.Join(parent, "identity")
			case "wrong_owner_parent":
				if os.Geteuid() != 0 {
					t.Skip("requires synthetic fixture chown")
				}
				parent := filepath.Join(base, "other-user")
				require.NoError(t, os.Mkdir(parent, 0o700))
				require.NoError(t, os.Chown(parent, 65534, 65534))
				cfg.Directory = filepath.Join(parent, "identity")
			case "wrong_owner_directory":
				if os.Geteuid() != 0 {
					t.Skip("requires synthetic fixture chown")
				}
				require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
				require.NoError(t, os.Chown(cfg.Directory, 65534, 65534))
			case "missing_parent":
				cfg.Directory = filepath.Join(base, "missing", "identity")
			case "regular_ancestor":
				parent := filepath.Join(base, "regular")
				require.NoError(t, os.WriteFile(parent, nil, 0o600))
				cfg.Directory = filepath.Join(parent, "identity")
			}
			_, err := Open(context.Background(), cfg)
			require.Error(t, err)
			if kind == "missing_parent" {
				_, err := os.Stat(filepath.Dir(cfg.Directory))
				require.True(t, os.IsNotExist(err))
			}
		})
	}
}

func TestLocationsRequireExplicitDisjointDirectory(t *testing.T) {
	for _, kind := range []string{"empty_directory", "relative_directory", "empty_workspace", "same", "inside", "outside", "symlink_workspace", "missing_workspace_symlink_parent", "root", "dotdot_overlap"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			base := filepath.Dir(cfg.Directory)
			switch kind {
			case "empty_directory":
				cfg.Directory = ""
			case "relative_directory":
				cfg.Directory = "identity"
			case "empty_workspace":
				cfg.Workspace = ""
			case "same":
				cfg.Workspace = cfg.Directory
			case "inside":
				cfg.Workspace = base
			case "outside":
				cfg.Workspace = filepath.Join(cfg.Directory, "build")
			case "symlink_workspace":
				require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
				require.NoError(t, os.Symlink(cfg.Directory, cfg.Workspace))
			case "missing_workspace_symlink_parent":
				require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
				alias := filepath.Join(base, "alias")
				require.NoError(t, os.Symlink(cfg.Directory, alias))
				cfg.Workspace = filepath.Join(alias, "missing", "child")
			case "root":
				cfg.Directory = "/"
			case "dotdot_overlap":
				cfg.Workspace = cfg.Directory + "/nested/.."
			}
			_, err := Open(context.Background(), cfg)
			require.Error(t, err)
		})
	}
	cfg := testConfig(t)
	cfg.Workspace = cfg.Directory + "-workspace"
	store := openTest(t, cfg)
	require.Equal(t, StateEmpty, store.State())
}

func TestRejectUnexpectedEntryAndNonemptyLock(t *testing.T) {
	for _, name := range []string{"unexpected", lockName} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			require.NoError(t, os.Mkdir(cfg.Directory, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, name), []byte("synthetic-data"), 0o600))
			_, err := Open(context.Background(), cfg)
			require.Error(t, err)
		})
	}
}

func TestLockExclusionAndStableInode(t *testing.T) {
	cfg := testConfig(t)
	store := openTest(t, cfg)
	second, err := Open(context.Background(), cfg)
	require.Nil(t, second)
	require.ErrorIs(t, err, ErrLocked)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestIdentityLockHelper$")
	cmd.Env = append(os.Environ(), "IDENTITYSTORE_TEST_DIRECTORY="+cfg.Directory, "IDENTITYSTORE_TEST_WORKSPACE="+cfg.Workspace)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "synthetic-lock-excluded")
	require.NoError(t, store.Close())
	reopened := openTest(t, cfg)
	require.Equal(t, StateEmpty, reopened.State())
}

func TestIdentityLockHelper(t *testing.T) {
	directory := os.Getenv("IDENTITYSTORE_TEST_DIRECTORY")
	if directory == "" {
		return
	}
	store, err := Open(context.Background(), Config{Directory: directory, Workspace: os.Getenv("IDENTITYSTORE_TEST_WORKSPACE"), ControllerBinding: syntheticBinding})
	if store != nil {
		_ = store.Close()
	}
	require.ErrorIs(t, err, ErrLocked)
	t.Log("synthetic-lock-excluded")
}

func TestLockAndStateReplacementDuringSessionFailClosed(t *testing.T) {
	for _, kind := range []string{"lock", "record", "unexpected", "stage"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			require.NoError(t, store.BeginEnrollment(context.Background()))
			switch kind {
			case "lock":
				require.NoError(t, os.Rename(filepath.Join(cfg.Directory, lockName), filepath.Join(filepath.Dir(cfg.Directory), "old-lock")))
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, lockName), nil, 0o600))
			case "record":
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, recordName), []byte("changed"), 0o600))
			case "unexpected":
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, "unexpected"), nil, 0o600))
			case "stage":
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, stageName), nil, 0o600))
			}
			require.Error(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
			require.ErrorIs(t, store.BeginEnrollment(context.Background()), ErrPending)
		})
	}
}

func TestBindingMustBeBoundedAndErrorsNeverIncludeInputs(t *testing.T) {
	for _, binding := range []string{"", " synthetic-binding", "synthetic\ncanary", strings.Repeat("x", 8193)} {
		cfg := testConfig(t)
		cfg.ControllerBinding = binding
		_, err := Open(context.Background(), cfg)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "canary")
	}
	cfg := testConfig(t)
	cfg.Directory = filepath.Join(filepath.Dir(cfg.Directory), "synthetic-secret-path", "missing")
	_, err := Open(context.Background(), cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-secret-path")
}

func TestTrustedStickyRootAncestorAllowsPrivateLeaf(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned synthetic fixture")
	}
	cfg := testConfig(t)
	parent := filepath.Join(filepath.Dir(cfg.Directory), "sticky-parent")
	require.NoError(t, os.Mkdir(parent, 0o700))
	require.NoError(t, os.Chmod(parent, os.ModeSticky|0o777))
	cfg.Directory = filepath.Join(parent, "identity")
	store := openTest(t, cfg)
	readyTest(t, store)
}

func TestCancellationAfterGuardRemovalNeverExposesUncommittedIdentity(t *testing.T) {
	cfg := testConfig(t)
	store := openTest(t, cfg)
	require.NoError(t, store.BeginEnrollment(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.hook = func(point string) error {
		if point == "guard.after_remove" {
			cancel()
			return errors.New("synthetic-canary")
		}
		return nil
	}
	require.Error(t, store.CommitIdentity(ctx, syntheticAgentID, syntheticToken))
	_, ok := store.Identity()
	require.False(t, ok)
	require.NoError(t, store.Close())
	_, err := Open(context.Background(), cfg)
	require.ErrorIs(t, err, ErrPending)
}

func TestStagingReplacementAtRenameRejected(t *testing.T) {
	for _, kind := range []string{"symlink", "inode", "mode", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			require.NoError(t, store.BeginEnrollment(context.Background()))
			store.hook = func(point string) error {
				if point != "ready.before_rename" {
					return nil
				}
				path := filepath.Join(cfg.Directory, stageName)
				switch kind {
				case "symlink":
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Symlink(recordName, path))
				case "inode":
					require.NoError(t, os.Rename(path, filepath.Join(filepath.Dir(cfg.Directory), "old-stage")))
					require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o600))
				case "mode":
					require.NoError(t, os.Chmod(path, 0o644))
				case "hardlink":
					require.NoError(t, os.Link(path, filepath.Join(filepath.Dir(cfg.Directory), "linked-stage")))
				}
				return nil
			}
			require.Error(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
			_, ok := store.Identity()
			require.False(t, ok)
			require.NoError(t, store.Close())
			_, err := Open(context.Background(), cfg)
			require.Error(t, err)
		})
	}
}

func TestReadyFileChangedAfterRenameRejected(t *testing.T) {
	cfg := testConfig(t)
	store := openTest(t, cfg)
	require.NoError(t, store.BeginEnrollment(context.Background()))
	store.hook = func(point string) error {
		if point == "ready.after_rename" {
			return os.WriteFile(filepath.Join(cfg.Directory, recordName), []byte("changed"), 0o600)
		}
		return nil
	}
	require.Error(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
	_, ok := store.Identity()
	require.False(t, ok)
	require.NoError(t, store.Close())
	_, err := Open(context.Background(), cfg)
	require.ErrorIs(t, err, ErrPending)
}

func TestProcessCrashReleasesLockWithoutDiscardingPending(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIdentityCrashHelper$")
	cmd.Env = append(os.Environ(), "IDENTITYSTORE_CRASH_DIRECTORY="+cfg.Directory, "IDENTITYSTORE_CRASH_WORKSPACE="+cfg.Workspace)
	output, err := cmd.CombinedOutput()
	require.Error(t, err, string(output))
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 23, exit.ExitCode())
	_, err = Open(context.Background(), cfg)
	require.ErrorIs(t, err, ErrPending)
	_, err = Open(context.Background(), cfg)
	require.ErrorIs(t, err, ErrPending)
}

func TestIdentityCrashHelper(t *testing.T) {
	directory := os.Getenv("IDENTITYSTORE_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	store, err := Open(context.Background(), Config{Directory: directory, Workspace: os.Getenv("IDENTITYSTORE_CRASH_WORKSPACE"), ControllerBinding: syntheticBinding})
	require.NoError(t, err)
	require.NoError(t, store.BeginEnrollment(context.Background()))
	// Exiting without Close models a lost enrollment response or process crash.
	// This fixture never sends an RPC and never contains real credentials.
	os.Exit(23)
}
