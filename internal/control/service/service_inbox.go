// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"gorm.io/gorm"
	"sync"
	"time"
)

type InboxService struct {
	repos  *repo.Repositories
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewInboxService(r *repo.Repositories) *InboxService { return &InboxService{repos: r} }
func inboxAuthorize(ctx context.Context, r *repo.Repositories, actor string, p *model.Project, pipeline string, approvalID string) error {
	subject, err := liveManualInboxSubject(ctx, r, actor)
	if err != nil {
		if errors.Is(err, repo.ErrManualTriggerForbidden) || errors.Is(err, repo.ErrApprovalForbidden) || errors.Is(err, gorm.ErrRecordNotFound) {
			return repo.ErrInboxForbidden
		}
		return repo.ErrInboxUnavailable
	}
	if p == nil {
		return nil
	}
	checker, err := authz.NewLiveAuthorizer(ctx, r)
	if err != nil {
		return err
	}
	scope := authz.ResourceRef{Type: "project", ID: p.ProjectID, ProjectID: p.ProjectID, OrgID: p.OrgID}
	allowed, err := checker.Check(ctx, subject, "project:read", scope)
	if err != nil {
		return err
	}
	if !allowed {
		return repo.ErrInboxForbidden
	}
	scope.Type = "pipeline"
	scope.ID = pipeline
	allowed, err = checker.Check(ctx, subject, "pipeline:read", scope)
	if err != nil {
		return err
	}
	if !allowed {
		return repo.ErrInboxForbidden
	}
	if approvalID != "" {
		scope.Type = "approval"
		scope.ID = approvalID
		allowed, err = checker.Check(ctx, subject, "approval:read", scope)
		if err != nil {
			return err
		}
		if !allowed {
			return repo.ErrInboxForbidden
		}
	}
	return nil
}
func (s *InboxService) List(ctx context.Context, actor, filter, cursor string, limit int) (*repo.InboxPage, error) {
	return s.repos.InboxList(ctx, actor, filter, cursor, limit, inboxAuthorize)
}
func (s *InboxService) UnreadCount(ctx context.Context, actor string) (*repo.InboxCount, error) {
	return s.repos.InboxUnreadCount(ctx, actor, inboxAuthorize)
}
func (s *InboxService) MarkRead(ctx context.Context, actor, id string) (*repo.InboxRead, error) {
	return s.repos.InboxMarkRead(ctx, actor, id, inboxAuthorize)
}
func (s *InboxService) ReconcileOnce(ctx context.Context) (repo.InboxTickResult, error) {
	return s.repos.InboxReconcile(ctx, inboxAuthorize)
}

// Start/Stop are idempotent; Stop joins before dependencies may be cleaned up.
// A fresh service resumes exclusively from durable work/delivery state.
func (s *InboxService) Start(parent context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	safe.Go(func() {
		defer close(done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				tick, cancel := context.WithTimeout(ctx, 5*time.Second)
				result, err := s.ReconcileOnce(tick)
				cancel()
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					log.Warnw("inbox reconciliation deferred", "code", "source_unavailable")
				} else if result.Invalid > 0 || result.Deferred > 0 {
					log.Warnw("inbox reconciliation deferred", "code", "source_or_access_deferred", "invalid", result.Invalid, "deferred", result.Deferred)
				}
				timer.Reset(5 * time.Second)
			}
		}
	})
}
func (s *InboxService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
	s.done = nil
}
