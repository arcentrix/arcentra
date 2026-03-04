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

package logger

import (
	"context"
	"fmt"
)

// Info logs a message at info level.
func (l *Logger) Info(args ...any) {
	l.Log(defaultContext(), levelInfo(), fmt.Sprint(args...))
}

// Infow logs a structured message at info level.
func (l *Logger) Infow(msg string, keysAndValues ...any) {
	l.InfoContext(defaultContext(), msg, keysAndValues...)
}

// InfoContext logs a context-aware structured message at info level.
func (l *Logger) InfoContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Log(ctx, levelInfo(), msg, keysAndValues...)
}

// Debug logs a message at debug level.
func (l *Logger) Debug(args ...any) {
	l.Log(defaultContext(), levelDebug(), fmt.Sprint(args...))
}

// Debugw logs a structured message at debug level.
func (l *Logger) Debugw(msg string, keysAndValues ...any) {
	l.DebugContext(defaultContext(), msg, keysAndValues...)
}

// DebugContext logs a context-aware structured message at debug level.
func (l *Logger) DebugContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Log(ctx, levelDebug(), msg, keysAndValues...)
}

// Warn logs a message at warn level.
func (l *Logger) Warn(args ...any) {
	l.Log(defaultContext(), levelWarn(), fmt.Sprint(args...))
}

// Warnw logs a structured message at warn level.
func (l *Logger) Warnw(msg string, keysAndValues ...any) {
	l.WarnContext(defaultContext(), msg, keysAndValues...)
}

// WarnContext logs a context-aware structured message at warn level.
func (l *Logger) WarnContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Log(ctx, levelWarn(), msg, keysAndValues...)
}

// Error logs a message at error level.
func (l *Logger) Error(args ...any) {
	l.Log(defaultContext(), levelError(), fmt.Sprint(args...))
}

// Errorw logs a structured message at error level.
func (l *Logger) Errorw(msg string, keysAndValues ...any) {
	l.ErrorContext(defaultContext(), msg, keysAndValues...)
}

// ErrorContext logs a context-aware structured message at error level.
func (l *Logger) ErrorContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Log(ctx, levelError(), msg, keysAndValues...)
}
