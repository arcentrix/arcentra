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
	"fmt"

	"github.com/arcentrix/arcentra/pkg/plugin"
)

func (tf *TaskFramework) emitJobEvent(eventType string, task *Task, data map[string]any) {
	emitter := tf.execCtx.EventEmitter
	if emitter == nil || task == nil || task.Job == nil {
		return
	}
	subject := buildJobSubject(tf.execCtx, task.Job.Name)
	emitter.Emit(context.Background(), eventType, emitter.BuildSource("pipeline"), subject, data, buildPipelineExtensions(tf.execCtx))
}

func (tf *TaskFramework) emitStepEvent(eventType string, task *Task, stepName string, data map[string]any) {
	emitter := tf.execCtx.EventEmitter
	if emitter == nil || task == nil || task.Job == nil {
		return
	}
	subject := buildStepSubject(tf.execCtx, task.Job.Name, stepName)
	emitter.Emit(context.Background(), eventType, emitter.BuildSource("pipeline"), subject, data, buildPipelineExtensions(tf.execCtx))
}

func buildPipelineExtensions(execCtx *ExecutionContext) map[string]any {
	if execCtx == nil || execCtx.Pipeline == nil {
		return nil
	}
	return map[string]any{
		"pipelineNamespace": execCtx.Pipeline.Namespace,
		"pipelineVersion":   execCtx.Pipeline.Version,
	}
}

func buildJobSubject(execCtx *ExecutionContext, jobName string) string {
	if execCtx == nil || execCtx.Pipeline == nil {
		return fmt.Sprintf("job:%s", jobName)
	}
	return fmt.Sprintf("pipeline:%s:job:%s", execCtx.Pipeline.Namespace, jobName)
}

func buildStepSubject(execCtx *ExecutionContext, jobName, stepName string) string {
	if execCtx == nil || execCtx.Pipeline == nil {
		return fmt.Sprintf("job:%s:step:%s", jobName, stepName)
	}
	return fmt.Sprintf("pipeline:%s:job:%s:step:%s", execCtx.Pipeline.Namespace, jobName, stepName)
}

func buildPipelineEventData(execCtx *ExecutionContext, status string) map[string]any {
	if execCtx == nil || execCtx.Pipeline == nil {
		return map[string]any{"status": status}
	}
	return map[string]any{
		"status":            status,
		"pipelineNamespace": execCtx.Pipeline.Namespace,
		"pipelineVersion":   execCtx.Pipeline.Version,
		"jobCount":          len(execCtx.Pipeline.Jobs),
	}
}

func (pe *PipelineExecutor) emitPipelineEvent(eventType, status string) {
	emitter := pe.execCtx.EventEmitter
	if emitter == nil {
		return
	}
	subject := fmt.Sprintf("pipeline:%s", pe.execCtx.Pipeline.Namespace)
	emitter.Emit(context.Background(), eventType, emitter.BuildSource("pipeline"), subject, buildPipelineEventData(pe.execCtx, status), buildPipelineExtensions(pe.execCtx))
}

func mapTaskStateToEvent(taskState TaskState, failed bool) string {
	if failed {
		return plugin.EventTypeJobFailed
	}
	switch taskState {
	case TaskStateStarted:
		return plugin.EventTypeJobStarted
	case TaskStateSucceeded:
		return plugin.EventTypeJobCompleted
	case TaskStateFailed:
		return plugin.EventTypeJobFailed
	case TaskStateSkipped:
		return plugin.EventTypeJobCancelled
	default:
		return ""
	}
}
