// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package nova

import (
	"context"
	"errors"
)

// EnqueueContext is an optional immediate-publication capability. Ordinary
// Enqueue and its delayed-publication behavior remain unchanged. A cancellation
// or error may follow broker acceptance: callers must retain their immutable
// application identity and reconcile it, never infer that no message was sent.
func (q *TaskQueueImpl) EnqueueContext(ctx context.Context, task *Task, opts ...Option) (*Result, error) {
	if ctx == nil || q == nil || q.ctx == nil || q.broker == nil || q.codec == nil || task == nil || task.Type == "" {
		return nil, errors.New("context enqueue unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := q.ctx.Err(); err != nil {
		return nil, err
	}
	options := &TaskOpts{Priority: PriorityNormal}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("invalid enqueue option")
		}
		opt.apply(options)
	}
	// The delay manager has its own persistence/lifecycle and does not honor this
	// caller deadline. Reject rather than pretending the optional API covers it.
	if !options.ProcessAt.IsZero() {
		return nil, errors.New("context enqueue supports immediate publication only")
	}
	publish, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(q.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := publish.Err(); err != nil {
		return nil, err
	}
	return q.sendTaskContext(publish, task, q.getQueueName(options.Queue, options.Priority), options.Priority)
}
