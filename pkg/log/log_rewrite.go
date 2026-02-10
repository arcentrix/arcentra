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
	"strings"
	"sync"

	"go.uber.org/zap"
)

var once sync.Once

// ensureLogger 确保 logger 已初始化，如果没有则使用默认配置
func ensureLogger() {
	mu.RLock()
	initialized := sugar != nil
	mu.RUnlock()

	if !initialized {
		once.Do(func() {
			// 使用默认配置初始化
			if err := Init(SetDefaults()); err != nil {
				// 如果初始化失败，创建一个基本的 stdout logger
				zapLogger, _ := zap.NewProduction()
				mu.Lock()
				logger = zapLogger
				sugar = zapLogger.Sugar()
				mu.Unlock()
			}
		})
	}
}

// getSugar 线程安全地获取 sugar logger
func getSugar() *zap.SugaredLogger {
	ensureLogger()
	mu.RLock()
	defer mu.RUnlock()
	return sugar
}

func Info(args ...interface{}) {
	getSugar().Info(args...)
}

func Infow(msg string, keysAndValues ...interface{}) {
	getSugar().Infow(msg, keysAndValues...)
}

func Debug(args ...interface{}) {
	getSugar().Debug(args...)
}

func Debugw(msg string, keysAndValues ...interface{}) {
	getSugar().Debugw(msg, keysAndValues...)
}

func Warn(args ...interface{}) {
	getSugar().Warn(args...)
}

func Warnw(msg string, keysAndValues ...interface{}) {
	getSugar().Warnw(msg, keysAndValues...)
}

func Error(args ...interface{}) {
	getSugar().Error(args...)
}

func Errorw(msg string, keysAndValues ...interface{}) {
	getSugar().Errorw(msg, keysAndValues...)
}

func Fatal(args ...interface{}) {
	getSugar().Fatal(args...)
}

func Fatalw(msg string, keysAndValues ...interface{}) {
	getSugar().Fatalw(msg, keysAndValues...)
}

// Sync 刷新缓冲区
func Sync() error {
	mu.RLock()
	defer mu.RUnlock()
	if logger != nil {
		err := logger.Sync()
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "bad file descriptor") {
			return nil
		}
		return err
	}
	return nil
}
