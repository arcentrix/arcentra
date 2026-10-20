// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package pipeline

import (
	"github.com/arcentrix/arcentra/internal/shared/pipeline/materialize"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func (tf *TaskFramework) resolveSourceExpectation(job *spec.Job) (*taskqueue.SourcePayload, *sourceprovenance.Binding, error) {
	c := &materialize.Context{Env: tf.execCtx.Env, PipelineIDRef: tf.execCtx.PipelineIDRef, PipelineRunID: tf.execCtx.PipelineRunID, JobRunID: tf.execCtx.JobRunID}
	return c.ResolveSource(job)
}
