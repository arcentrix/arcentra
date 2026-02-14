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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/wire"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	mu     sync.RWMutex
	logger *zap.Logger
	sugar  *zap.SugaredLogger
)

// ProviderSet is the Wire provider set for the log package.
var ProviderSet = wire.NewSet(ProvideLogger)

// ProvideLogger 提供 Logger 实例
func ProvideLogger(conf *Conf) (*Logger, error) {
	zapLogger, err := NewLog(conf)
	if err != nil {
		return nil, err
	}
	return &Logger{Log: zapLogger.Sugar()}, nil
}

// Conf holds Conf configuration options.
type Conf struct {
	Output       string
	Path         string
	Filename     string // 日志文件名，为空时使用默认值
	Level        string
	KeepHours    int // 日志保留天数（改为导出）
	RotateSize   int // 单个日志文件最大大小（MB）
	RotateNum    int // 保留的日志文件数量
	KafkaBrokers string
	KafkaTopic   string
}

// SetDefaults 返回默认配置
func SetDefaults() *Conf {
	return &Conf{
		Output:     "stdout",
		Path:       "./logs",
		Filename:   "app.log",
		Level:      "INFO",
		KeepHours:  7,   // 默认保留7天
		RotateSize: 100, // 默认100MB
		RotateNum:  10,  // 默认保留10个文件
	}
}

// Validate 验证配置
func (c *Conf) Validate() error {
	if c.Output == "file" {
		if c.Path == "" {
			return fmt.Errorf("log path is required when output is 'file'")
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

type Logger struct {
	Log *zap.SugaredLogger
}

type Option func(*Logger)

// NewLog initializes the logger and returns a zap.Logger.
func NewLog(conf *Conf) (*zap.Logger, error) {
	// 验证配置
	if err := conf.Validate(); err != nil {
		return nil, fmt.Errorf("invalid log config: %w", err)
	}

	var (
		writeSyncer zapcore.WriteSyncer
		encoder     zapcore.Encoder
		core        zapcore.Core
	)

	encoder = getEncoder()

	switch conf.Output {
	case "stdout":
		writeSyncer = zapcore.AddSync(os.Stdout)
	case "file":
		var err error
		writeSyncer, err = getFileLogWriter(conf)
		if err != nil {
			return nil, fmt.Errorf("failed to create file log writer: %w", err)
		}
	default:
		writeSyncer = zapcore.AddSync(os.Stdout)
	}

	core = zapcore.NewCore(encoder, writeSyncer, parseLogLevel(conf.Level))
	core = &traceCore{
		Core: core,
	}
	newLogger := zap.New(core, zap.AddCallerSkip(1), zap.AddCaller())

	mu.Lock()
	logger = newLogger
	sugar = newLogger.Sugar()
	mu.Unlock()

	// 使用 Debug 级别输出初始化信息，确保即使配置为 DEBUG 级别也能看到
	sugar.Debugw("log initialized",
		"output", conf.Output,
		"level", conf.Level,
	)

	return newLogger, nil
}

// Init 初始化全局日志实例（便捷方法）
func Init(conf *Conf) error {
	_, err := NewLog(conf)
	return err
}

// MustInit 初始化全局日志实例，失败则 panic
func MustInit(conf *Conf) {
	if err := Init(conf); err != nil {
		panic(fmt.Sprintf("failed to initialize logger: %v", err))
	}
}

// GetLogger 获取全局 zap.Logger 实例
func GetLogger() *zap.SugaredLogger {
	ensureLogger()
	mu.RLock()
	defer mu.RUnlock()
	return sugar
}

// GetLevel 获取当前日志级别
func GetLevel() zapcore.Level {
	mu.RLock()
	defer mu.RUnlock()
	if logger == nil {
		return zapcore.InfoLevel
	}
	// 通过检查不同级别是否启用来确定当前级别
	core := logger.Core()
	if core.Enabled(zapcore.DebugLevel) {
		return zapcore.DebugLevel
	}
	if core.Enabled(zapcore.InfoLevel) {
		return zapcore.InfoLevel
	}
	if core.Enabled(zapcore.WarnLevel) {
		return zapcore.WarnLevel
	}
	if core.Enabled(zapcore.ErrorLevel) {
		return zapcore.ErrorLevel
	}
	return zapcore.FatalLevel
}

// getEncoder returns the appropriate encoder based on the mode.
func getEncoder() zapcore.Encoder {
	encoderConfig := zap.NewDevelopmentEncoderConfig()

	encoderConfig.TimeKey = "time"
	encoderConfig.LevelKey = "level"
	encoderConfig.NameKey = "Conf"
	encoderConfig.CallerKey = "caller"
	encoderConfig.MessageKey = "msg"
	encoderConfig.StacktraceKey = "stacktrace"
	encoderConfig.LineEnding = zapcore.DefaultLineEnding
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder // 大写编码器
	encoderConfig.EncodeTime = customTimeEncoder            // ISO8601 UTC 时间格式
	encoderConfig.EncodeDuration = zapcore.SecondsDurationEncoder
	encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder // 相对路径编码器
	encoderConfig.EncodeName = zapcore.FullNameEncoder

	return zapcore.NewConsoleEncoder(encoderConfig)
}

// customTimeEncoder formats the time as 2006-01-02 15:04:05.
func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

// parseLogLevel converts a string level to a zapcore.Level.
// Supports case-insensitive matching.
func parseLogLevel(level string) zapcore.Level {
	levelUpper := strings.ToUpper(strings.TrimSpace(level))

	switch levelUpper {
	case "DEBUG":
		return zapcore.DebugLevel
	case "INFO":
		return zapcore.InfoLevel
	case "WARN", "WARNING":
		return zapcore.WarnLevel
	case "ERROR":
		return zapcore.ErrorLevel
	case "FATAL":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}
