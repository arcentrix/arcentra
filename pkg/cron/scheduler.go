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

package cron

import (
	"errors"
	"sync"

	"github.com/arcentrix/arcentra/pkg/log"
)

// ErrNotInitialized is returned when trying to use global cron before initialization
var ErrNotInitialized = errors.New("global cron instance is not initialized")

var (
	globalCron *Cron
	globalMu   sync.RWMutex
	once       sync.Once
)

// InitGlobal initializes the global cron instance
func Init(logger *log.Logger, opts ...OpOption) {
	once.Do(func() {
		globalMu.Lock()
		defer globalMu.Unlock()

		globalCron = New(opts...)
		if logger != nil {
			globalCron.ErrorLog = logger
		}
	})
}

// GetGlobal returns the global cron instance
// Returns nil if not initialized
func Get() *Cron {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalCron
}

// StartGlobal starts the global cron scheduler
func Start() {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c != nil {
		c.Start()
	}
}

// Stop stops the global cron scheduler
func Stop() {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c != nil {
		c.Stop()
	}
}

// AddFunc adds a func to the global cron instance
func AddFunc(spec string, cmd func(), names ...string) error {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c == nil {
		return ErrNotInitialized
	}

	return c.AddFunc(spec, cmd, names...)
}

// AddJob adds a job to the global cron instance
func AddJob(spec string, cmd Job, names ...string) error {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c == nil {
		return ErrNotInitialized
	}

	return c.AddJob(spec, cmd, names...)
}

// Remove removes a job from the global cron instance
func Remove(name string) error {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c == nil {
		return ErrNotInitialized
	}

	return c.Remove(name)
}

// Entries returns all entries from the global cron instance
func Entries() []*Entry {
	globalMu.RLock()
	c := globalCron
	globalMu.RUnlock()

	if c == nil {
		return nil
	}

	return c.Entries()
}
