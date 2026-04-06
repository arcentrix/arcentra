// Copyright 2026 Arcentra Authors.
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

package taskqueue

import (
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/engine/taskqueue"
)

// PayloadToExecutionRequest converts StepRunTaskPayload to ExecutionRequest for executor.Manager.
func PayloadToExecutionRequest(payload *taskqueue.StepRunTaskPayload, defaultJobTimeoutSec int) *executor.ExecutionRequest {
	if payload == nil {
		return nil
	}
	step := &executor.StepInfo{
		Name:    payload.StepName,
		Uses:    payload.Uses,
		Action:  payload.Action,
		Args:    payload.Args,
		Env:     payload.Env,
		Timeout: payload.Timeout,
	}
	if step.Args == nil {
		step.Args = make(map[string]any)
	}
	if step.Env == nil {
		step.Env = make(map[string]string)
	}
	job := &executor.JobInfo{
		Name: payload.JobName,
	}
	if payload.Env != nil {
		job.Env = payload.Env
	} else {
		job.Env = make(map[string]string)
	}
	pipeline := &executor.PipelineInfo{
		Namespace: payload.PipelineID,
	}
	req := executor.NewExecutionRequest(step, job, pipeline)
	req.Workspace = payload.Workspace
	req.Env = payload.Env
	if req.Env == nil {
		req.Env = make(map[string]string)
	}
	req.Options = &executor.ExecutionOptions{
		Timeout: executor.ParseTimeout(payload.Timeout, defaultJobTimeoutSec),
		Extra:   make(map[string]any),
	}
	return req
}
