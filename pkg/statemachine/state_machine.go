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

package statemachine

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/ringbuffer"
)

// Event represents an event that triggers a state transition in the FSM.
// Events are optional - state transitions can also be triggered directly.
//
// Semantics:
// - In Transition/TransitionToWithEvent, event is only metadata (passed to hooks, recorded in history).
// - In TriggerEvent, event is used for routing: (currentState, event) -> targetState.
type Event string

// TransitionHook is triggered when a state transition occurs.
type TransitionHook[T comparable] func(from, to T, event Event) error

// StateHook is triggered when entering or exiting a state.
type StateHook[T comparable] func(state T) error

// TransitionValidator validates whether a state transition is allowed.
type TransitionValidator[T comparable] func(from, to T, event Event) error

// TransitionRecord records a state transition in the FSM history.
// Error is the error message when the transition or a hook failed; Success is true when the transition committed and hooks ran without error.
type TransitionRecord[T comparable] struct {
	From      T
	To        T
	Event     Event
	Timestamp time.Time
	Error     string
	Success   bool
}

// StateMachine is a generic Finite State Machine implementation.
// It supports:
//   - State transitions with optional events
//   - Hooks (OnEnter, OnExit, OnTransition)
//   - Validators for transition validation
//   - Transition history tracking
//   - Graphviz DOT export for visualization
//
// The StateMachine is thread-safe and can be used concurrently.
//
// Important semantics:
//   - This implementation treats the zero value of T as "unset" in some places
//     (e.g. SetCurrent deciding whether to set initial state, ToDot deciding whether to render start/current).
//     If the zero value of T is a valid state in your domain, always initialize with NewWithState/SetCurrent early
//     and avoid relying on "unset" detection.
//   - Hooks and OnError are executed after the internal mutex is released. Hooks may safely call back into
//     this StateMachine (e.g. Current/Transition/etc.). When hooks run, Current() already reflects the new state.
type StateMachine[T comparable] struct {
	mu sync.RWMutex

	currentState T
	initialState T

	hasInitial bool

	// validTransitions: from -> set of valid next states, O(1) lookup
	validTransitions map[T]map[T]struct{}
	// eventTransitions: from -> event -> target state, O(1) lookup
	eventTransitions map[T]map[Event]T

	history        *ringbuffer.OverwritingRingBuffer[TransitionRecord[T]]
	maxHistorySize int

	onTransition []TransitionHook[T]
	onEnter      map[T][]StateHook[T]
	onExit       map[T][]StateHook[T]
	validators   []TransitionValidator[T]

	// stateSet caches all states that appear in validTransitions/eventTransitions for O(1) GetAllStates
	stateSet map[T]struct{}

	onError func(from, to T, event Event, err error)
}

// GraphIssue represents a graph validation issue.
type GraphIssue[T comparable] struct {
	Type    string
	State   T
	Related []T
	Message string
}

// defaultHistorySize is the default capacity of the history ring buffer.
const defaultHistorySize = 100

// Errors returned by Transition, TransitionTo, TriggerEvent, etc.
// Callers can use errors.Is(err, statemachine.ErrStateMismatch) to distinguish.
var (
	ErrStateMismatch      = errors.New("state mismatch")
	ErrInvalidTransition  = errors.New("invalid transition")
	ErrEventNotDefined    = errors.New("event transition not defined")
	ErrInvalidHistorySize = errors.New("history size must be greater than 0")
)

// ValidateHistorySize returns ErrInvalidHistorySize if size <= 0.
// Use before SetMaxHistorySize when you need to propagate an error.
func ValidateHistorySize(size int) error {
	if size <= 0 {
		return ErrInvalidHistorySize
	}
	return nil
}

// New creates a new StateMachine instance.
//
// Note: New does not set initial/current state. If you rely on Initial()/ToDot() start node,
// prefer NewWithState.
func New[T comparable]() *StateMachine[T] {

	return &StateMachine[T]{
		validTransitions: make(map[T]map[T]struct{}),
		eventTransitions: make(map[T]map[Event]T),
		onEnter:          make(map[T][]StateHook[T]),
		onExit:           make(map[T][]StateHook[T]),
		stateSet:         make(map[T]struct{}),
		history:          ringbuffer.NewOverwritingRingBuffer[TransitionRecord[T]](defaultHistorySize),
		maxHistorySize:   defaultHistorySize,
	}
}

// NewWithState creates a new StateMachine with an initial state.
//
// It sets both currentState and initialState to the provided value.
func NewWithState[T comparable](initialState T) *StateMachine[T] {
	sm := New[T]()
	sm.currentState = initialState
	sm.initialState = initialState
	sm.hasInitial = true
	sm.stateSet[initialState] = struct{}{}
	return sm
}

// Allow registers valid state transitions (compatibility method).
// This is equivalent to AddTransitions.
//
// Direction: from -> to...
// Constraint: edges must be registered before a transition is allowed.
func (sm *StateMachine[T]) Allow(from T, to ...T) *StateMachine[T] {
	return sm.AddTransitions(from, to...)
}

// addToStateSet records from and to in the state set (caller must hold sm.mu).
func (sm *StateMachine[T]) addToStateSet(states ...T) {
	for _, s := range states {
		sm.stateSet[s] = struct{}{}
	}
}

// AddTransition adds a valid state transition.
// This is the basic way to define transitions without events.
//
// Direction: from -> to
func (sm *StateMachine[T]) AddTransition(from T, to T) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.validTransitions[from] == nil {
		sm.validTransitions[from] = make(map[T]struct{})
	}
	sm.validTransitions[from][to] = struct{}{}
	sm.addToStateSet(from, to)
	return sm
}

// AddTransitions adds multiple valid state transitions from a source state.
//
// Direction: from -> to...
func (sm *StateMachine[T]) AddTransitions(from T, to ...T) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.validTransitions[from] == nil {
		sm.validTransitions[from] = make(map[T]struct{})
	}
	for _, target := range to {
		sm.validTransitions[from][target] = struct{}{}
	}
	sm.addToStateSet(from)
	sm.addToStateSet(to...)
	return sm
}

// AddEventTransition adds an event-driven state transition.
// When the specified event occurs in the from state, the FSM transitions to the to state.
//
// Direction: from + event -> to
//
// Constraints / side effects:
// - This registers the routing rule used by TriggerEvent (CanTransitionWithEvent checks this table).
// - It ALSO registers the plain edge (from -> to) in validTransitions, so Transition(from,to,...) is allowed too.
func (sm *StateMachine[T]) AddEventTransition(from T, event Event, to T) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.eventTransitions[from] == nil {
		sm.eventTransitions[from] = make(map[Event]T)
	}
	sm.eventTransitions[from][event] = to
	if sm.validTransitions[from] == nil {
		sm.validTransitions[from] = make(map[T]struct{})
	}
	sm.validTransitions[from][to] = struct{}{}
	sm.addToStateSet(from, to)
	return sm
}

// CanTransit checks if a transition from one state to another is valid (compatibility method).
func (sm *StateMachine[T]) CanTransit(from, to T) bool {
	return sm.CanTransition(from, to)
}

// CanTransition checks if a transition from one state to another is valid.
//
// Direction: from -> to
// Constraint: only checks the static edge table (validTransitions). It does not check currentState.
func (sm *StateMachine[T]) CanTransition(from, to T) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.validTransitions[from][to]
	return ok
}

// CanTransitionWithEvent checks if a transition is valid for the given event.
//
// Direction: from + event -> to (implicit)
// Constraint: checks ONLY eventTransitions (routing table). It does not check validTransitions.
func (sm *StateMachine[T]) CanTransitionWithEvent(from T, event Event) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.eventTransitions[from] == nil {
		return false
	}
	_, exists := sm.eventTransitions[from][event]
	return exists
}

// Current returns the current state of the StateMachine.
//
// It always returns a value of type T. If you created the machine via New() and never called SetCurrent(),
// the returned value will be the zero value of T.
func (sm *StateMachine[T]) Current() T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentState
}

// SetCurrent sets the current state without triggering hooks or history.
// Useful for initialization or recovery.
func (sm *StateMachine[T]) SetCurrent(state T) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.currentState = state
	sm.stateSet[state] = struct{}{}
	if !sm.hasInitial {
		sm.initialState = state
		sm.hasInitial = true
	}
}

// Initial returns the initial state of the StateMachine.
//
// If you created the machine via New() and never called SetCurrent(), the returned value will be the zero value of T.
func (sm *StateMachine[T]) Initial() T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.initialState
}

// Reset resets the StateMachine to its initial state and clears history.
//
// Reset does not validate transitions and does not run hooks.
func (sm *StateMachine[T]) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.currentState = sm.initialState
	sm.history = ringbuffer.NewOverwritingRingBuffer[TransitionRecord[T]](sm.history.Cap())
}

// GetValidNextStates returns all valid next states from the given state.
func (sm *StateMachine[T]) GetValidNextStates(from T) []T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	set := sm.validTransitions[from]
	if len(set) == 0 {
		return []T{}
	}
	result := make([]T, 0, len(set))
	for s := range set {
		result = append(result, s)
	}
	return result
}

// GetAllStates returns all states defined in the StateMachine (cached, O(1) effort).
func (sm *StateMachine[T]) GetAllStates() []T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.stateSet) == 0 {
		return []T{}
	}
	result := make([]T, 0, len(sm.stateSet))
	for s := range sm.stateSet {
		result = append(result, s)
	}
	return result
}

// History returns the transition history in chronological order.
//
// History is append-only and records both successful and failed transition attempts.
// It returns a copy of the ring buffer contents.
func (sm *StateMachine[T]) History() []TransitionRecord[T] {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.history.Snapshot()
}

// SetMaxHistorySize sets the maximum number of history records to keep.
// Resize keeps the last min(current size, size) records.
func (sm *StateMachine[T]) SetMaxHistorySize(size int) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if size <= 0 {
		return sm
	}
	sm.maxHistorySize = size
	sm.history.Resize(size)
	return sm
}

// OnTransition registers a hook that is called during any state transition.
//
// The hook runs after OnExit(from) and before currentState is updated.
func (sm *StateMachine[T]) OnTransition(h TransitionHook[T]) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onTransition = append(sm.onTransition, h)
	return sm
}

// OnEnter registers a hook that is called when entering a specific state.
//
// Note: OnEnter runs after currentState is updated. If an OnEnter hook fails, the state is NOT rolled back.
func (sm *StateMachine[T]) OnEnter(state T, h StateHook[T]) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onEnter[state] = append(sm.onEnter[state], h)
	return sm
}

// OnExit registers a hook that is called when exiting a specific state.
//
// OnExit runs before OnTransition and before currentState is updated.
func (sm *StateMachine[T]) OnExit(state T, h StateHook[T]) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onExit[state] = append(sm.onExit[state], h)
	return sm
}

// AddValidator adds a validator that checks if a transition is allowed.
//
// Validators run after the static transition edge check, and before hooks.
func (sm *StateMachine[T]) AddValidator(v TransitionValidator[T]) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.validators = append(sm.validators, v)
	return sm
}

// OnError registers an error handler that is called when a transition fails.
//
// The handler is called for any error during Transition/TransitionTo/TriggerEvent, including:
// invalid edge, validator error, or hook error.
func (sm *StateMachine[T]) OnError(handler func(from, to T, event Event, err error)) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onError = handler
	return sm
}

// Transit performs a state transition from one state to another (compatibility method).
//
// Deprecated-ish semantics: prefer TransitionTo / TriggerEvent for "current -> target" direction.
func (sm *StateMachine[T]) Transit(from, to T) error {
	return sm.Transition(from, to, "")
}

// hooksToRun holds copies of hooks to run after the lock is released.
type hooksToRun[T comparable] struct {
	exit       []StateHook[T]
	transition []TransitionHook[T]
	enter      []StateHook[T]
}

// transitionLocked validates, updates state, records history, and returns hooks to run after unlock.
// sm.mu MUST be held by the caller. It does NOT run hooks or onError.
func (sm *StateMachine[T]) transitionLocked(from, to T, event Event) (err error, hooks hooksToRun[T]) {
	startTime := time.Now()
	var errMsg string
	defer func() {
		record := TransitionRecord[T]{
			From:      from,
			To:        to,
			Event:     event,
			Timestamp: startTime,
			Error:     errMsg,
			Success:   errMsg == "",
		}
		sm.history.Push(record)
	}()

	if sm.validTransitions[from] == nil {
		errMsg = fmt.Sprintf("invalid transition: %v → %v", from, to)
		return fmt.Errorf("%w: %v → %v", ErrInvalidTransition, from, to), hooksToRun[T]{}
	}
	if _, ok := sm.validTransitions[from][to]; !ok {
		errMsg = fmt.Sprintf("invalid transition: %v → %v", from, to)
		return fmt.Errorf("%w: %v → %v", ErrInvalidTransition, from, to), hooksToRun[T]{}
	}
	for _, validator := range sm.validators {
		if vErr := validator(from, to, event); vErr != nil {
			errMsg = "validation failed: " + vErr.Error()
			return fmt.Errorf("validation failed: %w", vErr), hooksToRun[T]{}
		}
	}

	// Copy hooks so they can be run after unlock
	if h := sm.onExit[from]; len(h) > 0 {
		hooks.exit = append([]StateHook[T](nil), h...)
	}
	hooks.transition = append([]TransitionHook[T](nil), sm.onTransition...)
	if h := sm.onEnter[to]; len(h) > 0 {
		hooks.enter = append([]StateHook[T](nil), h...)
	}

	sm.currentState = to
	return nil, hooks
}

// runHooks runs exit, transition, and enter hooks. Used after unlock. On first error calls onError and returns.
func (sm *StateMachine[T]) runHooks(from, to T, event Event, h hooksToRun[T]) error {
	for _, fn := range h.exit {
		if err := fn(from); err != nil {
			if sm.onError != nil {
				sm.onError(from, to, event, fmt.Errorf("exit hook failed for state %v: %w", from, err))
			}
			return err
		}
	}
	for _, fn := range h.transition {
		if err := fn(from, to, event); err != nil {
			if sm.onError != nil {
				sm.onError(from, to, event, fmt.Errorf("transition hook failed: %w", err))
			}
			return err
		}
	}
	for _, fn := range h.enter {
		if err := fn(to); err != nil {
			if sm.onError != nil {
				sm.onError(from, to, event, fmt.Errorf("enter hook failed for state %v: %w", to, err))
			}
			return err
		}
	}
	return nil
}

// Transition performs a state transition from one state to another.
// It validates that currentState equals from, then validates the edge and validators, updates state, and runs hooks after releasing the lock.
//
// Notes:
//   - Transition checks that sm.currentState == from to avoid races when multiple goroutines transition.
//   - Hook order (after unlock): OnExit(from) -> OnTransition(from,to,event) -> OnEnter(to). When hooks run, Current() already returns to.
func (sm *StateMachine[T]) Transition(from, to T, event Event) error {
	sm.mu.Lock()
	if sm.currentState != from {
		sm.mu.Unlock()
		err := fmt.Errorf("%w: expected %v got %v", ErrStateMismatch, from, sm.currentState)
		if sm.onError != nil {
			sm.onError(from, to, event, err)
		}
		return err
	}
	err, hooks := sm.transitionLocked(from, to, event)
	sm.mu.Unlock()

	if err != nil {
		if sm.onError != nil {
			sm.onError(from, to, event, err)
		}
		return err
	}
	return sm.runHooks(from, to, event, hooks)
}

// TransitionTo performs a transition from the current state to the target state.
//
// This is the preferred API in most call sites, as it is based on the machine's currentState.
func (sm *StateMachine[T]) TransitionTo(to T) error {
	sm.mu.Lock()
	from := sm.currentState
	err, hooks := sm.transitionLocked(from, to, "")
	sm.mu.Unlock()

	if err != nil {
		if sm.onError != nil {
			sm.onError(from, to, "", err)
		}
		return err
	}
	return sm.runHooks(from, to, "", hooks)
}

// TransitionToWithEvent performs a transition from currentState to to, and records the provided event.
//
// Important:
// - event is NOT used for routing here (unlike TriggerEvent). It is metadata for hooks/history only.
// - This still validates the edge (currentState -> to) against validTransitions.
func (sm *StateMachine[T]) TransitionToWithEvent(to T, event Event) error {
	sm.mu.Lock()
	from := sm.currentState
	err, hooks := sm.transitionLocked(from, to, event)
	sm.mu.Unlock()

	if err != nil {
		if sm.onError != nil {
			sm.onError(from, to, event, err)
		}
		return err
	}
	return sm.runHooks(from, to, event, hooks)
}

// TriggerEvent triggers a state transition based on an event.
// It looks up the event transition table to find the target state.
//
// TriggerEvent is based on currentState, not an explicit "from" parameter.
func (sm *StateMachine[T]) TriggerEvent(event Event) error {
	sm.mu.Lock()
	from := sm.currentState
	var to T
	var exists bool
	if sm.eventTransitions[from] != nil {
		to, exists = sm.eventTransitions[from][event]
	}
	if !exists {
		sm.mu.Unlock()
		err := fmt.Errorf("%w: no transition defined for event %v in state %v", ErrEventNotDefined, event, from)
		if sm.onError != nil {
			sm.onError(from, *new(T), event, err)
		}
		return err
	}
	err, hooks := sm.transitionLocked(from, to, event)
	sm.mu.Unlock()

	if err != nil {
		if sm.onError != nil {
			sm.onError(from, to, event, err)
		}
		return err
	}
	return sm.runHooks(from, to, event, hooks)
}

// TransitTo performs a transition from the current state to the target state.
//
// Deprecated-ish compatibility alias of TransitionTo.
func (sm *StateMachine[T]) TransitTo(to T) error {
	return sm.TransitionTo(to)
}

// MustTransit performs a transition and panics on error (compatibility method).
func (sm *StateMachine[T]) MustTransit(from, to T) {
	sm.MustTransition(from, to, "")
}

// MustTransition performs a transition and panics on error.
func (sm *StateMachine[T]) MustTransition(from, to T, event Event) {
	if err := sm.Transition(from, to, event); err != nil {
		panic(err)
	}
}

// MustTransitTo performs a transition from current state and panics on error (compatibility method).
func (sm *StateMachine[T]) MustTransitTo(to T) {
	sm.MustTransitionTo(to)
}

// MustTransitionTo performs a transition from current state and panics on error.
func (sm *StateMachine[T]) MustTransitionTo(to T) {
	if err := sm.TransitionTo(to); err != nil {
		panic(err)
	}
}

// MustTransitionToWithEvent performs TransitionToWithEvent and panics on error.
func (sm *StateMachine[T]) MustTransitionToWithEvent(to T, event Event) {
	if err := sm.TransitionToWithEvent(to, event); err != nil {
		panic(err)
	}
}

// MustTriggerEvent triggers an event and panics on error.
func (sm *StateMachine[T]) MustTriggerEvent(event Event) {
	if err := sm.TriggerEvent(event); err != nil {
		panic(err)
	}
}

// Is checks if the current state matches the given state.
func (sm *StateMachine[T]) Is(state T) bool {
	return sm.Current() == state
}

// IsOneOf checks if the current state is one of the given states.
func (sm *StateMachine[T]) IsOneOf(states ...T) bool {
	current := sm.Current()
	return slices.Contains(states, current)
}

// CanTransitTo checks if a transition to the target state is valid from the current state (compatibility method).
func (sm *StateMachine[T]) CanTransitTo(to T) bool {
	return sm.CanTransitionTo(to)
}

// CanTransitionTo checks if a transition to the target state is valid from the current state.
func (sm *StateMachine[T]) CanTransitionTo(to T) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.validTransitions[sm.currentState][to]
	return ok
}

// ValidateGraph validates the FSM graph and returns discovered issues.
//
// It checks:
//   - unreachable states from initialState
//   - dead-end states (except self-loop only is still considered dead-end unless it has another outgoing edge)
//   - event transitions whose target edge is inconsistent (normally impossible through API, but useful for safety)
func (sm *StateMachine[T]) ValidateGraph() []GraphIssue[T] {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var issues []GraphIssue[T]

	// Inconsistent event mapping check.
	for from, evMap := range sm.eventTransitions {
		for ev, to := range evMap {
			if _, ok := sm.validTransitions[from][to]; !ok {
				issues = append(issues, GraphIssue[T]{
					Type:    "inconsistent_event_edge",
					State:   from,
					Related: []T{to},
					Message: fmt.Sprintf("event %q maps %v -> %v but plain edge is missing", ev, from, to),
				})
			}
		}
	}

	// Dead-end states.
	for state := range sm.stateSet {
		nexts := sm.validTransitions[state]
		outDegree := 0
		for to := range nexts {
			if to != state {
				outDegree++
			}
		}
		if len(nexts) == 0 || outDegree == 0 {
			issues = append(issues, GraphIssue[T]{
				Type:    "dead_end",
				State:   state,
				Related: nil,
				Message: fmt.Sprintf("state %v has no outgoing transition to a different state", state),
			})
		}
	}

	// Unreachable states.
	if sm.hasInitial {
		reached := make(map[T]struct{})
		queue := []T{sm.initialState}
		reached[sm.initialState] = struct{}{}

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]

			for next := range sm.validTransitions[cur] {
				if _, ok := reached[next]; ok {
					continue
				}
				reached[next] = struct{}{}
				queue = append(queue, next)
			}
		}

		for state := range sm.stateSet {
			if _, ok := reached[state]; !ok {
				issues = append(issues, GraphIssue[T]{
					Type:    "unreachable",
					State:   state,
					Related: nil,
					Message: fmt.Sprintf("state %v is unreachable from initial state %v", state, sm.initialState),
				})
			}
		}
	}

	return issues
}

// ToDot exports the StateMachine as a Graphviz DOT format string.
func (sm *StateMachine[T]) ToDot(name string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var b strings.Builder
	fmt.Fprintf(&b, "digraph %s {\n", name)
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=circle];\n")

	if sm.hasInitial {
		b.WriteString("  start [shape=point];\n")
		fmt.Fprintf(&b, "  start -> \"%v\";\n", sm.initialState)
	}
	fmt.Fprintf(&b, "  \"%v\" [style=filled, fillcolor=lightblue];\n", sm.currentState)

	edgeEvents := make(map[T]map[T][]string)
	for from, evMap := range sm.eventTransitions {
		if edgeEvents[from] == nil {
			edgeEvents[from] = make(map[T][]string)
		}
		for ev, to := range evMap {
			edgeEvents[from][to] = append(edgeEvents[from][to], string(ev))
		}
	}

	for from, tos := range sm.validTransitions {
		for to := range tos {
			labels := edgeEvents[from][to]
			if len(labels) > 0 {
				sort.Strings(labels)
				fmt.Fprintf(&b, "  \"%v\" -> \"%v\" [label=\"%s\"];\n", from, to, strings.Join(labels, ", "))
			} else {
				fmt.Fprintf(&b, "  \"%v\" -> \"%v\";\n", from, to)
			}
		}
	}

	b.WriteString("}\n")
	return b.String()
}
