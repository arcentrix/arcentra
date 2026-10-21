// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

var errRerunDispatchUnknown = errors.New("rerun notification outcome unknown; retain fixed execution identity")

type rerunContextPublisher interface {
	EnqueueContext(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error)
}

// The observation callback must use exact persisted terminal receipts. Missing
// terminal evidence, restart or a stale lease is never proof of failure/success.
type rerunDispatchObserver func(context.Context, repo.CopilotRerunDispatchWork) error

// Private, default-off controller component, bound once under validated opt-in.
// Its owning application must Stop/join it before closing Nova, then
// apply the broker's bounded flush; queue cancellation cannot withdraw acceptance.
type copilotRerunDispatcher struct {
	repositories *repo.Repositories
	publisher    rerunContextPublisher
	build        repo.CopilotRerunSealBuilder
	validate     repo.CopilotRerunClaimValidator
	observe      rerunDispatchObserver
	holder       string
	cursor       repo.CopilotRerunDispatchCursor
	mu           sync.Mutex
	cancel       context.CancelFunc
	done         chan struct{}
}

func newCopilotRerunDispatcher(r *repo.Repositories, publisher nova.IConsumer, policy *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService, observe rerunDispatchObserver) (*copilotRerunDispatcher, error) {
	contextual, ok := publisher.(rerunContextPublisher)
	if r == nil || r.Agent == nil || !ok || contextual == nil || (reflect.ValueOf(contextual).Kind() == reflect.Pointer && reflect.ValueOf(contextual).IsNil()) || observe == nil {
		return nil, errRerunDispatchUnknown
	}
	build, err := newCopilotRerunSealBuilder(policy, runtime, secrets)
	if err != nil {
		return nil, errRerunDispatchUnknown
	}
	validate, err := newCopilotRerunClaimValidator(policy, runtime, secrets)
	if err != nil {
		return nil, errRerunDispatchUnknown
	}
	return &copilotRerunDispatcher{repositories: r, publisher: contextual, build: build, validate: validate, observe: observe, holder: uuid.NewString()}, nil
}
func (d *copilotRerunDispatcher) Start(ctx context.Context) error {
	if d == nil || ctx == nil || ctx.Err() != nil {
		return errRerunDispatchUnknown
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done != nil {
		return errRerunDispatchUnknown
	}
	running, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if running.Err() != nil {
				return
			}
			_ = d.tick(running)
			select {
			case <-running.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (d *copilotRerunDispatcher) Stop(ctx context.Context) error {
	if d == nil || ctx == nil {
		return errRerunDispatchUnknown
	}
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errRerunDispatchUnknown
	}
}
func (d *copilotRerunDispatcher) tick(ctx context.Context) error {
	page, err := d.repositories.CopilotRerunScanDispatch(ctx, d.cursor, 8)
	if err != nil {
		return errRerunDispatchUnknown
	}
	d.cursor = page.Next // inspection progress, including skipped rows; wraps on exhaustion
	var outcome error
	for _, work := range page.Work {
		if ctx.Err() != nil {
			return errRerunDispatchUnknown
		}
		item, cancel := context.WithTimeout(ctx, 10*time.Second)
		if d.process(item, work) != nil {
			outcome = errRerunDispatchUnknown
		}
		cancel()
	}
	return outcome
}
func (d *copilotRerunDispatcher) process(ctx context.Context, work repo.CopilotRerunDispatchWork) (err error) {
	defer func() {
		if recover() != nil {
			err = errRerunDispatchUnknown
		}
	}()
	if work.State == repo.CopilotRerunScheduleClaimed {
		return d.observe(ctx, work)
	}
	if !time.Now().Before(work.ExpiresAt) {
		_, err := d.repositories.CopilotRerunExpireUnclaimed(ctx, work.ScheduleID)
		if err != nil {
			return errRerunDispatchUnknown
		}
		return nil
	}
	lease, err := d.repositories.CopilotRerunAcquireLease(ctx, work.ScheduleID, d.holder, time.Minute)
	if err != nil {
		return errRerunDispatchUnknown
	}
	current, err := d.repositories.CopilotRerunReadDispatchWork(ctx, work.ScheduleID)
	if err != nil {
		return errRerunDispatchUnknown
	}
	if current.State == repo.CopilotRerunScheduleClaimed {
		return d.observe(ctx, *current)
	}
	if current.State == repo.CopilotRerunSchedulePending {
		agent, err := d.pickAgent(ctx, lease.Epoch)
		if err != nil {
			return errRerunDispatchUnknown
		}
		_, err = d.repositories.CopilotRerunSeal(ctx, *lease, repo.CopilotRerunSealInput{OwnerUserID: current.OwnerUserID, ProjectID: current.ProjectID, IntentID: current.IntentID, AgentID: agent, SealID: uuid.NewString(), ExpectedIntentDigest: current.IntentDigest, ExpectedReviewedRunContextDigest: current.ReviewedRunContextDigest, ExpectedIntentVersion: current.IntentVersion, ExpectedGateVersion: current.GateVersion, ExpectedScheduleVersion: current.ScheduleVersion}, authorizeRerunDispatch, d.build)
		// A lost seal acknowledgement may already have committed a fixed Agent/seal.
		// Do not try another identity. The next primary scan reconciles durable state.
		if err != nil {
			return errRerunDispatchUnknown
		}
	} else if current.State != repo.CopilotRerunScheduleSealed {
		return errRerunDispatchUnknown
	}
	ref, err := d.repositories.CopilotRerunDispatchReference(ctx, *lease, authorizeRerunDispatch, d.validate)
	if err != nil {
		return errRerunDispatchUnknown
	}
	body, err := json.Marshal(ref)
	if err != nil || ref.Validate() != nil || ctx.Err() != nil {
		return errRerunDispatchUnknown
	}
	// Only a reference enters the queue. TaskID/ACK is not execution evidence. The
	// lease is intentionally retained after success or uncertainty, bounding retries.
	publish, cancel := context.WithDeadline(ctx, lease.Until)
	defer cancel()
	if _, err = d.publisher.EnqueueContext(publish, &nova.Task{Type: taskqueue.TaskTypeCopilotRerunReference, Payload: body}, nova.Queue(taskqueue.AgentQueue(ref.AgentID))); err != nil {
		return errRerunDispatchUnknown
	}
	return nil
}
func authorizeRerunDispatch(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner, action string) error {
	if (action != "seal" && action != "dispatch") || scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Run == nil || scope.Run.TriggeredBy != owner {
		return repo.ErrCopilotRerunForbidden
	}
	return authorizeRetryActor(ctx, view, owner, scope.Project.ProjectID, scope.Pipeline.PipelineID, "")
}

// Selection is only an advisory attempt. Seal checks the chosen Agent's current
// capability, labels and every frozen selector under lock. A selector mismatch
// leaves the schedule pending. The durable lease epoch rotates candidates fairly
// across retries; it is never part of immutable execution identity.
func (d *copilotRerunDispatcher) pickAgent(ctx context.Context, epoch uint64) (string, error) {
	agents, total, err := d.repositories.Agent.List(ctx, 1, 100)
	if err != nil || total > 100 || int64(len(agents)) != total || epoch == 0 {
		return "", errRerunDispatchUnknown
	}
	var ids []string
	for _, a := range agents {
		if rerunDispatchAgentCandidate(&a) {
			ids = append(ids, a.AgentID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "", errRerunDispatchUnknown
	}
	return ids[(epoch-1)%uint64(len(ids))], nil
}
func rerunDispatchAgentCandidate(a *model.Agent) bool {
	if a == nil || a.AgentID == "" || a.IsEnabled != 1 || (a.Status != 1 && a.Status != 4) || a.OS != "linux" || a.LastHeartbeat == nil || time.Since(*a.LastHeartbeat) > 2*time.Minute || a.LastHeartbeat.After(time.Now().Add(time.Minute)) {
		return false
	}
	var c taskqueue.AgentRuntimeCapabilities
	return json.Unmarshal(a.RuntimeCapabilities, &c) == nil && c.ExecutionMode == taskqueue.RuntimeContainerdOfflineV1 && c.ContainerdOfflineV1 && c.ContainerdWholeJobRetryV1 && c.ContainerdRetryApprovalV1 && c.CopilotWholeJobRerunPreparationV1
}
