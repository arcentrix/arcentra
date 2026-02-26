// Copyright 2025 Arcentra Authors.
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
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"golang.org/x/sync/errgroup"
)

// Runner runs pipeline jobs
type Runner struct {
	execCtx *ExecutionContext
}

// NewPipelineRunner creates a new pipeline runner
func NewPipelineRunner(ctx *ExecutionContext) *Runner {
	return &Runner{execCtx: ctx}
}

// Run executes all jobs in the pipeline
// Jobs can run in parallel unless concurrency is specified
func (r *Runner) Run(ctx context.Context, p *spec.Pipeline) error {
	// Update execution context with pipeline
	r.execCtx.Pipeline = p

	// Group jobs by concurrency key
	concurrencyGroups := make(map[string][]*spec.Job)
	var noConcurrencyJobs []*spec.Job

	for i := range p.Jobs {
		job := &p.Jobs[i]
		if job.Concurrency != "" {
			concurrencyGroups[job.Concurrency] = append(concurrencyGroups[job.Concurrency], job)
		} else {
			noConcurrencyJobs = append(noConcurrencyJobs, job)
		}
	}

	// Run jobs with concurrency control
	eg, egCtx := errgroup.WithContext(ctx)
	for _, jobs := range concurrencyGroups {
		eg.Go(func() error {
			// Jobs with same concurrency key run sequentially
			for _, job := range jobs {
				if err := egCtx.Err(); err != nil {
					return err
				}
				jr := NewJobRunner(r.execCtx, job)
				if err := jr.Run(egCtx); err != nil {
					return err
				}
			}
			return nil
		})
	}

	// Run jobs without concurrency control in parallel
	for _, job := range noConcurrencyJobs {
		eg.Go(func() error {
			jr := NewJobRunner(r.execCtx, job)
			return jr.Run(ctx)
		})
	}

	return eg.Wait()
}

// RunWithTimeout runs pipeline with timeout
func (r *Runner) RunWithTimeout(ctx context.Context, p *spec.Pipeline, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return r.Run(ctx, p)
}
