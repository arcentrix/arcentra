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

package database

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"go.uber.org/zap"
	"gorm.io/gorm/logger"
)

type GormLoggerAdapter struct {
	Config logger.Config
	Level  logger.LogLevel
}

var (
	gormLogger     *zap.SugaredLogger
	gormLoggerOnce sync.Once
)

func (l *GormLoggerAdapter) getLogger() *zap.SugaredLogger {
	gormLoggerOnce.Do(func() {
		baseLogger := log.GetLogger().Desugar()
		gormLogger = baseLogger.WithOptions(zap.AddCallerSkip(2)).Sugar()
	})
	return gormLogger
}

func NewGormLoggerAdapter(config logger.Config, logLevel logger.LogLevel) *GormLoggerAdapter {
	return &GormLoggerAdapter{
		Config: config,
		Level:  logLevel,
	}
}

func (l *GormLoggerAdapter) LogMode(level logger.LogLevel) logger.Interface {
	l.Level = level
	return l
}

func (l *GormLoggerAdapter) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.Level < logger.Info {
		return
	}
	l.getLogger().Infow(msg, data...)
}

func (l *GormLoggerAdapter) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.Level < logger.Warn {
		return
	}
	l.getLogger().Warnw(msg, data...)
}

func (l *GormLoggerAdapter) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.Level < logger.Error {
		return
	}
	l.getLogger().Errorw(msg, data...)
}

func (l *GormLoggerAdapter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.Level <= logger.Silent {
		return
	}

	elapsed := time.Since(begin).Seconds() // convert to seconds
	sql, rows := fc()

	if err != nil && l.Config.LogLevel >= logger.Error && (!errors.Is(err, logger.ErrRecordNotFound) || !l.Config.IgnoreRecordNotFoundError) {
		l.getLogger().Errorw("SQL query failed", "sql", sql, "rows", rows, "elapsed", elapsed, "error", err)
		return
	}

	if elapsed > l.Config.SlowThreshold.Seconds() && l.Config.SlowThreshold.Seconds() != 0 && l.Config.LogLevel >= logger.Warn {
		l.getLogger().Warnw("Slow SQL query", "sql", sql, "rows", rows, "elapsed", elapsed)
		return
	}

	if l.Config.LogLevel == logger.Info {
		l.getLogger().Debugw("SQL query", "sql", sql, "rows", rows, "elapsed", elapsed)
	}
}
