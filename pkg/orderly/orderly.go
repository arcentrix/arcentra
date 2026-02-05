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

package orderly

import "sync"

// Map is a thread-safe ordered map with a maximum size limit.
// It maintains insertion order and provides concurrent access protection.
type Map struct {
	mu      sync.RWMutex
	keys    []string
	values  map[string]any
	maxSize int
}

// New creates a new Map with the specified maximum size.
// The map will reject new entries once it reaches maxSize.
func New(maxSize int) *Map {
	return &Map{
		keys:    make([]string, 0, maxSize),
		values:  make(map[string]any),
		maxSize: maxSize,
	}
}

// Set adds or updates a key-value pair in the map.
// If the map has reached maxSize, new keys will be ignored.
// Existing keys will be updated without affecting the order.
func (m *Map) Set(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.keys) >= m.maxSize {
		return
	}

	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Get retrieves the value associated with the given key.
// It returns the value and a boolean indicating whether the key exists.
func (m *Map) Get(key string) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	value, ok := m.values[key]
	return value, ok
}

// Keys returns a copy of all keys in insertion order.
func (m *Map) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.keys...)
}

// ForEach iterates over all key-value pairs in insertion order.
// The provided function is called for each pair while holding the lock.
func (m *Map) ForEach(fn func(k string, v any)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, key := range m.keys {
		fn(key, m.values[key])
	}
}

// ToSlice returns all values as a slice in insertion order.
func (m *Map) ToSlice() []any {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]any, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, m.values[k])
	}
	return out
}
