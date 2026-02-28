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
