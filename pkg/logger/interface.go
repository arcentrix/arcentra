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

import "context"

// ILogger defines the logging interface for business code.
type ILogger interface {
	// Info logs a message at info level.
	Info(args ...any)
	// Infow logs a structured message at info level.
	Infow(msg string, keysAndValues ...any)
	// InfoContext logs a context-aware structured message at info level.
	InfoContext(ctx context.Context, msg string, keysAndValues ...any)

	// Debug logs a message at debug level.
	Debug(args ...any)
	// Debugw logs a structured message at debug level.
	Debugw(msg string, keysAndValues ...any)
	// DebugContext logs a context-aware structured message at debug level.
	DebugContext(ctx context.Context, msg string, keysAndValues ...any)

	// Warn logs a message at warn level.
	Warn(args ...any)
	// Warnw logs a structured message at warn level.
	Warnw(msg string, keysAndValues ...any)
	// WarnContext logs a context-aware structured message at warn level.
	WarnContext(ctx context.Context, msg string, keysAndValues ...any)

	// Error logs a message at error level.
	Error(args ...any)
	// Errorw logs a structured message at error level.
	Errorw(msg string, keysAndValues ...any)
	// ErrorContext logs a context-aware structured message at error level.
	ErrorContext(ctx context.Context, msg string, keysAndValues ...any)
}
