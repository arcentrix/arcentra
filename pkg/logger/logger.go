package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/wire"
)

var (
	mu     sync.RWMutex
	global *slog.Logger
	once   sync.Once
)

// ProviderSet is the Wire provider set for the logger package.
var ProviderSet = wire.NewSet(ProvideLogger)

// Conf defines logger configuration.
type Conf struct {
	Output     string
	Path       string
	Filename   string
	Level      string
	KeepHours  int
	RotateSize int
	RotateNum  int
}

// Logger wraps slog.Logger to satisfy dependency injection usage.
type Logger struct {
	*slog.Logger
}

// ProvideLogger creates a dependency-injected logger instance.
func ProvideLogger(conf *Conf) (*Logger, error) {
	l, err := New(conf)
	if err != nil {
		return nil, err
	}
	return &Logger{Logger: l}, nil
}

// SetDefaults returns default logger configuration.
func SetDefaults() *Conf {
	return &Conf{
		Output:     "stdout",
		Path:       "./logs",
		Filename:   "app.log",
		Level:      "INFO",
		KeepHours:  7,
		RotateSize: 100,
		RotateNum:  10,
	}
}

// Validate validates and normalizes logger configuration.
func (c *Conf) Validate() error {
	if c == nil {
		return fmt.Errorf("logger config is nil")
	}
	if c.Output == "" {
		c.Output = "stdout"
	}
	if c.Level == "" {
		c.Level = "INFO"
	}
	if c.Output == "file" {
		if c.Path == "" {
			return fmt.Errorf("log path is required when output is 'file'")
		}
		if c.Filename == "" {
			c.Filename = "app.log"
		}
		if c.RotateSize <= 0 {
			c.RotateSize = 100
		}
		if c.RotateNum <= 0 {
			c.RotateNum = 10
		}
		if c.KeepHours <= 0 {
			c.KeepHours = 7
		}
	}
	return nil
}

// New creates a slog logger and also updates the global logger instance.
func New(conf *Conf) (*slog.Logger, error) {
	if conf == nil {
		conf = SetDefaults()
	}
	l, err := buildLogger(conf, "")
	if err != nil {
		return nil, err
	}

	mu.Lock()
	global = l
	mu.Unlock()

	initGlobalManagerWithDefault(l)

	l.Log(context.Background(), slog.LevelDebug, "logger initialized", "output", conf.Output, "level", conf.Level)
	return l, nil
}

// NewWithCategory creates a logger and always appends category field.
func NewWithCategory(conf *Conf, category string) (*slog.Logger, error) {
	return buildLogger(conf, category)
}

// buildLogger creates a slog logger from configuration.
func buildLogger(conf *Conf, category string) (*slog.Logger, error) {
	if conf == nil {
		conf = SetDefaults()
	}
	if err := conf.Validate(); err != nil {
		return nil, fmt.Errorf("invalid logger config: %w", err)
	}

	output, err := buildOutputWriter(conf)
	if err != nil {
		return nil, err
	}

	handlerOptions := &slog.HandlerOptions{
		Level: parseLogLevel(conf.Level),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.Format("2006-01-02 15:04:05"))
				}
			}
			return a
		},
	}

	base := slog.NewTextHandler(output, handlerOptions)
	l := slog.New(newLogTrace(base))
	if strings.TrimSpace(category) != "" {
		l = l.With("category", strings.TrimSpace(category))
	}
	return l, nil
}

// Init initializes the global logger instance.
func Init(conf *Conf) error {
	_, err := New(conf)
	return err
}

// MustInit initializes the global logger and panics on failure.
func MustInit(conf *Conf) {
	if err := Init(conf); err != nil {
		panic(fmt.Sprintf("failed to initialize logger: %v", err))
	}
}

// GetLogger returns the global slog logger.
func GetLogger() *slog.Logger {
	ensureLogger()
	mu.RLock()
	defer mu.RUnlock()
	return global
}

// GetLevel returns the current configured log level.
func GetLevel() slog.Level {
	mu.RLock()
	defer mu.RUnlock()
	if global == nil {
		return slog.LevelInfo
	}
	ctx := context.Background()
	switch {
	case global.Enabled(ctx, slog.LevelDebug):
		return slog.LevelDebug
	case global.Enabled(ctx, slog.LevelInfo):
		return slog.LevelInfo
	case global.Enabled(ctx, slog.LevelWarn):
		return slog.LevelWarn
	case global.Enabled(ctx, slog.LevelError):
		return slog.LevelError
	default:
		return slog.LevelError + 4
	}
}

// Sync keeps compatibility with zap-style lifecycle and always returns nil.
func Sync() error {
	return nil
}

// parseLogLevel converts string level to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// buildOutputWriter builds the writer for stdout or file output.
func buildOutputWriter(conf *Conf) (io.Writer, error) {
	switch conf.Output {
	case "stdout":
		return os.Stdout, nil
	case "file":
		return getFileLogWriter(conf)
	default:
		return os.Stdout, nil
	}
}

// ensureLogger initializes global logger lazily with default configuration.
func ensureLogger() {
	mu.RLock()
	initialized := global != nil
	mu.RUnlock()
	if initialized {
		return
	}

	once.Do(func() {
		if _, err := New(SetDefaults()); err != nil {
			fallback := slog.New(newLogTrace(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
			mu.Lock()
			global = fallback
			mu.Unlock()
			initGlobalManagerWithDefault(fallback)
		}
	})
}
