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
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/builtin"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/statemachine"
)

// Context represents the context for pipeline execution throughout its lifecycle.
// It provides a unified interface for accessing pipeline information, storing key-value pairs,
// handling errors, and managing execution state, similar to gin.Context.
//
// Memory layout optimized for cache efficiency:
// - Pointers (8 bytes) grouped together
// - Small fields (bool, int) grouped together
// - Large structs (sync.RWMutex, time.Time) placed appropriately
type Context struct {
	// Pointers (8 bytes each) - grouped for better cache locality
	ctx          context.Context
	cancel       context.CancelFunc
	pipeline     *spec.Pipeline
	execCtx      *ExecutionContext
	stateMachine *statemachine.StateMachine[pipelinev1.PipelineStatus]
	currentJob   *spec.Job
	currentStep  *spec.Step
	abortError   error

	// String fields (16 bytes each) - grouped together
	pipelineId  string
	buildId     string
	projectId   string
	orgId       string
	triggeredBy string

	// Maps and slices (24 bytes for slice header, 8 bytes for map pointer)
	keys     map[string]any
	store    map[string]any
	errors   []error
	handlers []HandlerFunc

	// Mutex (32 bytes) - placed after pointers
	mu sync.RWMutex

	// Integer fields (8 bytes each)
	jobIndex  int
	stepIndex int
	index     int

	// Boolean field (1 byte) - grouped with small fields
	aborted bool
	// padding: 7 bytes (to align next field to 8 bytes)

	// Time struct (24 bytes)
	startTime time.Time
	// Time Pointers (8 bytes each) - grouped for better cache locality
	endTime *time.Time
}

// HandlerFunc defines the handler function signature for middleware
type HandlerFunc func(*Context)

// NewContext NewPipeline creates a new PipelineContext with the given context and pipeline.
func NewContext(ctx context.Context, pipeline *spec.Pipeline, execCtx *ExecutionContext) *Context {
	if ctx == nil {
		ctx = context.Background()
	}

	// Create state machine with initial state PENDING
	sm := statemachine.NewWithState(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
	// Define state transition rules
	sm.Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING,
		pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
		pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING) // Support retry

	pc := &Context{
		ctx:          ctx,
		pipeline:     pipeline,
		execCtx:      execCtx,
		keys:         make(map[string]any),
		store:        make(map[string]any),
		errors:       make([]error, 0),
		handlers:     make([]HandlerFunc, 0),
		index:        -1,
		startTime:    time.Now(),
		pipelineId:   pipeline.Namespace,
		stateMachine: sm,
	}

	// Register hooks for terminal states to set endTime
	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS,
		func(state pipelinev1.PipelineStatus) error {
			pc.mu.Lock()
			if pc.endTime == nil {
				now := time.Now()
				pc.endTime = &now
			}
			pc.mu.Unlock()
			return nil
		})

	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
		func(state pipelinev1.PipelineStatus) error {
			pc.mu.Lock()
			if pc.endTime == nil {
				now := time.Now()
				pc.endTime = &now
			}
			pc.mu.Unlock()
			return nil
		})

	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED,
		func(state pipelinev1.PipelineStatus) error {
			pc.mu.Lock()
			if pc.endTime == nil {
				now := time.Now()
				pc.endTime = &now
			}
			pc.mu.Unlock()
			return nil
		})
	return pc
}

// Context returns the underlying context.Context
func (c *Context) Context() context.Context {
	return c.ctx
}

// WithContext creates a new PipelineContext with the given context
func (c *Context) WithContext(ctx context.Context) *Context {
	// Create new state machine with current state
	currentState := c.Status()
	sm := statemachine.NewWithState(currentState)
	// Define state transition rules
	sm.Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING,
		pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
		pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED).
		Allow(pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
			pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING)

	newCtx := &Context{
		ctx:          ctx,
		pipeline:     c.pipeline,
		execCtx:      c.execCtx,
		keys:         make(map[string]any),
		store:        make(map[string]any),
		errors:       make([]error, 0),
		handlers:     c.handlers,
		index:        -1,
		startTime:    c.startTime,
		endTime:      c.endTime,
		pipelineId:   c.pipelineId,
		buildId:      c.buildId,
		projectId:    c.projectId,
		orgId:        c.orgId,
		triggeredBy:  c.triggeredBy,
		currentJob:   c.currentJob,
		currentStep:  c.currentStep,
		jobIndex:     c.jobIndex,
		stepIndex:    c.stepIndex,
		aborted:      c.aborted,
		abortError:   c.abortError,
		stateMachine: sm,
	}

	// Register hooks for terminal states
	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS,
		func(state pipelinev1.PipelineStatus) error {
			newCtx.mu.Lock()
			if newCtx.endTime == nil {
				now := time.Now()
				newCtx.endTime = &now
			}
			newCtx.mu.Unlock()
			return nil
		})

	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED,
		func(state pipelinev1.PipelineStatus) error {
			newCtx.mu.Lock()
			if newCtx.endTime == nil {
				now := time.Now()
				newCtx.endTime = &now
			}
			newCtx.mu.Unlock()
			return nil
		})

	sm.OnEnter(
		pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED,
		func(state pipelinev1.PipelineStatus) error {
			newCtx.mu.Lock()
			if newCtx.endTime == nil {
				now := time.Now()
				newCtx.endTime = &now
			}
			newCtx.mu.Unlock()
			return nil
		})

	// Copy keys and store
	c.mu.RLock()
	maps.Copy(newCtx.keys, c.keys)
	maps.Copy(newCtx.store, c.store)
	c.mu.RUnlock()
	return newCtx
}

// Set stores a key-value pair in the context
func (c *Context) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[key] = value
}

// Get retrieves a value by key from the context
func (c *Context) Get(key string) (value any, exists bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, exists = c.keys[key]
	return
}

// MustGet retrieves a value by key, panics if not found
func (c *Context) MustGet(key string) any {
	value, exists := c.Get(key)
	if !exists {
		panic(fmt.Sprintf("key %q does not exist", key))
	}
	return value
}

// GetString returns the value associated with the key as a string
// Optimized: single map lookup with type assertion
func (c *Context) GetString(key string) (s string) {
	c.mu.RLock()
	val, ok := c.keys[key]
	c.mu.RUnlock()
	if ok && val != nil {
		s, _ = val.(string)
	}
	return
}

// GetInt returns the value associated with the key as an integer
// Optimized: single map lookup with type assertion
func (c *Context) GetInt(key string) (i int) {
	c.mu.RLock()
	val, ok := c.keys[key]
	c.mu.RUnlock()
	if ok && val != nil {
		i, _ = val.(int)
	}
	return
}

// GetBool returns the value associated with the key as a boolean
// Optimized: single map lookup with type assertion
func (c *Context) GetBool(key string) (b bool) {
	c.mu.RLock()
	val, ok := c.keys[key]
	c.mu.RUnlock()
	if ok && val != nil {
		b, _ = val.(bool)
	}
	return
}

// Store stores a value in the store map (for temporary data)
func (c *Context) Store(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = value
}

// Retrieve retrieves a value from the store map
func (c *Context) Retrieve(key string) (value any, exists bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, exists = c.store[key]
	return
}

// PipelineId returns the pipeline ID
func (c *Context) PipelineId() string {
	return c.pipelineId
}

// SetPipelineId sets the pipeline ID
func (c *Context) SetPipelineId(id string) {
	c.pipelineId = id
}

// BuildId returns the build ID
func (c *Context) BuildId() string {
	return c.buildId
}

// SetBuildId sets the build ID
func (c *Context) SetBuildId(id string) {
	c.buildId = id
}

// ProjectId returns the project ID
func (c *Context) ProjectId() string {
	return c.projectId
}

// SetProjectId sets the project ID
func (c *Context) SetProjectId(id string) {
	c.projectId = id
}

// OrgId returns the organization ID
func (c *Context) OrgId() string {
	return c.orgId
}

// SetOrgId sets the organization ID
func (c *Context) SetOrgId(id string) {
	c.orgId = id
}

// TriggeredBy returns who triggered the pipeline
func (c *Context) TriggeredBy() string {
	return c.triggeredBy
}

// SetTriggeredBy sets who triggered the pipeline
func (c *Context) SetTriggeredBy(user string) {
	c.triggeredBy = user
}

// Pipeline returns the pipeline specification
func (c *Context) Pipeline() *spec.Pipeline {
	return c.pipeline
}

// CurrentJob returns the current executing job
func (c *Context) CurrentJob() *spec.Job {
	return c.currentJob
}

// SetCurrentJob sets the current executing job
func (c *Context) SetCurrentJob(job *spec.Job, index int) {
	c.currentJob = job
	c.jobIndex = index
}

// CurrentStep returns the current executing step
func (c *Context) CurrentStep() *spec.Step {
	return c.currentStep
}

// SetCurrentStep sets the current executing step
func (c *Context) SetCurrentStep(step *spec.Step, index int) {
	c.currentStep = step
	c.stepIndex = index
}

// JobIndex returns the current job index
func (c *Context) JobIndex() int {
	return c.jobIndex
}

// StepIndex returns the current step index
func (c *Context) StepIndex() int {
	return c.stepIndex
}

// ExecutionContext returns the legacy execution context
func (c *Context) ExecutionContext() *ExecutionContext {
	return c.execCtx
}

// Logger returns the logger from execution context
func (c *Context) Logger() log.Logger {
	if c.execCtx != nil {
		return c.execCtx.Logger
	}
	return log.Logger{}
}

// PluginManager returns the plugin manager from execution context
func (c *Context) PluginManager() *plugin.Manager {
	if c.execCtx != nil {
		return c.execCtx.PluginManager
	}
	return nil
}

// BuiltinManager returns the builtin manager from execution context
func (c *Context) BuiltinManager() *builtin.Manager {
	if c.execCtx != nil {
		return c.execCtx.BuiltinManager
	}
	return nil
}

// AgentManager returns the agent manager from execution context
func (c *Context) AgentManager() *AgentManager {
	if c.execCtx != nil {
		return c.execCtx.AgentManager
	}
	return nil
}

// WorkspaceRoot returns the workspace root path
func (c *Context) WorkspaceRoot() string {
	if c.execCtx != nil {
		return c.execCtx.WorkspaceRoot
	}
	return ""
}

// JobWorkspace returns workspace path for job
func (c *Context) JobWorkspace(jobName string) string {
	if c.execCtx != nil {
		return c.execCtx.JobWorkspace(jobName)
	}
	return ""
}

// StepWorkspace returns workspace path for step
func (c *Context) StepWorkspace(jobName, stepName string) string {
	if c.execCtx != nil {
		return c.execCtx.StepWorkspace(jobName, stepName)
	}
	return ""
}

// Env returns the environment variables
func (c *Context) Env() map[string]string {
	if c.execCtx != nil {
		return c.execCtx.Env
	}
	return make(map[string]string)
}

// Error adds an error to the context
func (c *Context) Error(err error) {
	if err != nil {
		c.mu.Lock()
		c.errors = append(c.errors, err)
		c.mu.Unlock()
	}
}

// Errors returns all errors
func (c *Context) Errors() []error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.errors
}

// Abort aborts the pipeline execution with an error
func (c *Context) Abort() {
	c.mu.Lock()
	c.aborted = true
	c.abortError = fmt.Errorf("pipeline aborted")
	cancel := c.cancel
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Transition to CANCELED state (must be outside lock to avoid deadlock with hooks)
	_ = c.stateMachine.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED)
}

// AbortWithError aborts the pipeline execution with a specific error
func (c *Context) AbortWithError(err error) {
	c.mu.Lock()
	c.aborted = true
	c.abortError = err
	if err != nil {
		c.errors = append(c.errors, err)
	}
	c.mu.Unlock()

	// Transition to CANCELED state (must be outside lock to avoid deadlock with hooks)
	_ = c.stateMachine.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED)
}

// IsAborted returns whether the pipeline is aborted
func (c *Context) IsAborted() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.aborted
}

// AbortError returns the abort error
func (c *Context) AbortError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.abortError
}

// Use adds middleware handlers to the context
func (c *Context) Use(middleware ...HandlerFunc) {
	c.handlers = append(c.handlers, middleware...)
}

// Next executes the next handler in the middleware chain
func (c *Context) Next() {
	c.index++
	for c.index < len(c.handlers) {
		c.handlers[c.index](c)
		c.index++
	}
}

// Reset resets the middleware chain index
func (c *Context) Reset() {
	c.index = -1
}

// StartTime returns the pipeline start time
func (c *Context) StartTime() time.Time {
	return c.startTime
}

// EndTime returns the pipeline end time
func (c *Context) EndTime() *time.Time {
	return c.endTime
}

// SetEndTime sets the pipeline end time
func (c *Context) SetEndTime(t time.Time) {
	c.endTime = &t
}

// Duration returns the pipeline execution duration
func (c *Context) Duration() time.Duration {
	if c.endTime != nil {
		return c.endTime.Sub(c.startTime)
	}
	return time.Since(c.startTime)
}

// Done returns a channel that's closed when the context is cancelled
func (c *Context) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err returns the error if the context is cancelled
func (c *Context) Err() error {
	return c.ctx.Err()
}

// Deadline returns the deadline if set
func (c *Context) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

// Value returns the value associated with key in the underlying context
func (c *Context) Value(key any) any {
	return c.ctx.Value(key)
}

// WithValue creates a new context with the given key-value pair
func (c *Context) WithValue(key, val any) *Context {
	newCtx := c.WithContext(context.WithValue(c.ctx, key, val))
	return newCtx
}

// WithTimeout creates a new context with timeout
func (c *Context) WithTimeout(timeout time.Duration) (*Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	return c.WithContext(ctx), cancel
}

// WithCancel creates a new context with cancellation
func (c *Context) WithCancel() (*Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(c.ctx)
	return c.WithContext(ctx), cancel
}

// LogJob Log logs a message at the job level
func (c *Context) LogJob(job, msg string) {
	if c.execCtx != nil {
		c.execCtx.LogJob(job, msg)
	}
}

// LogStep logs a message at the step level
func (c *Context) LogStep(job, step, msg string) {
	if c.execCtx != nil {
		c.execCtx.LogStep(job, step, msg)
	}
}

// EvalCondition evaluates a condition expression
func (c *Context) EvalCondition(conditionExpr string) (bool, error) {
	if c.execCtx != nil {
		return c.execCtx.EvalCondition(conditionExpr)
	}
	return true, nil
}

// EvalConditionWithContext evaluates a condition with additional context
func (c *Context) EvalConditionWithContext(conditionExpr string, context map[string]any) (bool, error) {
	if c.execCtx != nil {
		return c.execCtx.EvalConditionWithContext(conditionExpr, context)
	}
	return true, nil
}

// ResolveVariable resolves variable substitution
func (c *Context) ResolveVariable(value string) string {
	if c.execCtx != nil {
		return c.execCtx.ResolveVariable(value)
	}
	return value
}

// ResolveVariables resolves variables in a map recursively
func (c *Context) ResolveVariables(params map[string]any) map[string]any {
	if c.execCtx != nil {
		return c.execCtx.ResolveVariables(params)
	}
	return params
}

// ResolveStepEnv resolves environment variables for step
func (c *Context) ResolveStepEnv(job *spec.Job, step *spec.Step) map[string]string {
	if c.execCtx != nil {
		return c.execCtx.ResolveStepEnv(job, step)
	}
	return make(map[string]string)
}

// MarshalParams marshals params to JSON
func (c *Context) MarshalParams(params map[string]any) (json.RawMessage, error) {
	if c.execCtx != nil {
		return c.execCtx.MarshalParams(params)
	}
	return json.Marshal(params)
}

// SendNotification sends a notification
func (c *Context) SendNotification(ctx context.Context, item *spec.NotifyItem, success bool) error {
	if c.execCtx != nil {
		return c.execCtx.SendNotification(ctx, item, success)
	}
	return nil
}

// Status returns the current pipeline status
func (c *Context) Status() pipelinev1.PipelineStatus {
	if c.stateMachine == nil {
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING
	}
	return c.stateMachine.Current()
}

// SetStatus sets the pipeline status (for initialization or recovery)
func (c *Context) SetStatus(status pipelinev1.PipelineStatus) {
	if c.stateMachine != nil {
		c.stateMachine.SetCurrent(status)
	}
}

// TransitionTo transitions the pipeline to a new status
func (c *Context) TransitionTo(status pipelinev1.PipelineStatus) error {
	if c.stateMachine == nil {
		return fmt.Errorf("state machine not initialized")
	}
	return c.stateMachine.TransitionTo(status)
}

// CanTransitionTo checks if a transition to the target status is valid
func (c *Context) CanTransitionTo(status pipelinev1.PipelineStatus) bool {
	if c.stateMachine == nil {
		return false
	}
	return c.stateMachine.CanTransitionTo(status)
}

// StateMachine returns the underlying state machine
func (c *Context) StateMachine() *statemachine.StateMachine[pipelinev1.PipelineStatus] {
	return c.stateMachine
}

// ToMap converts the context to a map for serialization
func (c *Context) ToMap() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]any)
	result["pipelineId"] = c.pipelineId
	result["buildId"] = c.buildId
	result["projectId"] = c.projectId
	result["orgId"] = c.orgId
	result["triggeredBy"] = c.triggeredBy
	result["startTime"] = c.startTime
	if c.endTime != nil {
		result["endTime"] = *c.endTime
	}
	result["duration"] = c.Duration().String()
	result["status"] = string(c.Status())
	if c.currentJob != nil {
		result["currentJob"] = c.currentJob.Name
	}
	if c.currentStep != nil {
		result["currentStep"] = c.currentStep.Name
	}
	result["jobIndex"] = c.jobIndex
	result["stepIndex"] = c.stepIndex
	result["aborted"] = c.aborted
	if len(c.errors) > 0 {
		errorMsgs := make([]string, len(c.errors))
		for i, err := range c.errors {
			errorMsgs[i] = err.Error()
		}
		result["errors"] = errorMsgs
	}

	// Copy keys
	if len(c.keys) > 0 {
		result["keys"] = c.keys
	}

	// Add state machine history if available
	if c.stateMachine != nil {
		history := c.stateMachine.History()
		if len(history) > 0 {
			historyData := make([]map[string]any, len(history))
			for i, record := range history {
				historyData[i] = map[string]any{
					"from":      string(record.From),
					"to":        string(record.To),
					"event":     string(record.Event),
					"timestamp": record.Timestamp,
					"error":     record.Error != nil,
				}
				if record.Error != nil {
					historyData[i]["errorMessage"] = record.Error.Error()
				}
			}
			result["stateHistory"] = historyData
		}
	}

	return result
}
