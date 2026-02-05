// Copyright 2025 Arcentra Team
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
	"path/filepath"

	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// getFileLogWriter returns the WriteSyncer for logging to a file.
func getFileLogWriter(config *Conf) (zapcore.WriteSyncer, error) {
	// confirm log directory if not exists
	if err := os.MkdirAll(config.Path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logPath := filepath.Join(config.Path, config.Filename)

	lumberJackLogger := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    config.RotateSize, // MB
		MaxBackups: config.RotateNum,
		MaxAge:     config.KeepHours, // days
		Compress:   true,
	}

	return zapcore.AddSync(lumberJackLogger), nil
}
