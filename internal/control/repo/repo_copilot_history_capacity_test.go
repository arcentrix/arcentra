// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCopilotHistoryConversationCapacityIncludesRetainedRows(t *testing.T) {
	historyConversationCapacityTest(t, historyTestDB(t))
}

// Shared with the real MySQL gate: the owner-wide admission cap must include
// retained deleted/expired rows and serialize creators in different projects.
func historyConversationCapacityTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	const owner = "capacity-owner"
	scopes := []CopilotHistoryScope{
		historySeed(t, db, owner, "capacity-first"),
		historySeed(t, db, owner, "capacity-second"),
	}
	now := time.Now().UTC().Truncate(time.Second)
	rows := make([]model.CopilotConversation, 0, 999)
	for i := 0; i < 999; i++ {
		scope := scopes[i%len(scopes)]
		id := fmt.Sprintf("capacity-seed-%04d", i)
		row := model.CopilotConversation{
			ConversationID: id, OwnerUserID: owner, ProjectID: scope.ProjectID,
			PipelineID: scope.PipelineID, RunID: scope.RunID,
			CreateRequestID: id, CreateFingerprint: id, State: "active",
			Version: 1, NextSequence: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
			ExpiresAt: now.Add(time.Hour), TombstoneUntil: now.Add(CopilotHistoryTombstoneTTL),
		}
		switch i % 3 {
		case 1:
			row.State = "deleted"
			row.DeletedAt = &now
		case 2:
			row.ExpiresAt = now.Add(-time.Minute)
		}
		rows = append(rows, row)
	}
	require.NoError(t, db.CreateInBatches(rows, 100).Error)
	var retained int64
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("owner_user_id = ?", owner).Count(&retained).Error)
	require.EqualValues(t, 999, retained)
	for _, condition := range []string{"state = 'deleted'", "state = 'active' AND expires_at <= ?", "state = 'active' AND expires_at > ?"} {
		query := db.Model(&model.CopilotConversation{}).Where("owner_user_id = ?", owner)
		if condition == "state = 'deleted'" {
			query = query.Where(condition)
		} else {
			query = query.Where(condition, now)
		}
		var count int64
		require.NoError(t, query.Count(&count).Error)
		require.EqualValues(t, 333, count)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type result struct {
		conversation *model.CopilotConversation
		err          error
	}
	start := make(chan struct{})
	results := make(chan result, len(scopes))
	for i, scope := range scopes {
		go func(i int, scope CopilotHistoryScope) {
			<-start
			r := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
			id := fmt.Sprintf("capacity-race-%d", i)
			conversation, err := r.CopilotHistoryCreate(ctx, scope, id, "create-"+id, "fingerprint-"+id, historyTestAllow)
			results <- result{conversation: conversation, err: err}
		}(i, scope)
	}
	close(start)
	// Join both attempts before asserting, including when either unexpectedly fails.
	completed := []result{<-results, <-results}
	successes, capacityRejections := 0, 0
	for _, result := range completed {
		if result.err == nil {
			successes++
			require.NotNil(t, result.conversation)
			require.Equal(t, owner, result.conversation.OwnerUserID)
		} else {
			require.ErrorIs(t, result.err, ErrCopilotHistoryCapacity)
			require.Nil(t, result.conversation)
			capacityRejections++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, capacityRejections)
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("owner_user_id = ?", owner).Count(&retained).Error)
	require.EqualValues(t, 1000, retained)

	for i, scope := range scopes {
		r := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
		id := fmt.Sprintf("capacity-full-%d", i)
		conversation, err := r.CopilotHistoryCreate(ctx, scope, id, "create-"+id, "fingerprint-"+id, historyTestAllow)
		require.ErrorIs(t, err, ErrCopilotHistoryCapacity)
		require.Nil(t, conversation)
	}
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("owner_user_id = ?", owner).Count(&retained).Error)
	require.EqualValues(t, 1000, retained)
}
