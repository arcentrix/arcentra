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

package queue

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAsynqLoggerAdapter(t *testing.T) {
	adapter := &asynqLoggerAdapter{}

	tests := []struct {
		name string
		call func()
	}{
		{
			name: "Debug",
			call: func() {
				adapter.Debug("test debug message")
			},
		},
		{
			name: "Info",
			call: func() {
				adapter.Info("test info message")
			},
		},
		{
			name: "Warn",
			call: func() {
				adapter.Warn("test warn message")
			},
		},
		{
			name: "Error",
			call: func() {
				adapter.Error("test error message")
			},
		},
		{
			name: "Fatal",
			call: func() {
				// Fatal 会调用 os.Exit，在测试中跳过
				// adapter.Fatal("test fatal message")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这些方法应该不 panic
			assert.NotPanics(t, tt.call)
		})
	}
}

func TestAsynqLoggerAdapter_MultipleArgs(t *testing.T) {
	adapter := &asynqLoggerAdapter{}

	assert.NotPanics(t, func() {
		adapter.Debug("message", "arg1", "arg2")
		adapter.Info("message", "arg1", "arg2")
		adapter.Warn("message", "arg1", "arg2")
		adapter.Error("message", "arg1", "arg2")
	})
}
