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

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/statemachine"
	"golang.org/x/sync/errgroup"
)

var (
	// defaultContextPool is the global context pool instance
	defaultContextPool *ContextPool
	defaultPoolOnce    sync.Once
)

// StorageStrategy defines the interface for cold data storage strategy
// This allows implementing different storage backends (e.g., Redis, database, file system)
// for cold context data that is evicted from the LRU cache
type StorageStrategy interface {
	// Save stores a context to cold storage
	Save(ctx context.Context, pipelineId string, data []byte) error
	// Load loads a context from cold storage
	Load(ctx context.Context, pipelineId string) ([]byte, error)
	// Delete removes a context from cold storage
	Delete(ctx context.Context, pipelineId string) error
}

// ContextPoolConfig configures the context pool behavior
type ContextPoolConfig struct {
	// MaxActiveContexts maximum number of active contexts in LRU cache
	MaxActiveContexts int
	// MaxTotalContexts maximum total number of contexts (active + cold)
	// When exceeded, new context creation will be blocked
	MaxTotalContexts int
	// StorageStrategy strategy for storing cold contexts
	// If nil, cold contexts will be discarded
	StorageStrategy StorageStrategy
	// Logger logger for pool operations
	Logger log.Logger
	// IdleTimeout duration after which an unused context will be evicted
	// If zero, contexts will not be evicted based on idle time
	IdleTimeout time.Duration
	// CleanupInterval interval between cleanup runs
	// If zero, defaults to 5 minutes
	CleanupInterval time.Duration
}

// defaultContextPoolConfig returns default configuration
func defaultContextPoolConfig() *ContextPoolConfig {
	return &ContextPoolConfig{
		MaxActiveContexts: 1000,
		MaxTotalContexts:  10000,
		StorageStrategy:   nil,
		Logger:            log.Logger{},
		IdleTimeout:       30 * time.Minute, // Default: evict contexts idle for 30 minutes
		CleanupInterval:   5 * time.Minute,  // Default: cleanup every 5 minutes
	}
}

// lruNode represents a node in the LRU cache doubly linked list
// Memory layout optimized: pointers grouped together
type lruNode struct {
	// Pointers (8 bytes each) - grouped for better cache locality
	prev  *lruNode
	next  *lruNode
	value *Context

	// String (16 bytes)
	key string

	// Time struct (24 bytes)
	lastAccess time.Time
}

// lruCache implements a thread-safe LRU cache for active contexts
type lruCache struct {
	mu       sync.RWMutex
	capacity int
	count    int
	head     *lruNode
	tail     *lruNode
	nodes    map[string]*lruNode
}

// // newLRUCache creates a new LRU cache with the specified capacity
func newLRUCache(capacity int) *lruCache {
	if capacity <= 0 {
		capacity = 1000
	}
	head := &lruNode{}
	tail := &lruNode{}
	head.next = tail
	tail.prev = head
	return &lruCache{
		capacity: capacity,
		count:    0,
		head:     head,
		tail:     tail,
		nodes:    make(map[string]*lruNode),
	}
}

// get retrieves a context from the cache and moves it to the front
func (l *lruCache) get(key string) (*Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	node, ok := l.nodes[key]
	if !ok {
		return nil, false
	}

	// Move to front
	l.moveToFront(node)
	node.lastAccess = time.Now()
	return node.value, true
}

// put adds or updates a context in the cache
// Returns the evicted context if cache is full, or nil if no eviction occurred
func (l *lruCache) put(key string, value *Context) *Context {
	l.mu.Lock()
	defer l.mu.Unlock()

	// If key exists, update and move to front
	if node, ok := l.nodes[key]; ok {
		node.value = value
		node.lastAccess = time.Now()
		l.moveToFront(node)
		return nil
	}

	// Create new node
	node := &lruNode{
		key:        key,
		value:      value,
		lastAccess: time.Now(),
	}

	// Add to front
	l.addToFront(node)
	l.nodes[key] = node
	l.count++

	// Evict if capacity exceeded
	if l.count > l.capacity {
		return l.evictLRU()
	}

	return nil
}

// remove removes a context from the cache
func (l *lruCache) remove(key string) (*Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	node, ok := l.nodes[key]
	if !ok {
		return nil, false
	}

	l.removeNode(node)
	delete(l.nodes, key)
	l.count--
	return node.value, true
}

// size returns the current cache size
func (l *lruCache) size() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.count
}

// moveToFront moves a node to the front of the list
func (l *lruCache) moveToFront(node *lruNode) {
	l.removeNode(node)
	l.addToFront(node)
}

// addToFront adds a node to the front of the list
func (l *lruCache) addToFront(node *lruNode) {
	node.prev = l.head
	node.next = l.head.next
	l.head.next.prev = node
	l.head.next = node
}

// removeNode removes a node from the list
func (l *lruCache) removeNode(node *lruNode) {
	node.prev.next = node.next
	node.next.prev = node.prev
}

// evictLRU evicts the least recently used node and returns its value
func (l *lruCache) evictLRU() *Context {
	if l.count == 0 {
		return nil
	}

	node := l.tail.prev
	if node == l.head {
		return nil
	}

	l.removeNode(node)
	delete(l.nodes, node.key)
	l.count--
	return node.value
}

// ContextPool manages a pool of pipeline contexts with LRU caching
// and support for cold data storage
type ContextPool struct {
	mu     sync.RWMutex
	config *ContextPoolConfig
	lru    *lruCache
	// sync.Pool for fast context allocation/deallocation
	contextPool     sync.Pool
	activeCount     int
	totalCount      int
	activeCountCond *sync.Cond
	totalCountCond  *sync.Cond
	// Cleanup goroutine management
	cleanupCtx    context.Context
	cleanupCancel context.CancelFunc
	cleanupEg     *errgroup.Group
	cleanupOnce   sync.Once
}

// NewContextPool creates a new context pool with the given configuration
func NewContextPool(config *ContextPoolConfig) *ContextPool {
	if config == nil {
		config = defaultContextPoolConfig()
	}
	if config.MaxActiveContexts <= 0 {
		config.MaxActiveContexts = 1000
	}
	if config.MaxTotalContexts <= 0 {
		config.MaxTotalContexts = 10000
	}

	pool := &ContextPool{
		config:          config,
		lru:             newLRUCache(config.MaxActiveContexts),
		activeCount:     0,
		totalCount:      0,
		activeCountCond: sync.NewCond(&sync.Mutex{}),
		totalCountCond:  sync.NewCond(&sync.Mutex{}),
	}

	// Initialize sync.Pool for context reuse
	pool.contextPool = sync.Pool{
		New: func() any {
			return &Context{
				keys:     make(map[string]any, 8), // Pre-allocate with small capacity
				store:    make(map[string]any, 8),
				errors:   make([]error, 0, 4),
				handlers: make([]HandlerFunc, 0, 4),
			}
		},
	}

	// Initialize cleanup context
	pool.cleanupCtx, pool.cleanupCancel = context.WithCancel(context.Background())

	// Start cleanup goroutine if IdleTimeout is configured
	if config.IdleTimeout > 0 {
		pool.startCleanup()
	}

	return pool
}

// Get retrieves a context from the pool or creates a new one
// If the pool is at capacity, it will wait until a context is available
func (p *ContextPool) Get(ctx context.Context, pipeline *spec.Pipeline, execCtx *ExecutionContext) (*Context, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("pipeline cannot be nil")
	}

	pipelineId := pipeline.Namespace
	if pipelineId == "" {
		return nil, fmt.Errorf("pipeline namespace cannot be empty")
	}

	// Try to get from LRU cache first
	if pc, ok := p.lru.get(pipelineId); ok {
		// Reset context for reuse
		pc.ResetForReuse(ctx, pipeline, execCtx)
		return pc, nil
	}

	// Check total count limit
	p.mu.Lock()
	if p.totalCount >= p.config.MaxTotalContexts {
		p.mu.Unlock()
		// Wait for available slot
		p.totalCountCond.L.Lock()
		for p.totalCount >= p.config.MaxTotalContexts {
			select {
			case <-ctx.Done():
				p.totalCountCond.L.Unlock()
				return nil, ctx.Err()
			default:
				p.totalCountCond.Wait()
			}
		}
		p.totalCountCond.L.Unlock()
		p.mu.Lock()
	}

	// Create new context
	pc := p.createNewContext(ctx, pipeline, execCtx)
	p.totalCount++
	p.mu.Unlock()

	// Try to add to LRU cache
	evicted := p.lru.put(pipelineId, pc)
	if evicted != nil {
		// Handle evicted context (store to cold storage if strategy is set)
		p.handleEvictedContext(ctx, evicted)
	}

	return pc, nil
}

// Put returns a context to the pool
// The context will be cached in LRU if there's space, otherwise it may be stored to cold storage
func (p *ContextPool) Put(pc *Context) {
	if pc == nil {
		return
	}

	pipelineId := pc.PipelineId()
	if pipelineId == "" {
		// Return to sync.Pool if no pipeline ID
		p.returnToSyncPool(pc)
		return
	}

	// Try to add to LRU cache
	evicted := p.lru.put(pipelineId, pc)
	if evicted != nil && evicted != pc {
		// Handle evicted context
		p.handleEvictedContext(context.Background(), evicted)
	}

	// Notify waiting goroutines
	p.totalCountCond.Broadcast()
}

// Remove removes a context from the pool
func (p *ContextPool) Remove(pipelineId string) {
	if pipelineId == "" {
		return
	}

	// Remove from LRU cache
	if ctx, ok := p.lru.remove(pipelineId); ok {
		p.returnToSyncPool(ctx)
		p.mu.Lock()
		p.totalCount--
		p.mu.Unlock()
		p.totalCountCond.Broadcast()
	}

	// Remove from cold storage if strategy is set
	if p.config.StorageStrategy != nil {
		_ = p.config.StorageStrategy.Delete(context.Background(), pipelineId)
	}
}

// Stats returns pool statistics
func (p *ContextPool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PoolStats{
		ActiveContexts: p.lru.size(),
		TotalContexts:  p.totalCount,
		MaxActive:      p.config.MaxActiveContexts,
		MaxTotal:       p.config.MaxTotalContexts,
	}
}

// startCleanup starts the background cleanup goroutine
func (p *ContextPool) startCleanup() {
	p.cleanupOnce.Do(func() {
		cleanupInterval := p.config.CleanupInterval
		if cleanupInterval <= 0 {
			cleanupInterval = 5 * time.Minute
		}

		p.cleanupEg.Go(func() error {
			return p.cleanupLoop(cleanupInterval)
		})
	})
}

// cleanupLoop periodically cleans up idle contexts
func (p *ContextPool) cleanupLoop(interval time.Duration) error {

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.cleanupCtx.Done():
			return nil
		case <-ticker.C:
			p.cleanupIdleContexts()
		}
	}
}

// cleanupIdleContexts removes contexts that have been idle for too long
func (p *ContextPool) cleanupIdleContexts() {
	if p.config.IdleTimeout <= 0 {
		return
	}

	now := time.Now()
	expiredItems := make([]struct {
		key      string
		ctx      *Context
		idleTime time.Duration
	}, 0)

	// Collect expired contexts with their idle times
	p.lru.mu.RLock()
	for key, node := range p.lru.nodes {
		idleTime := now.Sub(node.lastAccess)
		if idleTime > p.config.IdleTimeout {
			expiredItems = append(expiredItems, struct {
				key      string
				ctx      *Context
				idleTime time.Duration
			}{key: key, ctx: node.value, idleTime: idleTime})
		}
	}
	p.lru.mu.RUnlock()

	// Remove expired contexts
	for _, item := range expiredItems {
		if ctx, ok := p.lru.remove(item.key); ok {
			p.config.Logger.Infow("Evicting idle context", "pipelineId", item.key, "idleTime", item.idleTime)
			p.handleEvictedContext(context.Background(), ctx)
		}
	}

	if len(expiredItems) > 0 {
		p.config.Logger.Infow("Cleaned up idle contexts", "pipelineId", len(expiredItems))
	}
}

// Stop stops the cleanup goroutine and releases resources
func (p *ContextPool) Stop() {
	p.cleanupCancel()
	if err := p.cleanupEg.Wait(); err != nil {
		p.config.Logger.Errorw("Failed to cleanup context pool", "error", err)
	}
}

// Shutdown gracefully shuts down the pool, cleaning up all contexts
func (p *ContextPool) Shutdown() {
	// Stop cleanup goroutine
	p.Stop()

	// Clear all contexts from LRU cache
	p.lru.mu.Lock()
	keys := make([]string, 0, len(p.lru.nodes))
	for key := range p.lru.nodes {
		keys = append(keys, key)
	}
	p.lru.mu.Unlock()

	for _, key := range keys {
		if ctx, ok := p.lru.remove(key); ok {
			p.returnToSyncPool(ctx)
		}
	}

	// Reset counts
	p.mu.Lock()
	p.totalCount = 0
	p.activeCount = 0
	p.mu.Unlock()

	p.config.Logger.Info("Context pool shutdown complete")
}

// PoolStats contains pool statistics
type PoolStats struct {
	ActiveContexts int
	TotalContexts  int
	MaxActive      int
	MaxTotal       int
}

// createNewContext creates a new pipeline context
// Uses sync.Pool to reduce GC pressure
func (p *ContextPool) createNewContext(ctx context.Context, pipeline *spec.Pipeline, execCtx *ExecutionContext) *Context {
	if ctx == nil {
		ctx = context.Background()
	}

	// Try to get from sync.Pool first
	pc, _ := p.contextPool.Get().(*Context)
	if pc == nil {
		// Fallback: create new context if pool returns nil
		pc = &Context{
			keys:     make(map[string]any, 8),
			store:    make(map[string]any, 8),
			errors:   make([]error, 0, 4),
			handlers: make([]HandlerFunc, 0, 4),
		}
	}

	// Reset context fields (reuse from pool)
	pc.ctx = ctx
	pc.pipeline = pipeline
	pc.execCtx = execCtx
	pc.index = -1
	pc.startTime = time.Now()
	pc.pipelineId = pipeline.Namespace
	pc.buildId = ""
	pc.projectId = ""
	pc.orgId = ""
	pc.triggeredBy = ""
	pc.currentJob = nil
	pc.currentStep = nil
	pc.jobIndex = 0
	pc.stepIndex = 0
	pc.aborted = false
	pc.abortError = nil
	pc.endTime = nil
	pc.cancel = nil

	// Clear maps and slices (reuse capacity)
	for k := range pc.keys {
		delete(pc.keys, k)
	}
	for k := range pc.store {
		delete(pc.store, k)
	}
	pc.errors = pc.errors[:0]
	pc.handlers = pc.handlers[:0]

	// Create or reset state machine
	if pc.stateMachine == nil {
		sm := statemachine.NewWithState(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
		sm.Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS, pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED, pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED)
		pc.stateMachine = sm
		p.registerStateMachineHooks(pc, sm)
	} else {
		pc.stateMachine.SetCurrent(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
	}

	return pc
}

// registerStateMachineHooks registers hooks for terminal states
func (p *ContextPool) registerStateMachineHooks(pc *Context, sm *statemachine.StateMachine[pipelinev1.PipelineStatus]) {
	sm.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
	sm.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
	sm.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
}

// handleEvictedContext handles evicted context by storing to cold storage if strategy is set
func (p *ContextPool) handleEvictedContext(ctx context.Context, pc *Context) {
	if p.config.StorageStrategy == nil {
		// No storage strategy, return to sync.Pool and decrement count
		p.returnToSyncPool(pc)
		p.mu.Lock()
		p.totalCount--
		p.mu.Unlock()
		p.totalCountCond.Broadcast()
		return
	}

	// Serialize and store to cold storage
	pipelineId := pc.PipelineId()
	if pipelineId == "" {
		p.returnToSyncPool(pc)
		return
	}

	// Note: In a real implementation, you would serialize this properly (e.g., JSON, protobuf)
	// For now, we'll just log and store metadata
	p.config.Logger.Infow("Evicting context to cold storage", "pipelineId", pipelineId)

	// Store to cold storage (async to avoid blocking)
	safe.Go(func() {
		// In a real implementation, serialize pc properly
		// For now, we'll just mark it as stored
		_ = p.config.StorageStrategy.Save(ctx, pipelineId, nil)
		// After storing, return to sync.Pool
		p.returnToSyncPool(pc)
	})

	p.mu.Lock()
	p.totalCount--
	p.mu.Unlock()
	p.totalCountCond.Broadcast()
}

// returnToSyncPool returns a context to sync.Pool for reuse
func (p *ContextPool) returnToSyncPool(pc *Context) {
	if pc == nil {
		return
	}
	// Clear references to avoid memory leaks
	pc.ctx = nil
	pc.cancel = nil
	pc.pipeline = nil
	pc.execCtx = nil
	pc.stateMachine = nil
	pc.currentJob = nil
	pc.currentStep = nil
	pc.endTime = nil
	pc.abortError = nil
	p.contextPool.Put(pc)
}

// ResetForReuse resets a context for reuse (called by Context)
func (pc *Context) ResetForReuse(ctx context.Context, pipeline *spec.Pipeline, execCtx *ExecutionContext) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	// Reset base context
	if ctx != nil {
		pc.ctx = ctx
	}

	// Reset pipeline and exec context
	pc.pipeline = pipeline
	pc.execCtx = execCtx

	// Reset execution state
	pc.currentJob = nil
	pc.currentStep = nil
	pc.jobIndex = 0
	pc.stepIndex = 0

	// Reset key-value storage
	pc.keys = make(map[string]any)
	pc.store = make(map[string]any)

	// Reset error handling
	pc.errors = make([]error, 0)
	pc.aborted = false
	pc.abortError = nil

	// Reset middleware
	pc.handlers = make([]HandlerFunc, 0)
	pc.index = -1

	// Reset metadata
	pc.startTime = time.Now()
	pc.endTime = nil

	// Reset pipeline ID and execution information
	if pipeline != nil {
		pc.pipelineId = pipeline.Namespace
	}
	pc.buildId = ""
	pc.projectId = ""
	pc.orgId = ""
	pc.triggeredBy = ""

	// Reset state machine
	if pc.stateMachine != nil {
		pc.stateMachine.SetCurrent(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
	} else {
		sm := statemachine.NewWithState(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
		sm.Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS, pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED, pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING).
			Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED, pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING, pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED)
		pc.stateMachine = sm
	}

	// Re-register hooks
	pc.stateMachine.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
	pc.stateMachine.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
	pc.stateMachine.OnEnter(pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED, func(state pipelinev1.PipelineStatus) error {
		pc.mu.Lock()
		if pc.endTime == nil {
			now := time.Now()
			pc.endTime = &now
		}
		pc.mu.Unlock()
		return nil
	})
}

// InitDefaultContextPool initializes the default global context pool
// This should be called during application startup
// Returns a cleanup function that should be called during application shutdown
func InitDefaultContextPool(config *ContextPoolConfig) func() {
	var cleanup func()
	defaultPoolOnce.Do(func() {
		if config == nil {
			config = defaultContextPoolConfig()
		}
		defaultContextPool = NewContextPool(config)
		cleanup = func() {
			if defaultContextPool != nil {
				defaultContextPool.Shutdown()
			}
		}
	})
	return cleanup
}

// GetDefaultContextPool returns the default global context pool
// Returns nil if not initialized
func GetDefaultContextPool() *ContextPool {
	return defaultContextPool
}

// NewContextFromPool creates a new pipeline context from the pool if available,
// otherwise creates a new one directly
// This function maintains backward compatibility with NewContext
func NewContextFromPool(ctx context.Context, pipeline *spec.Pipeline, execCtx *ExecutionContext) (*Context, error) {
	if defaultContextPool != nil {
		return defaultContextPool.Get(ctx, pipeline, execCtx)
	}
	// Fallback to direct creation if pool is not initialized
	return NewContext(ctx, pipeline, execCtx), nil
}

// ReturnToPool returns a context to the default pool
// This is a convenience function that calls Put on the default pool
func ReturnToPool(pc *Context) {
	if defaultContextPool != nil && pc != nil {
		defaultContextPool.Put(pc)
	}
}
