// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/gorm"
	"time"
)

// CaptureObservations 在定义加载前原子保存完整归一化观察，不创建运行或推进游标
func (r *SCMPollingRepo) CaptureObservations(ctx context.Context, in CaptureObservationsInput) (*model.SCMPollingStream, error) {
	before, e := r.GetByProject(ctx, in.ProjectID)
	if e != nil {
		return nil, e
	}
	if e := checkPollingRevision(before, in.ExpectedRevision); e != nil {
		return nil, e
	}
	selection, e := polling.ParseSelection(before.SelectionJSON)
	if e != nil {
		return nil, e
	}
	ids := make([]string, 0, len(selection.Pipelines))
	for _, p := range selection.Pipelines {
		ids = append(ids, p.PipelineID)
	}
	var out *model.SCMPollingStream
	var denied error
	e = r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		pipelines, e := lockPollingPipelines(ctx, tx, ids, in.ProjectID)
		if e != nil {
			return e
		}
		p, e := lockPollingProject(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, in.ExpectedRevision); e != nil {
			return e
		}
		if e := requirePollingActive(s, in.ActivationGeneration); e != nil {
			return e
		}
		if s.PendingObservationJSON != "" || s.PendingBatchJSON != "" {
			return polling.ErrUnresolved
		}
		scope, e := pollingScope(s)
		if e != nil {
			return e
		}
		if e := pollingReady(p, scope); e != nil {
			return e
		}
		actual, e := pollingSelection(pipelines, scope)
		if e != nil {
			return e
		}
		body, e := actual.Canonical()
		if e != nil {
			return e
		}
		storedSelection, e := selection.Canonical()
		if e != nil || body != storedSelection || s.SelectionJSON != before.SelectionJSON || s.SelectionDigest != before.SelectionDigest {
			return polling.ErrConflict
		}
		digest, e := actual.Digest()
		if e != nil || digest != s.SelectionDigest {
			return polling.ErrConflict
		}
		b := in.Batch
		if b.ScopeDigest != s.ScopeDigest || b.Generation != s.ActivationGeneration || b.SelectionDigest != s.SelectionDigest {
			return polling.ErrConflict
		}
		b.Revision = s.Revision + 1
		encoded, e := b.Canonical()
		if e != nil {
			return e
		}
		current, e := polling.ParseCursor(s.CursorJSON)
		if e != nil {
			return e
		}
		oldTime, _ := time.Parse(time.RFC3339Nano, current.Boundary)
		newTime, _ := time.Parse(time.RFC3339Nano, b.NextCursor.Boundary)
		if newTime.Before(oldTime) {
			return polling.ErrConflict
		}
		view, e := authorizePolling(ctx, tx, s.EnabledBy, in.ProjectID, ids)
		if e != nil {
			if errors.Is(e, polling.ErrForbidden) {
				denied = e
				return updatePollingStream(ctx, tx, s, map[string]any{"state": polling.StatePaused, "activation_generation": s.ActivationGeneration + 1, "pause_reason": "authorization_denied", "pending_observation_json": encoded})
			}
			return e
		}
		if e := updatePollingStream(ctx, tx, s, map[string]any{"pending_observation_json": encoded, "next_poll_at": nil, "last_poll_error": ""}); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return view.current(ctx)
	})
	if e != nil {
		return nil, e
	}
	if denied != nil {
		return nil, denied
	}
	return out, nil
}
