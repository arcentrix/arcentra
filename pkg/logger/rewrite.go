package logger

import (
	"context"
	"fmt"
	"log/slog"

	tracectx "github.com/arcentrix/arcentra/pkg/trace/context"
)

// defaultContext returns trace-aware context for global logging methods.
func defaultContext() context.Context {
	ctx := tracectx.GetContext()
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

// Info logs an info message.
func Info(args ...any) {
	GetLogger().Log(defaultContext(), levelInfo(), fmt.Sprint(args...))
}

// Infow logs a structured info message.
func Infow(msg string, keysAndValues ...any) {
	InfoContext(defaultContext(), msg, keysAndValues...)
}

// InfoContext logs a structured info message with context.
func InfoContext(ctx context.Context, msg string, keysAndValues ...any) {
	GetLogger().Log(ctx, levelInfo(), msg, keysAndValues...)
}

// Debug logs a debug message.
func Debug(args ...any) {
	GetLogger().Log(defaultContext(), levelDebug(), fmt.Sprint(args...))
}

// Debugw logs a structured debug message.
func Debugw(msg string, keysAndValues ...any) {
	DebugContext(defaultContext(), msg, keysAndValues...)
}

// DebugContext logs a structured debug message with context.
func DebugContext(ctx context.Context, msg string, keysAndValues ...any) {
	GetLogger().Log(ctx, levelDebug(), msg, keysAndValues...)
}

// Warn logs a warn message.
func Warn(args ...any) {
	GetLogger().Log(defaultContext(), levelWarn(), fmt.Sprint(args...))
}

// Warnw logs a structured warn message.
func Warnw(msg string, keysAndValues ...any) {
	WarnContext(defaultContext(), msg, keysAndValues...)
}

// WarnContext logs a structured warn message with context.
func WarnContext(ctx context.Context, msg string, keysAndValues ...any) {
	GetLogger().Log(ctx, levelWarn(), msg, keysAndValues...)
}

// Error logs an error message.
func Error(args ...any) {
	GetLogger().Log(defaultContext(), levelError(), fmt.Sprint(args...))
}

// Errorw logs a structured error message.
func Errorw(msg string, keysAndValues ...any) {
	ErrorContext(defaultContext(), msg, keysAndValues...)
}

// ErrorContext logs a structured error message with context.
func ErrorContext(ctx context.Context, msg string, keysAndValues ...any) {
	GetLogger().Log(ctx, levelError(), msg, keysAndValues...)
}

// levelDebug returns slog debug level.
func levelDebug() slog.Level {
	return slog.LevelDebug
}

// levelInfo returns slog info level.
func levelInfo() slog.Level {
	return slog.LevelInfo
}

// levelWarn returns slog warn level.
func levelWarn() slog.Level {
	return slog.LevelWarn
}

// levelError returns slog error level.
func levelError() slog.Level {
	return slog.LevelError
}
