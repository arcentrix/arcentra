// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/safe"
)

// PipelineExecutor executes pipelines using DAG-based reconciliation
type PipelineExecutor struct {
	execCtx *ExecutionContext
	logger  log.Logger
}

// NewPipelineExecutor creates a new pipeline executor
func NewPipelineExecutor(
	p *spec.Pipeline,
	pluginMgr *plugin.Manager,
	workspace string,
	logger log.Logger,
) *PipelineExecutor {
	execCtx := NewExecutionContext(p, pluginMgr, workspace, logger)
	return &PipelineExecutor{
		execCtx: execCtx,
		logger:  logger,
	}
}

// Execute executes the pipeline using DAG-based reconciliation
func (pe *PipelineExecutor) Execute(ctx context.Context) error {
	// Build DAG from pipeline jobs
	graph, tasks, err := BuildDAG(pe.execCtx.Pipeline.Jobs)
	if err != nil {
		return fmt.Errorf("build DAG: %w", err)
	}

	// Create task framework
	taskFramework := NewTaskFramework(pe.execCtx, pe.logger)

	// Create reconciler
	reconciler := NewReconciler(graph, tasks, taskFramework, pe.logger)

	// Channel to trigger reconcile
	reconcileCh := make(chan struct{}, 1)

	// Set callback to trigger reconcile when task completes
	reconciler.SetOnCompleted(func() {
		select {
		case reconcileCh <- struct{}{}:
		default:
		}
	})

	// Initial reconcile
	reconcileCh <- struct{}{}

	// Reconciliation loop
	ticker := time.NewTicker(1 * time.Second) // Fallback ticker
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconcileCh:
			// Triggered by task completion
			hasMore, err := reconciler.Reconcile(ctx)
			if err != nil {
				return fmt.Errorf("reconcile: %w", err)
			}
			if !hasMore && reconciler.IsCompleted() {
				if pe.logger.Log != nil {
					pe.logger.Log.Infow("pipeline execution completed", "namespace", pe.execCtx.Pipeline.Namespace)
				}
				return nil
			}
		case <-ticker.C:
			// Fallback: periodic check
			if reconciler.IsCompleted() {
				if pe.logger.Log != nil {
					pe.logger.Log.Infow("pipeline execution completed", "namespace", pe.execCtx.Pipeline.Namespace)
				}
				return nil
			}
		}
	}
}

// ExecuteWithTimeout executes pipeline with timeout
func (pe *PipelineExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return pe.Execute(ctx)
}

// ExecuteAsync executes pipeline asynchronously
func (pe *PipelineExecutor) ExecuteAsync(ctx context.Context) <-chan error {
	errCh := make(chan error, 1)
	safe.Go(func() {
		errCh <- pe.Execute(ctx)
		close(errCh)
	})
	return errCh
}
