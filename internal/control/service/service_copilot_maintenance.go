// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
)

// CopilotHistoryMaintenanceService only reconciles expired local records and
// erases expired content. It cannot call models, SCM, pipeline or deployment.
// Retention continues when new persistent conversations have been disabled.
type CopilotHistoryMaintenanceService struct {
	repos  *repo.Repositories
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCopilotHistoryMaintenanceService(r *repo.Repositories) *CopilotHistoryMaintenanceService {
	return &CopilotHistoryMaintenanceService{repos: r}
}
func (s *CopilotHistoryMaintenanceService) Tick(ctx context.Context) error {
	if s == nil || s.repos == nil {
		return ErrCopilotHistoryUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.repos.CopilotHistoryReconcile(bounded, 100); err != nil {
		return ErrCopilotHistoryUnavailable
	}
	if _, err := s.repos.CopilotHistoryPurge(bounded, 100); err != nil {
		return ErrCopilotHistoryUnavailable
	}
	return nil
}
func (s *CopilotHistoryMaintenanceService) Start(parent context.Context) {
	if s == nil || parent == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				log.Warn("Copilot history maintenance unavailable; expired content remains inaccessible")
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
func (s *CopilotHistoryMaintenanceService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if done == nil {
		return
	}
	cancel()
	<-done
	s.mu.Lock()
	if s.done == done {
		s.done = nil
		s.cancel = nil
	}
	s.mu.Unlock()
}
