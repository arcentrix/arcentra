// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/retry"
	"github.com/stretchr/testify/require"
)

func TestSecretSnapshotChangePreventsNewJobExecution(t *testing.T) {
	root := environmentContext(t)
	root.ValidateSecrets = func(context.Context) error { return errors.New("private-canary") }
	job := &spec.Job{Name: "blocked", Steps: []*spec.Step{{Name: "run", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "touch must-not-execute"})}}}
	err := NewTaskFramework(root, root.Logger).Execute(context.Background(), &Task{Name: job.Name, Job: job})
	require.Error(t, err)
	require.True(t, retry.IsBlocked(err))
	require.NotContains(t, err.Error(), "private-canary")
	_, err = os.Stat(filepath.Join(root.WorkspaceRoot, "environment", job.Name, "must-not-execute"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSecretSnapshotChangeBlocksNextLocalStepAndRetry(t *testing.T) {
	for _, mode := range []string{"next-step", "failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			root := environmentContext(t)
			marker := filepath.Join(root.WorkspaceRoot, "environment", "build", "delivered")
			root.ValidateSecrets = func(context.Context) error {
				if _, err := os.Stat(marker); err == nil {
					return errors.New("synthetic-rotated-secret")
				}
				return nil
			}
			command := "printf x >> delivered"
			job := &spec.Job{Name: "build", Steps: []*spec.Step{
				{Name: "first", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": command})},
				{Name: "next", Uses: "shell", ContinueOnError: true, Args: spec.MapAsStruct(map[string]any{"run": "touch must-not-execute"})},
			}}
			if mode != "next-step" {
				job.Retry = &spec.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
				job.Steps[0].Args = spec.MapAsStruct(map[string]any{"run": command + "; exit 9"})
			}
			err := NewTaskFramework(root, root.Logger).Execute(context.Background(), &Task{Name: job.Name, Job: job})
			require.Error(t, err)
			require.True(t, retry.IsBlocked(err), "revocation must not be retried or ignored by continueOnError")
			require.NotContains(t, err.Error(), "synthetic-rotated-secret")
			delivered, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "x", string(delivered), "an authorized earlier attempt is not repeated after revocation")
			_, err = os.Stat(filepath.Join(filepath.Dir(marker), "must-not-execute"))
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Equal(t, "synthetic-environment-canary", root.Env["secrets.fixture"], "snapshot values remain immutable")
		})
	}
}

func TestSecretSnapshotChangeImmediatelyBeforeEnqueuePreventsAgentDelivery(t *testing.T) {
	root := environmentContext(t)
	store, queue := &environmentDispatchStore{}, &environmentDispatchQueue{}
	root.JobRunStore, root.TaskQueue = store, queue
	root.SelectAgent = func(context.Context, *spec.Job) (string, error) { return "agent", nil }
	checks := 0
	root.ValidateSecrets = func(context.Context) error { checks++; return errors.New("revoked-private-canary") }
	framework := NewTaskFramework(root.ForJob(), root.Logger)
	job := &spec.Job{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "true"})}}}
	task := &Task{Name: job.Name, Job: job}
	require.NoError(t, framework.createRecords(context.Background(), task))
	require.NoError(t, framework.prepare(context.Background(), task))
	err := framework.queue(context.Background(), task)
	require.Error(t, err)
	require.True(t, retry.IsBlocked(err))
	require.NotContains(t, err.Error(), "revoked-private-canary")
	require.Equal(t, 1, checks)
	require.Empty(t, queue.payload)
}

func TestSecretSnapshotRecheckedAfterTaskCreationWait(t *testing.T) {
	root := environmentContext(t)
	checks := 0
	root.ValidateSecrets = func(context.Context) error {
		checks++
		if checks > 1 {
			return errors.New("revoked during approval wait")
		}
		return nil
	}
	job := &spec.Job{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "touch must-not-execute"})}}}
	err := NewTaskFramework(root, root.Logger).Execute(context.Background(), &Task{Name: job.Name, Job: job})
	require.Error(t, err)
	require.True(t, retry.IsBlocked(err))
	require.Equal(t, 2, checks, "check must follow the approval-bearing task creation phase")
	_, err = os.Stat(filepath.Join(root.WorkspaceRoot, "environment", job.Name, "must-not-execute"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
