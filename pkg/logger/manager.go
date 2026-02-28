package logger

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/google/wire"
)

var (
	managerMu    sync.RWMutex
	globalManager IManager
)

// ManagerProviderSet is the Wire provider set for logger manager.
var ManagerProviderSet = wire.NewSet(ProvideManager)

// IManager defines named logger management behavior.
type IManager interface {
	// Get returns logger by name. It falls back to default logger when name does not exist.
	Get(name string) *Logger
	// Names returns all registered logger names in ascending order.
	Names() []string
}

// MultiConf defines multi-channel logger configuration.
type MultiConf struct {
	Default  *Conf
	Channels map[string]*Conf
}

// SetDefaults initializes missing fields for MultiConf.
func (c *MultiConf) SetDefaults() {
	if c.Default == nil {
		c.Default = SetDefaults()
	}
	if c.Channels == nil {
		c.Channels = map[string]*Conf{}
	}
}

// Validate validates and normalizes MultiConf.
func (c *MultiConf) Validate() error {
	if c == nil {
		return fmt.Errorf("multi logger config is nil")
	}
	c.SetDefaults()
	if err := c.Default.Validate(); err != nil {
		return fmt.Errorf("invalid default logger config: %w", err)
	}
	for name, conf := range c.Channels {
		n := strings.TrimSpace(name)
		if n == "" {
			return fmt.Errorf("logger channel name cannot be empty")
		}
		if conf == nil {
			conf = cloneConf(c.Default)
			c.Channels[name] = conf
		}
		inheritConf(conf, c.Default)
		if err := conf.Validate(); err != nil {
			return fmt.Errorf("invalid logger config for channel %q: %w", name, err)
		}
	}
	return nil
}

type manager struct {
	defaultLogger *Logger
	channels      map[string]*Logger
}

// InitMulti initializes a manager with default + named channel loggers.
func InitMulti(conf *MultiConf) error {
	m, err := NewManager(conf)
	if err != nil {
		return err
	}
	setGlobalManager(m)

	mu.Lock()
	global = m.Get("").Logger
	mu.Unlock()
	return nil
}

// MustInitMulti initializes multi logger and panics on failure.
func MustInitMulti(conf *MultiConf) {
	if err := InitMulti(conf); err != nil {
		panic(fmt.Sprintf("failed to initialize multi logger: %v", err))
	}
}

// NewManager builds a logger manager from multi config.
func NewManager(conf *MultiConf) (IManager, error) {
	if conf == nil {
		conf = &MultiConf{}
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}

	defaultSlog, err := buildLogger(conf.Default, "")
	if err != nil {
		return nil, err
	}
	m := &manager{
		defaultLogger: &Logger{Logger: defaultSlog.With("category", "default")},
		channels:      make(map[string]*Logger, len(conf.Channels)),
	}

	for name, channelConf := range conf.Channels {
		channelName := strings.TrimSpace(name)
		channelLogger, channelErr := buildLogger(channelConf, channelName)
		if channelErr != nil {
			return nil, channelErr
		}
		m.channels[channelName] = &Logger{Logger: channelLogger}
	}

	return m, nil
}

// ProvideManager creates manager for dependency injection.
func ProvideManager(conf *MultiConf) (IManager, error) {
	return NewManager(conf)
}

// Channel returns named channel logger from global manager.
func Channel(name string) *Logger {
	return GetManager().Get(name)
}

// GetManager returns global logger manager.
func GetManager() IManager {
	managerMu.RLock()
	if globalManager != nil {
		defer managerMu.RUnlock()
		return globalManager
	}
	managerMu.RUnlock()

	ensureLogger()
	return defaultManagerFromGlobal()
}

// Get returns logger by channel name.
func (m *manager) Get(name string) *Logger {
	if m == nil || m.defaultLogger == nil {
		return &Logger{Logger: GetLogger()}
	}
	channelName := strings.TrimSpace(name)
	if channelName == "" || strings.EqualFold(channelName, "default") {
		return m.defaultLogger
	}
	if l, ok := m.channels[channelName]; ok {
		return l
	}
	return &Logger{Logger: m.defaultLogger.With("channel", channelName)}
}

// Names returns registered channel names.
func (m *manager) Names() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.channels)+1)
	names = append(names, "default")
	for channelName := range m.channels {
		names = append(names, channelName)
	}
	sort.Strings(names)
	return names
}

// initGlobalManagerWithDefault initializes global manager with a default logger.
func initGlobalManagerWithDefault(defaultLogger *slog.Logger) {
	if defaultLogger == nil {
		return
	}
	setGlobalManager(&manager{
		defaultLogger: &Logger{Logger: defaultLogger.With("category", "default")},
		channels:      map[string]*Logger{},
	})
}

// setGlobalManager sets the process-level logger manager.
func setGlobalManager(m IManager) {
	managerMu.Lock()
	defer managerMu.Unlock()
	globalManager = m
}

// defaultManagerFromGlobal builds and returns a manager backed by global logger.
func defaultManagerFromGlobal() IManager {
	managerMu.RLock()
	if globalManager != nil {
		defer managerMu.RUnlock()
		return globalManager
	}
	managerMu.RUnlock()

	setGlobalManager(&manager{
		defaultLogger: &Logger{Logger: GetLogger().With("category", "default")},
		channels:      map[string]*Logger{},
	})

	managerMu.RLock()
	defer managerMu.RUnlock()
	return globalManager
}

// cloneConf creates a shallow copy of logger configuration.
func cloneConf(src *Conf) *Conf {
	if src == nil {
		return SetDefaults()
	}
	copied := *src
	return &copied
}

// inheritConf fills destination config with fallback values.
func inheritConf(dst *Conf, fallback *Conf) {
	if dst == nil || fallback == nil {
		return
	}
	if dst.Output == "" {
		dst.Output = fallback.Output
	}
	if dst.Path == "" {
		dst.Path = fallback.Path
	}
	if dst.Filename == "" {
		dst.Filename = fallback.Filename
	}
	if dst.Level == "" {
		dst.Level = fallback.Level
	}
	if dst.KeepHours <= 0 {
		dst.KeepHours = fallback.KeepHours
	}
	if dst.RotateSize <= 0 {
		dst.RotateSize = fallback.RotateSize
	}
	if dst.RotateNum <= 0 {
		dst.RotateNum = fallback.RotateNum
	}
}
