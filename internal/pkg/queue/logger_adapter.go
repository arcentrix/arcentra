// Copyright 2025 Arcade Team
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

package queue

import (
	"github.com/arcentrix/arcade/pkg/log"
)

// asynqLoggerAdapter 适配器，将 asynq.Logger 接口适配到 pkg/log
type asynqLoggerAdapter struct{}

// Debug 实现 asynq.Logger 接口
func (l *asynqLoggerAdapter) Debug(args ...any) {
	log.Debug(args...)
}

// Info 实现 asynq.Logger 接口
func (l *asynqLoggerAdapter) Info(args ...any) {
	log.Info(args...)
}

// Warn 实现 asynq.Logger 接口
func (l *asynqLoggerAdapter) Warn(args ...any) {
	log.Warn(args...)
}

// Error 实现 asynq.Logger 接口
func (l *asynqLoggerAdapter) Error(args ...any) {
	log.Error(args...)
}

// Fatal 实现 asynq.Logger 接口
func (l *asynqLoggerAdapter) Fatal(args ...any) {
	log.Fatal(args...)
}
