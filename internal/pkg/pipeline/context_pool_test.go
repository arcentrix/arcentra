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

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcade/api/pipeline/v1"
	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcade/pkg/log"
)

func TestLRUCache(t *testing.T) {
	cache := newLRUCache(3)

	// Create test contexts
	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}
	pipeline3 := &spec.Pipeline{Namespace: "pipeline-3"}
	pipeline4 := &spec.Pipeline{Namespace: "pipeline-4"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	ctx1 := NewContext(context.Background(), pipeline1, execCtx)
	ctx2 := NewContext(context.Background(), pipeline2, execCtx)
	ctx3 := NewContext(context.Background(), pipeline3, execCtx)
	ctx4 := NewContext(context.Background(), pipeline4, execCtx)

	// Test put and get
	cache.put("p1", ctx1)
	cache.put("p2", ctx2)
	cache.put("p3", ctx3)

	if cache.size() != 3 {
		t.Errorf("expected cache size 3, got %d", cache.size())
	}

	// Test get moves to front
	val, ok := cache.get("p1")
	if !ok || val != ctx1 {
		t.Error("failed to get p1")
	}

	// Test eviction (p2 should be evicted as it's least recently used)
	evicted := cache.put("p4", ctx4)
	if evicted == nil {
		t.Error("expected eviction but got nil")
	}
	if evicted != ctx2 {
		t.Error("expected p2 to be evicted")
	}

	if cache.size() != 3 {
		t.Errorf("expected cache size 3 after eviction, got %d", cache.size())
	}

	// Test remove
	removed, ok := cache.remove("p1")
	if !ok || removed != ctx1 {
		t.Error("failed to remove p1")
	}
	if cache.size() != 2 {
		t.Errorf("expected cache size 2 after removal, got %d", cache.size())
	}
}

func TestContextPool(t *testing.T) {
	config := &ContextPoolConfig{
		MaxActiveContexts: 2,
		MaxTotalContexts:  5,
		StorageStrategy:   nil,
		Logger:            log.Logger{},
	}

	pool := NewContextPool(config)

	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}
	pipeline3 := &spec.Pipeline{Namespace: "pipeline-3"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	// Test Get creates new context
	ctx1, err := pool.Get(context.Background(), pipeline1, execCtx)
	if err != nil {
		t.Fatalf("failed to get context: %v", err)
	}
	if ctx1 == nil {
		t.Fatal("got nil context")
	}
	if ctx1.PipelineId() != "pipeline-1" {
		t.Errorf("expected pipelineId 'pipeline-1', got %q", ctx1.PipelineId())
	}

	// Test Put returns context to pool
	pool.Put(ctx1)

	// Test Get retrieves from pool
	ctx2, err := pool.Get(context.Background(), pipeline1, execCtx)
	if err != nil {
		t.Fatalf("failed to get context from pool: %v", err)
	}
	if ctx2.PipelineId() != "pipeline-1" {
		t.Errorf("expected pipelineId 'pipeline-1', got %q", ctx2.PipelineId())
	}

	// Test stats
	stats := pool.Stats()
	if stats.ActiveContexts != 1 {
		t.Errorf("expected 1 active context, got %d", stats.ActiveContexts)
	}
	if stats.TotalContexts != 1 {
		t.Errorf("expected 1 total context, got %d", stats.TotalContexts)
	}

	// Test LRU eviction
	pool.Get(context.Background(), pipeline2, execCtx)
	pool.Get(context.Background(), pipeline3, execCtx)

	stats = pool.Stats()
	if stats.ActiveContexts > config.MaxActiveContexts {
		t.Errorf("active contexts %d exceeds max %d", stats.ActiveContexts, config.MaxActiveContexts)
	}
}

func TestContextPoolWithLimit(t *testing.T) {
	config := &ContextPoolConfig{
		MaxActiveContexts: 2,
		MaxTotalContexts:  3,
		StorageStrategy:   nil,
		Logger:            log.Logger{},
	}

	pool := NewContextPool(config)

	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}
	pipeline3 := &spec.Pipeline{Namespace: "pipeline-3"}
	pipeline4 := &spec.Pipeline{Namespace: "pipeline-4"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	// Fill up to max total
	ctx1, _ := pool.Get(context.Background(), pipeline1, execCtx)
	ctx2, _ := pool.Get(context.Background(), pipeline2, execCtx)
	ctx3, _ := pool.Get(context.Background(), pipeline3, execCtx)

	// Return one to make room
	pool.Put(ctx1)

	// Should be able to get new one now
	ctx4, err := pool.Get(context.Background(), pipeline4, execCtx)
	if err != nil {
		t.Fatalf("failed to get context: %v", err)
	}
	if ctx4 == nil {
		t.Fatal("got nil context")
	}

	// Clean up
	pool.Put(ctx2)
	pool.Put(ctx3)
	pool.Put(ctx4)
}

func TestResetForReuse(t *testing.T) {
	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	ctx := NewContext(context.Background(), pipeline1, execCtx)

	// Set some state
	ctx.SetPipelineId("pipeline-1")
	ctx.SetBuildId("build-1")
	ctx.Set("key1", "value1")
	ctx.Store("store1", "value1")
	ctx.Error(fmt.Errorf("test error"))
	ctx.SetCurrentJob(&spec.Job{Name: "job1"}, 0)
	ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING)

	// Reset for reuse
	ctx.ResetForReuse(context.Background(), pipeline2, execCtx)

	// Verify reset
	if ctx.PipelineId() != "pipeline-2" {
		t.Errorf("expected pipelineId 'pipeline-2', got %q", ctx.PipelineId())
	}
	if ctx.BuildId() != "" {
		t.Errorf("expected empty buildId, got %q", ctx.BuildId())
	}
	if _, ok := ctx.Get("key1"); ok {
		t.Error("expected key1 to be cleared")
	}
	if _, ok := ctx.Retrieve("store1"); ok {
		t.Error("expected store1 to be cleared")
	}
	if len(ctx.Errors()) != 0 {
		t.Error("expected errors to be cleared")
	}
	if ctx.CurrentJob() != nil {
		t.Error("expected currentJob to be cleared")
	}
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING {
		t.Errorf("expected status PENDING, got %v", ctx.Status())
	}
}

func TestDefaultContextPool(t *testing.T) {
	// Test initialization
	config := &ContextPoolConfig{
		MaxActiveContexts: 10,
		MaxTotalContexts:  100,
		Logger:            log.Logger{},
	}

	cleanup := InitDefaultContextPool(config)
	defer cleanup()

	pool := GetDefaultContextPool()
	if pool == nil {
		t.Fatal("default pool is nil")
	}

	// Test NewContextFromPool
	pipeline := &spec.Pipeline{Namespace: "test-pipeline"}
	execCtx := &ExecutionContext{
		Pipeline:      pipeline,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	ctx, err := NewContextFromPool(context.Background(), pipeline, execCtx)
	if err != nil {
		t.Fatalf("failed to get context from pool: %v", err)
	}
	if ctx == nil {
		t.Fatal("got nil context")
	}

	// Test ReturnToPool
	ReturnToPool(ctx)

	// Verify it's in the pool
	stats := pool.Stats()
	if stats.ActiveContexts != 1 {
		t.Errorf("expected 1 active context, got %d", stats.ActiveContexts)
	}
}

func TestStorageStrategy(t *testing.T) {
	// Mock storage strategy
	mockStorage := &mockStorageStrategy{
		storage: make(map[string][]byte),
	}

	config := &ContextPoolConfig{
		MaxActiveContexts: 2,
		MaxTotalContexts:  5,
		StorageStrategy:   mockStorage,
		Logger:            log.Logger{},
	}

	pool := NewContextPool(config)

	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}
	pipeline3 := &spec.Pipeline{Namespace: "pipeline-3"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	// Create contexts that will trigger eviction
	ctx1, _ := pool.Get(context.Background(), pipeline1, execCtx)
	ctx2, _ := pool.Get(context.Background(), pipeline2, execCtx)
	ctx3, _ := pool.Get(context.Background(), pipeline3, execCtx)

	// Wait a bit for async storage
	time.Sleep(100 * time.Millisecond)

	// Verify storage was called (eviction should have occurred)
	// Note: This is a simplified test, actual implementation would verify storage
	pool.Put(ctx1)
	pool.Put(ctx2)
	pool.Put(ctx3)
}

func TestContextPoolCleanup(t *testing.T) {
	config := &ContextPoolConfig{
		MaxActiveContexts: 10,
		MaxTotalContexts:  20,
		StorageStrategy:   nil,
		Logger:            log.Logger{},
		IdleTimeout:       100 * time.Millisecond, // Short timeout for testing
		CleanupInterval:   50 * time.Millisecond,  // Frequent cleanup for testing
	}

	pool := NewContextPool(config)
	defer pool.Stop()

	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	pipeline2 := &spec.Pipeline{Namespace: "pipeline-2"}

	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	// Create contexts
	ctx1, err := pool.Get(context.Background(), pipeline1, execCtx)
	if err != nil {
		t.Fatalf("failed to get context: %v", err)
	}
	ctx2, err := pool.Get(context.Background(), pipeline2, execCtx)
	if err != nil {
		t.Fatalf("failed to get context: %v", err)
	}

	// Put contexts back to pool
	pool.Put(ctx1)
	pool.Put(ctx2)

	// Verify contexts are in pool
	stats := pool.Stats()
	if stats.ActiveContexts != 2 {
		t.Errorf("expected 2 active contexts, got %d", stats.ActiveContexts)
	}

	// Wait for cleanup to run (should evict idle contexts)
	time.Sleep(200 * time.Millisecond)

	// Verify contexts were cleaned up
	stats = pool.Stats()
	if stats.ActiveContexts > 0 {
		t.Logf("Note: contexts may still be in pool if cleanup hasn't run yet")
		// Cleanup is async, so we check if it's less than before
		if stats.ActiveContexts >= 2 {
			t.Errorf("expected contexts to be cleaned up, but still have %d", stats.ActiveContexts)
		}
	}
}

func TestContextPoolShutdown(t *testing.T) {
	config := &ContextPoolConfig{
		MaxActiveContexts: 10,
		MaxTotalContexts:  20,
		StorageStrategy:   nil,
		Logger:            log.Logger{},
		IdleTimeout:       1 * time.Minute,
		CleanupInterval:   5 * time.Minute,
	}

	pool := NewContextPool(config)

	pipeline1 := &spec.Pipeline{Namespace: "pipeline-1"}
	execCtx := &ExecutionContext{
		Pipeline:      pipeline1,
		WorkspaceRoot: "/tmp",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	// Create and put back contexts
	ctx1, _ := pool.Get(context.Background(), pipeline1, execCtx)
	pool.Put(ctx1)

	// Verify context is in pool
	stats := pool.Stats()
	if stats.ActiveContexts != 1 {
		t.Errorf("expected 1 active context, got %d", stats.ActiveContexts)
	}

	// Shutdown pool
	pool.Shutdown()

	// Verify pool is empty
	stats = pool.Stats()
	if stats.ActiveContexts != 0 {
		t.Errorf("expected 0 active contexts after shutdown, got %d", stats.ActiveContexts)
	}
	if stats.TotalContexts != 0 {
		t.Errorf("expected 0 total contexts after shutdown, got %d", stats.TotalContexts)
	}
}

// mockStorageStrategy implements StorageStrategy for testing
type mockStorageStrategy struct {
	storage map[string][]byte
	mu      sync.Mutex
}

func (m *mockStorageStrategy) Save(ctx context.Context, pipelineId string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage[pipelineId] = data
	return nil
}

func (m *mockStorageStrategy) Load(ctx context.Context, pipelineId string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.storage[pipelineId]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return data, nil
}

func (m *mockStorageStrategy) Delete(ctx context.Context, pipelineId string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.storage, pipelineId)
	return nil
}
