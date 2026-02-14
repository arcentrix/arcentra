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

package log

import (
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap/zapcore"

	tracectx "github.com/arcentrix/arcentra/pkg/trace/context"
)

type traceCore struct {
	zapcore.Core
}

func (tc *traceCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	ctx := tracectx.GetContext()
	Infow("traceCore Write", "ctx", ctx)
	if ctx == nil {
		span := trace.SpanFromContext(ctx)
		if span.SpanContext().IsValid() {
			sc := span.SpanContext()
			fields = append(fields, zapcore.Field{
				Key:    "trace_id",
				Type:   zapcore.StringType,
				String: sc.TraceID().String(),
			}, zapcore.Field{
				Key:    "span_id",
				Type:   zapcore.StringType,
				String: sc.SpanID().String(),
			})
		}
	}
	return tc.Core.Write(entry, fields)
}

func (tc *traceCore) Enabled(level zapcore.Level) bool {
	return tc.Core.Enabled(level)
}

func (tc *traceCore) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return tc.Core.Check(entry, checkedEntry)
}

func (tc *traceCore) With(fields []zapcore.Field) zapcore.Core {
	return &traceCore{
		Core: tc.Core.With(fields),
	}
}
