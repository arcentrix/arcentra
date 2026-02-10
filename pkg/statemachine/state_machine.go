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
	"fmt"
	"slices"
	"sync"
	"time"
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
type TransitionRecord[T comparable] struct {
	From      T
	To        T
	Event     Event
	Timestamp time.Time
	Error     error
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
//   - Hooks and the OnError handler are executed while holding the internal mutex. Hooks MUST NOT call back into
//     this StateMachine (e.g. Current/Transition/etc.), otherwise it may deadlock.
type StateMachine[T comparable] struct {
	mu sync.RWMutex

	currentState T
	initialState T

	// Transition definitions: from state -> list of valid next states
	validTransitions map[T][]T
	// Event-based transitions: (from state, event) -> target state
	eventTransitions map[transitionKey[T]]T

	history        []TransitionRecord[T]
	maxHistorySize int

	onTransition []TransitionHook[T]
	onEnter      map[T][]StateHook[T]
	onExit       map[T][]StateHook[T]
	validators   []TransitionValidator[T]

	onError func(from, to T, event Event, err error)
}

type transitionKey[T comparable] struct {
	From  T
	Event Event
}

// New creates a new StateMachine instance.
//
// Note: New does not set initial/current state. If you rely on Initial()/ToDot() start node,
// prefer NewWithState.
func New[T comparable]() *StateMachine[T] {
	return &StateMachine[T]{
		validTransitions: make(map[T][]T),
		eventTransitions: make(map[transitionKey[T]]T),
		onEnter:          make(map[T][]StateHook[T]),
		onExit:           make(map[T][]StateHook[T]),
		history:          make([]TransitionRecord[T], 0),
		maxHistorySize:   100,
	}
}

// NewWithState creates a new StateMachine with an initial state.
//
// It sets both currentState and initialState to the provided value.
func NewWithState[T comparable](initialState T) *StateMachine[T] {
	sm := New[T]()
	sm.currentState = initialState
	sm.initialState = initialState
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

// AddTransition adds a valid state transition.
// This is the basic way to define transitions without events.
//
// Direction: from -> to
func (sm *StateMachine[T]) AddTransition(from T, to T) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !slices.Contains(sm.validTransitions[from], to) {
		sm.validTransitions[from] = append(sm.validTransitions[from], to)
	}
	return sm
}

// AddTransitions adds multiple valid state transitions from a source state.
//
// Direction: from -> to...
func (sm *StateMachine[T]) AddTransitions(from T, to ...T) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, target := range to {
		if !slices.Contains(sm.validTransitions[from], target) {
			sm.validTransitions[from] = append(sm.validTransitions[from], target)
		}
	}
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
	key := transitionKey[T]{From: from, Event: event}
	sm.eventTransitions[key] = to
	// Also add to valid transitions
	if !slices.Contains(sm.validTransitions[from], to) {
		sm.validTransitions[from] = append(sm.validTransitions[from], to)
	}
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
	return slices.Contains(sm.validTransitions[from], to)
}

// CanTransitionWithEvent checks if a transition is valid for the given event.
//
// Direction: from + event -> to (implicit)
// Constraint: checks ONLY eventTransitions (routing table). It does not check validTransitions.
func (sm *StateMachine[T]) CanTransitionWithEvent(from T, event Event) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	key := transitionKey[T]{From: from, Event: event}
	_, exists := sm.eventTransitions[key]
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

// SetCurrent sets the current state without triggering hooks.
// This is useful for initialization or recovery scenarios.
//
// Semantics:
// - It never validates transitions.
// - It does not write to History.
// - If initialState is still the zero value of T, it will also set initialState to the given state.
func (sm *StateMachine[T]) SetCurrent(state T) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.currentState = state
	if sm.initialState == *new(T) {
		sm.initialState = state
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
	sm.history = make([]TransitionRecord[T], 0)
}

// GetValidNextStates returns all valid next states from the given state.
func (sm *StateMachine[T]) GetValidNextStates(from T) []T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if states, ok := sm.validTransitions[from]; ok {
		result := make([]T, len(states))
		copy(result, states)
		return result
	}
	return []T{}
}

// GetAllStates returns all states defined in the StateMachine.
func (sm *StateMachine[T]) GetAllStates() []T {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	stateSet := make(map[T]bool)
	for from, tos := range sm.validTransitions {
		stateSet[from] = true
		for _, to := range tos {
			stateSet[to] = true
		}
	}
	states := make([]T, 0, len(stateSet))
	for state := range stateSet {
		states = append(states, state)
	}
	return states
}

// History returns the transition history.
//
// History is append-only and records both successful and failed transition attempts.
// It returns a copy of the internal slice.
func (sm *StateMachine[T]) History() []TransitionRecord[T] {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]TransitionRecord[T], len(sm.history))
	copy(result, sm.history)
	return result
}

// SetMaxHistorySize sets the maximum number of history records to keep.
//
// If size is smaller than the current history length, the oldest records are dropped.
func (sm *StateMachine[T]) SetMaxHistorySize(size int) *StateMachine[T] {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxHistorySize = size
	if len(sm.history) > size {
		sm.history = sm.history[len(sm.history)-size:]
	}
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

// Transition performs a state transition from one state to another.
// It validates the transition, runs validators, triggers hooks, and records history.
//
// Notes:
//   - The transition is validated against the (from -> to) edge, not against the currentState.
//     For consistency, callers should usually pass from == Current() (or use TransitionTo).
//   - Hook order: OnExit(from) -> OnTransition(from,to,event) -> update currentState -> OnEnter(to).
//   - If an OnEnter hook fails, currentState has already been updated (no rollback).
func (sm *StateMachine[T]) Transition(from, to T, event Event) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.transitionLocked(from, to, event)
}

func (sm *StateMachine[T]) transitionLocked(from, to T, event Event) error {
	// sm.mu MUST be held by the caller.

	startTime := time.Now()
	var transitionErr error

	defer func() {
		record := TransitionRecord[T]{
			From:      from,
			To:        to,
			Event:     event,
			Timestamp: startTime,
			Error:     transitionErr,
		}
		sm.history = append(sm.history, record)

		if len(sm.history) > sm.maxHistorySize {
			sm.history = sm.history[len(sm.history)-sm.maxHistorySize:]
		}

		if transitionErr != nil && sm.onError != nil {
			sm.onError(from, to, event, transitionErr)
		}
	}()

	// Validate transition
	if !slices.Contains(sm.validTransitions[from], to) {
		transitionErr = fmt.Errorf("invalid transition: %v → %v", from, to)
		return transitionErr
	}

	// Run validators
	for _, validator := range sm.validators {
		if err := validator(from, to, event); err != nil {
			transitionErr = fmt.Errorf("validation failed: %w", err)
			return transitionErr
		}
	}

	// Trigger OnExit hooks
	if hooks := sm.onExit[from]; len(hooks) > 0 {
		for _, h := range hooks {
			if err := h(from); err != nil {
				transitionErr = fmt.Errorf("exit hook failed for state %v: %w", from, err)
				return transitionErr
			}
		}
	}

	// Trigger OnTransition hooks
	for _, h := range sm.onTransition {
		if err := h(from, to, event); err != nil {
			transitionErr = fmt.Errorf("transition hook failed: %w", err)
			return transitionErr
		}
	}

	// Update current state
	sm.currentState = to

	// Trigger OnEnter hooks
	if hooks := sm.onEnter[to]; len(hooks) > 0 {
		for _, h := range hooks {
			if err := h(to); err != nil {
				transitionErr = fmt.Errorf("enter hook failed for state %v: %w", to, err)
				return transitionErr
			}
		}
	}

	return nil
}

// TransitTo performs a transition from the current state to the target state.
//
// Deprecated-ish compatibility alias of TransitionTo.
func (sm *StateMachine[T]) TransitTo(to T) error {
	return sm.TransitionTo(to)
}

// TransitionTo performs a transition from the current state to the target state.
//
// This is the preferred API in most call sites, as it is based on the machine's currentState.
func (sm *StateMachine[T]) TransitionTo(to T) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.transitionLocked(sm.currentState, to, "")
}

// TransitionToWithEvent performs a transition from currentState to to, and records the provided event.
//
// Important:
// - event is NOT used for routing here (unlike TriggerEvent). It is metadata for hooks/history only.
// - This still validates the edge (currentState -> to) against validTransitions.
func (sm *StateMachine[T]) TransitionToWithEvent(to T, event Event) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.transitionLocked(sm.currentState, to, event)
}

// TriggerEvent triggers a state transition based on an event.
// It looks up the event transition table to find the target state.
//
// TriggerEvent is based on currentState, not an explicit "from" parameter.
func (sm *StateMachine[T]) TriggerEvent(event Event) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	current := sm.currentState
	key := transitionKey[T]{From: current, Event: event}
	to, exists := sm.eventTransitions[key]
	if !exists {
		return fmt.Errorf("no transition defined for event %v in state %v", event, current)
	}
	return sm.transitionLocked(current, to, event)
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
	return slices.Contains(sm.validTransitions[sm.currentState], to)
}

// ToDot exports the StateMachine as a Graphviz DOT format string.
//
// Semantics:
// - If initialState is not the zero value of T, it will render a "start" node pointing to initialState.
// - If currentState is not the zero value of T, it will highlight currentState with a fill color.
func (sm *StateMachine[T]) ToDot(name string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	dot := fmt.Sprintf("digraph %s {\n", name)
	dot += "  rankdir=LR;\n"
	dot += "  node [shape=circle];\n"

	if sm.initialState != *new(T) {
		dot += "  start [shape=point];\n"
		dot += fmt.Sprintf("  start -> \"%v\";\n", sm.initialState)
	}

	if sm.currentState != *new(T) {
		dot += fmt.Sprintf("  \"%v\" [style=filled, fillcolor=lightblue];\n", sm.currentState)
	}

	for from, tos := range sm.validTransitions {
		for _, to := range tos {
			label := ""
			// Check if there's an event for this transition
			for key, target := range sm.eventTransitions {
				if key.From == from && target == to {
					if label != "" {
						label += ", "
					}
					label += string(key.Event)
				}
			}
			if label != "" {
				dot += fmt.Sprintf("  \"%v\" -> \"%v\" [label=\"%s\"];\n", from, to, label)
			} else {
				dot += fmt.Sprintf("  \"%v\" -> \"%v\";\n", from, to)
			}
		}
	}

	dot += "}\n"
	return dot
}
