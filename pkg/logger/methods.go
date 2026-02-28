package logger

import (
	"context"
	"fmt"
)

// Info logs a message at info level.
func (l *Logger) Info(args ...any) {
	l.Logger.Log(defaultContext(), levelInfo(), fmt.Sprint(args...))
}

// Infow logs a structured message at info level.
func (l *Logger) Infow(msg string, keysAndValues ...any) {
	l.InfoContext(defaultContext(), msg, keysAndValues...)
}

// InfoContext logs a context-aware structured message at info level.
func (l *Logger) InfoContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Logger.Log(ctx, levelInfo(), msg, keysAndValues...)
}

// Debug logs a message at debug level.
func (l *Logger) Debug(args ...any) {
	l.Logger.Log(defaultContext(), levelDebug(), fmt.Sprint(args...))
}

// Debugw logs a structured message at debug level.
func (l *Logger) Debugw(msg string, keysAndValues ...any) {
	l.DebugContext(defaultContext(), msg, keysAndValues...)
}

// DebugContext logs a context-aware structured message at debug level.
func (l *Logger) DebugContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Logger.Log(ctx, levelDebug(), msg, keysAndValues...)
}

// Warn logs a message at warn level.
func (l *Logger) Warn(args ...any) {
	l.Logger.Log(defaultContext(), levelWarn(), fmt.Sprint(args...))
}

// Warnw logs a structured message at warn level.
func (l *Logger) Warnw(msg string, keysAndValues ...any) {
	l.WarnContext(defaultContext(), msg, keysAndValues...)
}

// WarnContext logs a context-aware structured message at warn level.
func (l *Logger) WarnContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Logger.Log(ctx, levelWarn(), msg, keysAndValues...)
}

// Error logs a message at error level.
func (l *Logger) Error(args ...any) {
	l.Logger.Log(defaultContext(), levelError(), fmt.Sprint(args...))
}

// Errorw logs a structured message at error level.
func (l *Logger) Errorw(msg string, keysAndValues ...any) {
	l.ErrorContext(defaultContext(), msg, keysAndValues...)
}

// ErrorContext logs a context-aware structured message at error level.
func (l *Logger) ErrorContext(ctx context.Context, msg string, keysAndValues ...any) {
	l.Logger.Log(ctx, levelError(), msg, keysAndValues...)
}
