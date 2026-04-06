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
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tracectx "github.com/arcentrix/arcentra/pkg/trace/context"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	defaultOutputStdout = "stdout"
	defaultLogLevelInfo = "INFO"
)

// TestSetDefaults verifies default logger configuration.
func TestSetDefaults(t *testing.T) {
	conf := SetDefaults()
	if conf.Output != defaultOutputStdout {
		t.Fatalf("expected output stdout, got %s", conf.Output)
	}
	if conf.Level != defaultLogLevelInfo {
		t.Fatalf("expected level INFO, got %s", conf.Level)
	}
	if conf.Filename == "" {
		t.Fatal("expected default filename to be set")
	}
}

// TestConfValidate verifies config validation and normalization.
func TestConfValidate(t *testing.T) {
	conf := &Conf{Output: "file", Path: "/tmp/test-logger"}
	if err := conf.Validate(); err != nil {
		t.Fatalf("validate should pass: %v", err)
	}
	if conf.RotateSize <= 0 || conf.RotateNum <= 0 || conf.KeepHours <= 0 {
		t.Fatal("expected file rotation values to be auto-filled")
	}
}

// TestNewFileOutput verifies file output works with slog backend.
func TestNewFileOutput(t *testing.T) {
	tmpDir := t.TempDir()
	conf := &Conf{
		Output:   "file",
		Path:     tmpDir,
		Filename: "logger.log",
		Level:    "INFO",
	}

	l, err := New(conf)
	if err != nil {
		t.Fatalf("New() should not fail: %v", err)
	}

	l.Info("file output test")
	logFile := filepath.Join(tmpDir, "logger.log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("expected log file content to be non-empty")
	}
}

// TestParseLogLevel verifies log-level parsing behavior.
func TestParseLogLevel(t *testing.T) {
	if parseLogLevel("debug") != slog.LevelDebug {
		t.Fatal("expected DEBUG to map to slog.LevelDebug")
	}
	if parseLogLevel("warn") != slog.LevelWarn {
		t.Fatal("expected WARN to map to slog.LevelWarn")
	}
	if parseLogLevel("unknown") != slog.LevelInfo {
		t.Fatal("expected unknown level to map to slog.LevelInfo")
	}
}

// TestOTelHandlerWithContext verifies trace fields are injected from context.
func TestOTelHandlerWithContext(t *testing.T) {
	var buf bytes.Buffer
	h := newLogTrace(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := slog.New(h)

	tp := sdktrace.NewTracerProvider()
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()
	ctx, span := tp.Tracer("logger-test").Start(context.Background(), "span")
	l.InfoContext(ctx, "hello")
	span.End()

	logLine := buf.String()
	if !strings.Contains(logLine, "trace_id=") {
		t.Fatalf("expected trace_id in log line: %s", logLine)
	}
	if !strings.Contains(logLine, "span_id=") {
		t.Fatalf("expected span_id in log line: %s", logLine)
	}
}

// TestOTelHandlerFallbackContext verifies fallback context extraction works.
func TestOTelHandlerFallbackContext(t *testing.T) {
	var buf bytes.Buffer
	h := newLogTrace(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := slog.New(h)

	tp := sdktrace.NewTracerProvider()
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()
	ctx, span := tp.Tracer("logger-test").Start(context.Background(), "fallback-span")
	tracectx.SetContext(ctx)
	defer tracectx.ClearContext()

	l.Info("hello without explicit context")
	span.End()

	logLine := buf.String()
	if !strings.Contains(logLine, "trace_id=") {
		t.Fatalf("expected trace_id in fallback log line: %s", logLine)
	}
}

// TestInitMulti verifies multi-category logger initialization.
func TestInitMulti(t *testing.T) {
	tmpDir := t.TempDir()
	conf := &MultiConf{
		Default: &Conf{
			Output:   "file",
			Path:     tmpDir,
			Filename: "app.log",
			Level:    "INFO",
		},
		Categories: map[string]*Conf{
			"http": {
				Output:   "file",
				Path:     tmpDir,
				Filename: "http.log",
				Level:    "INFO",
			},
			"plugin": {
				Output:   "file",
				Path:     tmpDir,
				Filename: "plugin.log",
				Level:    "INFO",
			},
		},
	}

	if err := InitMulti(conf); err != nil {
		t.Fatalf("InitMulti() should not fail: %v", err)
	}

	Category("http").Infow("http request", "path", "/health")
	Category("plugin").Infow("plugin run", "name", "git")
	Infow("default run", "module", "app")

	httpContent, err := os.ReadFile(filepath.Join(tmpDir, "http.log"))
	if err != nil {
		t.Fatalf("failed to read http.log: %v", err)
	}
	if !strings.Contains(string(httpContent), "\"category\":\"http\"") {
		t.Fatalf("expected category=http in http.log: %s", string(httpContent))
	}

	pluginContent, err := os.ReadFile(filepath.Join(tmpDir, "plugin.log"))
	if err != nil {
		t.Fatalf("failed to read plugin.log: %v", err)
	}
	if !strings.Contains(string(pluginContent), "\"category\":\"plugin\"") {
		t.Fatalf("expected category=plugin in plugin.log: %s", string(pluginContent))
	}

	defaultContent, err := os.ReadFile(filepath.Join(tmpDir, "app.log"))
	if err != nil {
		t.Fatalf("failed to read app.log: %v", err)
	}
	if !strings.Contains(string(defaultContent), "\"category\":\"default\"") {
		t.Fatalf("expected category=default in app.log: %s", string(defaultContent))
	}
}

// TestCategoryFallback verifies unknown category falls back to default logger (category=default only).
func TestCategoryFallback(t *testing.T) {
	tmpDir := t.TempDir()
	conf := &MultiConf{
		Default: &Conf{
			Output:   "file",
			Path:     tmpDir,
			Filename: "fallback.log",
			Level:    "INFO",
		},
	}

	if err := InitMulti(conf); err != nil {
		t.Fatalf("InitMulti() should not fail: %v", err)
	}

	Category("cron").Infow("cron event", "task", "cleanup")
	content, err := os.ReadFile(filepath.Join(tmpDir, "fallback.log"))
	if err != nil {
		t.Fatalf("failed to read fallback.log: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "\"category\":\"default\"") {
		t.Fatalf("expected fallback log to include category=default: %s", text)
	}
}

func TestPrintLog(_ *testing.T) {
	_ = Init(SetDefaults())
	Info("test")
	Infow("test", "key", "value")
	Debug("test")
	Debugw("test", "key", "value")
	Warn("test")
	Warnw("test", "key", "value")
	Error("test")
	Errorw("test", "key", "value")

	ch := Category("cron")
	ch.Info("test")
	ch.Infow("test", "key", "value")
	ch.Debug("test")
	ch.Debugw("test", "key", "value")
	ch.Warn("test")
	ch.Warnw("test", "key", "value")
	ch.Error("test")
	ch.Errorw("test", "key", "value")

	ch.InfoContext(context.Background(), "test", "key", "value")
	ch.DebugContext(context.Background(), "test", "key", "value")
	ch.WarnContext(context.Background(), "test", "key", "value")
	ch.ErrorContext(context.Background(), "test", "key", "value")
}
