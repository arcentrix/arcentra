//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package identitystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

const syntheticAgentID = "synthetic-agent-1"
const syntheticToken = syntheticAgentID + ":synthetic-signature-never-a-real-secret"
const syntheticBinding = "synthetic-controller.invalid:443|verified-name|synthetic-trust-hash"

func testConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	return Config{Directory: filepath.Join(base, "identity"), Workspace: filepath.Join(base, "workspace"), ControllerBinding: syntheticBinding}
}

func openTest(t *testing.T, cfg Config) *Store {
	t.Helper()
	store, err := Open(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func readyTest(t *testing.T, store *Store) {
	t.Helper()
	require.NoError(t, store.BeginEnrollment(context.Background()))
	require.NoError(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
}

func TestDurableIdentityRestartAndImmutableReady(t *testing.T) {
	cfg := testConfig(t)
	store := openTest(t, cfg)
	require.Equal(t, StateEmpty, store.State())
	_, ok := store.Identity()
	require.False(t, ok)
	require.Error(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
	require.NoError(t, store.BeginEnrollment(context.Background()))
	require.Equal(t, StatePending, store.State())
	for _, name := range []string{recordName, guardName} {
		data, err := os.ReadFile(filepath.Join(cfg.Directory, name))
		require.NoError(t, err)
		require.NotContains(t, string(data), syntheticToken)
		require.NotContains(t, string(data), "perAgentToken")
		require.NotContains(t, string(data), "registrationToken")
	}
	require.NoError(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
	require.Equal(t, StateReady, store.State())
	identity, ok := store.Identity()
	require.True(t, ok)
	require.Equal(t, Identity{syntheticAgentID, syntheticToken}, identity)
	require.Error(t, store.BeginEnrollment(context.Background()))
	require.Error(t, store.CommitIdentity(context.Background(), "replacement-agent", "replacement-agent:replacement-signature"))
	before, err := os.ReadFile(filepath.Join(cfg.Directory, recordName))
	require.NoError(t, err)
	lockInfo, err := os.Stat(filepath.Join(cfg.Directory, lockName))
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, store.Close())
	reopened := openTest(t, cfg)
	identity, ok = reopened.Identity()
	require.True(t, ok)
	require.Equal(t, Identity{syntheticAgentID, syntheticToken}, identity)
	after, err := os.ReadFile(filepath.Join(cfg.Directory, recordName))
	require.NoError(t, err)
	require.Equal(t, before, after)
	nextLock, err := os.Stat(filepath.Join(cfg.Directory, lockName))
	require.NoError(t, err)
	require.True(t, os.SameFile(lockInfo, nextLock))
	entries, err := os.ReadDir(cfg.Directory)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestPendingAndStagingNeverReset(t *testing.T) {
	for _, kind := range []string{"pending", "guard_only", "stage_only", "ready_with_stage", "ready_with_guard"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			switch kind {
			case "pending":
				require.NoError(t, store.BeginEnrollment(context.Background()))
			case "guard_only":
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, guardName), nil, 0o600))
			case "stage_only":
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, stageName), nil, 0o600))
			case "ready_with_stage":
				readyTest(t, store)
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, stageName), []byte("partial"), 0o600))
			case "ready_with_guard":
				readyTest(t, store)
				require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, guardName), nil, 0o600))
			}
			require.NoError(t, store.Close())
			before, err := os.ReadDir(cfg.Directory)
			require.NoError(t, err)
			reopened, err := Open(context.Background(), cfg)
			require.Nil(t, reopened)
			require.ErrorIs(t, err, ErrPending)
			after, err := os.ReadDir(cfg.Directory)
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = Open(context.Background(), cfg)
			require.ErrorIs(t, err, ErrPending)
		})
	}
}

func TestControllerBindingAndRecordValidation(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"wrong_binding": func(b []byte) []byte {
			return []byte(strings.ReplaceAll(string(b), syntheticBinding, "different-controller.invalid"))
		},
		"unknown_version": func(b []byte) []byte { return []byte(strings.Replace(string(b), `"version":1`, `"version":2`, 1)) },
		"unknown_field": func(b []byte) []byte {
			return append(b[:len(b)-1], []byte(`,"registrationToken":"synthetic-canary"}`)...)
		},
		"duplicate_field": func(b []byte) []byte { return append([]byte(`{"version":1,`), b[1:]...) },
		"trailing":        func(b []byte) []byte { return append(b, []byte(`{}`)...) },
		"malformed":       func([]byte) []byte { return []byte(`{"version":`) },
		"empty":           func([]byte) []byte { return nil },
		"oversize":        func([]byte) []byte { return []byte(strings.Repeat("X", maxRecordBytes+1)) },
		"invalid_state": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"state":"ready"`, `"state":"other"`, 1))
		},
		"wrong_id": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"agentID":"synthetic-agent-1"`, `"agentID":"other-agent"`, 1))
		},
		"pending_with_token": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"state":"ready"`, `"state":"pending"`, 1))
		},
		"null": func([]byte) []byte { return []byte(`null`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			readyTest(t, store)
			require.NoError(t, store.Close())
			path := filepath.Join(cfg.Directory, recordName)
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			mutated := mutate(b)
			require.NoError(t, os.WriteFile(path, mutated, 0o600))
			reopened, err := Open(context.Background(), cfg)
			require.Nil(t, reopened)
			require.Error(t, err)
			require.NotContains(t, err.Error(), syntheticToken)
			require.NotContains(t, err.Error(), "synthetic-canary")
			unchanged, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(mutated), string(unchanged))
		})
	}
}

func TestContextAndInvalidIdentity(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	_, err = os.Stat(cfg.Directory)
	require.True(t, os.IsNotExist(err))
	store := openTest(t, cfg)
	require.ErrorIs(t, store.BeginEnrollment(ctx), context.Canceled)
	require.NoError(t, store.BeginEnrollment(context.Background()))
	require.ErrorIs(t, store.CommitIdentity(ctx, syntheticAgentID, syntheticToken), context.Canceled)
	for _, pair := range [][2]string{{"", ""}, {"agent", ""}, {"other", syntheticToken}, {"bad id", "bad id:signature"}, {"agent", "agent:"}, {"agent", "agent:bad\nsecret"}, {"agent", "agent:two:colons"}, {strings.Repeat("a", 257), "a:signature"}, {"agent", "agent:" + strings.Repeat("a", 4096)}} {
		require.Error(t, ValidateIdentity(pair[0], pair[1]))
		require.Error(t, store.CommitIdentity(context.Background(), pair[0], pair[1]))
	}
	require.NoError(t, store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken))
	require.NoError(t, store.Close())
	require.ErrorIs(t, store.BeginEnrollment(context.Background()), ErrClosed)
	_, ok := store.Identity()
	require.False(t, ok)
}

func TestEveryEnrollmentPersistenceBoundary(t *testing.T) {
	points := []string{"guard.before_create", "guard.after_create", "guard.before_write", "guard.after_write", "guard.before_file_sync", "guard.after_file_sync", "guard.before_dir_sync", "guard.after_dir_sync", "pending.before_create", "pending.after_create", "pending.before_write", "pending.after_write", "pending.before_file_sync", "pending.after_file_sync", "pending.before_rename", "pending.after_rename", "pending.before_dir_sync", "pending.after_dir_sync"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			hit := false
			store.hook = func(got string) error {
				if got == point {
					hit = true
					return errors.New("synthetic-sensitive-error-canary")
				}
				return nil
			}
			err := store.BeginEnrollment(context.Background())
			require.Error(t, err)
			require.True(t, hit)
			require.NotContains(t, err.Error(), "canary")
			require.ErrorIs(t, store.BeginEnrollment(context.Background()), ErrPending)
			require.NoError(t, store.Close())
			reopened, err := Open(context.Background(), cfg)
			if point == "guard.before_create" {
				require.NoError(t, err)
				require.Equal(t, StateEmpty, reopened.State())
				require.NoError(t, reopened.Close())
			} else {
				require.ErrorIs(t, err, ErrPending)
			}
		})
	}
}

func TestEveryReadyPersistenceBoundary(t *testing.T) {
	points := []string{"ready.before_create", "ready.after_create", "ready.before_write", "ready.after_write", "ready.before_file_sync", "ready.after_file_sync", "ready.before_rename", "ready.after_rename", "ready.before_dir_sync", "ready.after_dir_sync", "guard.before_remove", "guard.after_remove", "commit.before_dir_sync", "commit.after_dir_sync"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			require.NoError(t, store.BeginEnrollment(context.Background()))
			hit := false
			store.hook = func(got string) error {
				if got == point {
					hit = true
					return errors.New(syntheticToken)
				}
				return nil
			}
			err := store.CommitIdentity(context.Background(), syntheticAgentID, syntheticToken)
			require.Error(t, err)
			require.True(t, hit)
			require.NotContains(t, err.Error(), syntheticToken)
			_, ok := store.Identity()
			require.False(t, ok)
			require.ErrorIs(t, store.BeginEnrollment(context.Background()), ErrPending)
			require.NoError(t, store.Close())
			_, err = Open(context.Background(), cfg)
			require.ErrorIs(t, err, ErrPending)
		})
	}
}

func TestOpenPersistenceBoundaries(t *testing.T) {
	for _, point := range []string{"directory.before_create", "directory.after_create", "directory.before_parent_sync", "directory.after_parent_sync", "lock.before_file_sync", "lock.after_file_sync", "lock.before_dir_sync", "lock.after_dir_sync"} {
		t.Run(point, func(t *testing.T) {
			cfg := testConfig(t)
			hit := false
			store, err := openStore(context.Background(), cfg, func(got string) error {
				if got == point {
					hit = true
					return errors.New("synthetic-canary")
				}
				return nil
			})
			require.Nil(t, store)
			require.Error(t, err)
			require.True(t, hit)
			require.NotContains(t, err.Error(), "canary")
			reopened := openTest(t, cfg)
			require.Equal(t, StateEmpty, reopened.State())
		})
	}
}

func TestCancellationAtPersistenceBoundariesRetainsEvidence(t *testing.T) {
	for _, point := range []string{"guard.after_write", "pending.after_rename", "ready.after_write", "ready.after_rename", "guard.after_remove"} {
		t.Run(point, func(t *testing.T) {
			cfg := testConfig(t)
			store := openTest(t, cfg)
			ready := strings.HasPrefix(point, "ready") || point == "guard.after_remove"
			if ready {
				require.NoError(t, store.BeginEnrollment(context.Background()))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.hook = func(got string) error {
				if got == point {
					cancel()
				}
				return nil
			}
			var err error
			if ready {
				err = store.CommitIdentity(ctx, syntheticAgentID, syntheticToken)
			} else {
				err = store.BeginEnrollment(ctx)
			}
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, store.Close())
			_, err = Open(context.Background(), cfg)
			require.ErrorIs(t, err, ErrPending)
		})
	}
}

func TestPendingRecordWithoutGuardFailsClosed(t *testing.T) {
	cfg := testConfig(t)
	store := openTest(t, cfg)
	require.NoError(t, store.Close())
	data, err := sonic.Marshal(record{Version: recordVersion, ControllerBinding: syntheticBinding, State: StatePending})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Directory, recordName), data, 0o600))
	_, err = Open(context.Background(), cfg)
	require.ErrorIs(t, err, ErrPending)
}
