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

package executor

import (
	"context"
	"time"

	"github.com/arcentrix/arcentra/pkg/engine/logstream"
)

// LogPublisher publishes build logs.
type LogPublisher interface {
	Publish(ctx context.Context, msg *logstream.BuildLogMessage) error
}

// BuildLogMessageFromEvent builds a build log message from EventContext.
func BuildLogMessageFromEvent(ctx EventContext, content, stream string) *logstream.BuildLogMessage {
	return &logstream.BuildLogMessage{
		PipelineID: ctx.PipelineID,
		StepName:   ctx.StepName,
		StepRunID:  ctx.StepID,
		PluginName: ctx.PluginName,
		AgentID:    ctx.AgentID,
		Timestamp:  time.Now().Unix(),
		LineNumber: 0,
		Level:      "info",
		Stream:     stream,
		Content:    content,
	}
}
