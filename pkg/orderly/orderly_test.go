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

import (
	"reflect"
	"sync"
	"testing"

	"github.com/arcentrix/arcentra/pkg/safe"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		maxSize int
		want    *Map
	}{
		{
			name:    "create Map with maxSize 10",
			maxSize: 10,
			want: &Map{
				keys:    make([]string, 0, 10),
				values:  make(map[string]any),
				maxSize: 10,
			},
		},
		{
			name:    "create Map with maxSize 0",
			maxSize: 0,
			want: &Map{
				keys:    make([]string, 0),
				values:  make(map[string]any),
				maxSize: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.maxSize)
			if got.maxSize != tt.want.maxSize {
				t.Errorf("New() maxSize = %v, want %v", got.maxSize, tt.want.maxSize)
			}
			if len(got.keys) != len(tt.want.keys) {
				t.Errorf("New() keys length = %v, want %v", len(got.keys), len(tt.want.keys))
			}
			if len(got.values) != len(tt.want.values) {
				t.Errorf("New() values length = %v, want %v", len(got.values), len(tt.want.values))
			}
		})
	}
}

func TestMap_Set(t *testing.T) {
	t.Run("add new key-value pairs", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", 42)

		if len(m.keys) != 2 {
			t.Errorf("Set() keys length = %v, want 2", len(m.keys))
		}

		val, ok := m.values["key1"]
		if !ok || val != "value1" {
			t.Errorf("Set() values[\"key1\"] = %v, want \"value1\"", val)
		}

		val, ok = m.values["key2"]
		if !ok || val != 42 {
			t.Errorf("Set() values[\"key2\"] = %v, want 42", val)
		}
	})

	t.Run("update existing key", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key1", "value2")

		if len(m.keys) != 1 {
			t.Errorf("Set() keys length = %v, want 1", len(m.keys))
		}

		val, ok := m.values["key1"]
		if !ok || val != "value2" {
			t.Errorf("Set() values[\"key1\"] = %v, want \"value2\"", val)
		}
	})

	t.Run("reach maxSize limit", func(t *testing.T) {
		m := New(2)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3") // should be ignored

		if len(m.keys) != 2 {
			t.Errorf("Set() keys length = %v, want 2", len(m.keys))
		}

		if _, ok := m.values["key3"]; ok {
			t.Error("Set() key3 should not be added when maxSize is reached")
		}
	})

	t.Run("maintain insertion order", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		expectedKeys := []string{"key1", "key2", "key3"}
		if !reflect.DeepEqual(m.keys, expectedKeys) {
			t.Errorf("Set() keys = %v, want %v", m.keys, expectedKeys)
		}
	})
}

func TestMap_Get(t *testing.T) {
	t.Run("get existing key", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", 42)

		val, ok := m.Get("key1")
		if !ok {
			t.Error("Get() ok = false, want true")
		}
		if val != "value1" {
			t.Errorf("Get() value = %v, want \"value1\"", val)
		}

		val, ok = m.Get("key2")
		if !ok {
			t.Error("Get() ok = false, want true")
		}
		if val != 42 {
			t.Errorf("Get() value = %v, want 42", val)
		}
	})

	t.Run("get non-existent key", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")

		val, ok := m.Get("nonexistent")
		if ok {
			t.Error("Get() ok = true, want false")
		}
		if val != nil {
			t.Errorf("Get() value = %v, want nil", val)
		}
	})

	t.Run("get key from empty Map", func(t *testing.T) {
		m := New(10)

		val, ok := m.Get("anykey")
		if ok {
			t.Error("Get() ok = true, want false")
		}
		if val != nil {
			t.Errorf("Get() value = %v, want nil", val)
		}
	})
}

func TestMap_Keys(t *testing.T) {
	t.Run("get all keys", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		keys := m.Keys()
		expectedKeys := []string{"key1", "key2", "key3"}

		if !reflect.DeepEqual(keys, expectedKeys) {
			t.Errorf("Keys() = %v, want %v", keys, expectedKeys)
		}
	})

	t.Run("get keys from empty Map", func(t *testing.T) {
		m := New(10)
		keys := m.Keys()

		if len(keys) != 0 {
			t.Errorf("Keys() length = %v, want 0", len(keys))
		}
	})

	t.Run("returned slice is a copy", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")

		keys1 := m.Keys()
		keys2 := m.Keys()

		if &keys1[0] == &keys2[0] {
			t.Error("Keys() returned the same slice reference, want different")
		}
	})
}

func TestMap_ForEach(t *testing.T) {
	t.Run("iterate over all key-value pairs", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		visited := make(map[string]any)
		m.ForEach(func(k string, v any) {
			visited[k] = v
		})

		if len(visited) != 3 {
			t.Errorf("ForEach() visited %d keys, want 3", len(visited))
		}

		if visited["key1"] != "value1" {
			t.Errorf("ForEach() visited[\"key1\"] = %v, want \"value1\"", visited["key1"])
		}
		if visited["key2"] != "value2" {
			t.Errorf("ForEach() visited[\"key2\"] = %v, want \"value2\"", visited["key2"])
		}
		if visited["key3"] != "value3" {
			t.Errorf("ForEach() visited[\"key3\"] = %v, want \"value3\"", visited["key3"])
		}
	})

	t.Run("iterate in insertion order", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		order := make([]string, 0)
		m.ForEach(func(k string, v any) {
			order = append(order, k)
		})

		expectedOrder := []string{"key1", "key2", "key3"}
		if !reflect.DeepEqual(order, expectedOrder) {
			t.Errorf("ForEach() order = %v, want %v", order, expectedOrder)
		}
	})

	t.Run("iterate over empty Map", func(t *testing.T) {
		m := New(10)
		count := 0
		m.ForEach(func(k string, v any) {
			count++
		})

		if count != 0 {
			t.Errorf("ForEach() count = %v, want 0", count)
		}
	})
}

func TestMap_ToSlice(t *testing.T) {
	t.Run("convert to slice", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		slice := m.ToSlice()
		expectedSlice := []any{"value1", "value2", "value3"}

		if !reflect.DeepEqual(slice, expectedSlice) {
			t.Errorf("ToSlice() = %v, want %v", slice, expectedSlice)
		}
	})

	t.Run("convert in insertion order", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", "value2")
		m.Set("key3", "value3")

		slice := m.ToSlice()
		if len(slice) != 3 {
			t.Errorf("ToSlice() length = %v, want 3", len(slice))
		}

		if slice[0] != "value1" {
			t.Errorf("ToSlice()[0] = %v, want \"value1\"", slice[0])
		}
		if slice[1] != "value2" {
			t.Errorf("ToSlice()[1] = %v, want \"value2\"", slice[1])
		}
		if slice[2] != "value3" {
			t.Errorf("ToSlice()[2] = %v, want \"value3\"", slice[2])
		}
	})

	t.Run("convert empty Map to slice", func(t *testing.T) {
		m := New(10)
		slice := m.ToSlice()

		if len(slice) != 0 {
			t.Errorf("ToSlice() length = %v, want 0", len(slice))
		}
	})

	t.Run("contain values of different types", func(t *testing.T) {
		m := New(10)
		m.Set("key1", "value1")
		m.Set("key2", 42)
		m.Set("key3", true)

		slice := m.ToSlice()
		if len(slice) != 3 {
			t.Errorf("ToSlice() length = %v, want 3", len(slice))
		}

		if slice[0] != "value1" {
			t.Errorf("ToSlice()[0] = %v, want \"value1\"", slice[0])
		}
		if slice[1] != 42 {
			t.Errorf("ToSlice()[1] = %v, want 42", slice[1])
		}
		if slice[2] != true {
			t.Errorf("ToSlice()[2] = %v, want true", slice[2])
		}
	})
}

func TestMap_Concurrent(t *testing.T) {
	t.Run("concurrent Set and Get", func(t *testing.T) {
		m := New(100)
		var wg sync.WaitGroup
		workers := 10
		opsPerWorker := 100

		// concurrent writes
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			id := i
			safe.Go(func() {
				defer wg.Done()
				for j := 0; j < opsPerWorker; j++ {
					key := string(rune('a'+id)) + string(rune('0'+j))
					m.Set(key, id*opsPerWorker+j)
				}
			})
		}
		wg.Wait()

		// concurrent reads
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			id := i
			safe.Go(func() {
				defer wg.Done()
				for j := 0; j < opsPerWorker; j++ {
					key := string(rune('a'+id)) + string(rune('0'+j))
					_, _ = m.Get(key)
				}
			})
		}
		wg.Wait()

		// verify data consistency
		keys := m.Keys()
		if len(keys) > m.maxSize {
			t.Errorf("Concurrent Set() exceeded maxSize: got %d, want <= %d", len(keys), m.maxSize)
		}
	})

	t.Run("concurrent ForEach", func(t *testing.T) {
		m := New(50)
		for i := 0; i < 50; i++ {
			m.Set(string(rune('a'+i)), i)
		}

		var wg sync.WaitGroup
		workers := 10
		wg.Add(workers)

		for i := 0; i < workers; i++ {
			safe.Go(func() {
				defer wg.Done()
				count := 0
				m.ForEach(func(k string, v any) {
					count++
				})
				if count != 50 {
					t.Errorf("Concurrent ForEach() count = %d, want 50", count)
				}
			})
		}
		wg.Wait()
	})

	t.Run("concurrent Keys and ToSlice", func(t *testing.T) {
		m := New(50)
		for i := 0; i < 50; i++ {
			m.Set(string(rune('a'+i)), i)
		}

		var wg sync.WaitGroup
		workers := 10
		wg.Add(workers * 2)

		for i := 0; i < workers; i++ {
			safe.Go(func() {
				defer wg.Done()
				keys := m.Keys()
				if len(keys) != 50 {
					t.Errorf("Concurrent Keys() length = %d, want 50", len(keys))
				}
			})

			safe.Go(func() {
				defer wg.Done()
				slice := m.ToSlice()
				if len(slice) != 50 {
					t.Errorf("Concurrent ToSlice() length = %d, want 50", len(slice))
				}
			})
		}
		wg.Wait()
	})
}
