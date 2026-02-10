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

package shutdown

import (
	"sync"
	"sync/atomic"
)

// Manager manages graceful shutdown state
type Manager struct {
	shuttingDown int32 // atomic flag: 0 = running, 1 = shutting down
	mu           sync.RWMutex
	shutdownChan chan struct{}
}

// NewManager creates a new shutdown manager
func NewManager() *Manager {
	return &Manager{
		shuttingDown: 0,
		shutdownChan: make(chan struct{}, 1),
	}
}

// IsShuttingDown returns true if the service is shutting down
func (m *Manager) IsShuttingDown() bool {
	return atomic.LoadInt32(&m.shuttingDown) == 1
}

// Shutdown triggers graceful shutdown
// Returns true if shutdown was triggered, false if already shutting down
func (m *Manager) Shutdown() bool {
	if !atomic.CompareAndSwapInt32(&m.shuttingDown, 0, 1) {
		return false // already shutting down
	}

	select {
	case m.shutdownChan <- struct{}{}:
	default:
		// channel already has signal
	}
	return true
}

// Wait waits for shutdown signal
func (m *Manager) Wait() <-chan struct{} {
	return m.shutdownChan
}
