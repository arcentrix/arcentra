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
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
)

// 定义测试用状态
type OrderStatus string

const (
	OrderCreated   OrderStatus = "CREATED"
	OrderPaid      OrderStatus = "PAID"
	OrderShipped   OrderStatus = "SHIPPED"
	OrderDelivered OrderStatus = "DELIVERED"
	OrderCanceled  OrderStatus = "CANCELED"
)

func TestStateMachine_Basic(t *testing.T) {
	sm := NewWithState(OrderCreated)

	sm.Allow(OrderCreated, OrderPaid, OrderCanceled).
		Allow(OrderPaid, OrderShipped, OrderCanceled).
		Allow(OrderShipped, OrderDelivered)

	// 测试当前状态
	if sm.Current() != OrderCreated {
		t.Errorf("expected current state to be %v, got %v", OrderCreated, sm.Current())
	}

	// 测试初始状态
	if sm.Initial() != OrderCreated {
		t.Errorf("expected initial state to be %v, got %v", OrderCreated, sm.Initial())
	}

	// 测试合法转移
	if err := sm.TransitTo(OrderPaid); err != nil {
		t.Errorf("expected transition to succeed, got error: %v", err)
	}

	if sm.Current() != OrderPaid {
		t.Errorf("expected current state to be %v, got %v", OrderPaid, sm.Current())
	}

	// 测试非法转移
	if err := sm.TransitTo(OrderDelivered); err == nil {
		t.Error("expected transition to fail, but it succeeded")
	}
}

func TestStateMachine_CanTransit(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid, OrderCanceled)

	if !sm.CanTransitTo(OrderPaid) {
		t.Error("expected to be able to transit to PAID")
	}

	if sm.CanTransitTo(OrderShipped) {
		t.Error("expected NOT to be able to transit to SHIPPED")
	}
}

func TestStateMachine_Hooks(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid).
		Allow(OrderPaid, OrderShipped)

	// 记录钩子执行顺序
	var executionOrder []string

	sm.OnExit(OrderCreated, func(state OrderStatus) error {
		executionOrder = append(executionOrder, "exit:created")
		return nil
	})

	sm.OnTransition(func(from, to OrderStatus, event Event) error {
		executionOrder = append(executionOrder, "transition")
		return nil
	})

	sm.OnEnter(OrderPaid, func(state OrderStatus) error {
		executionOrder = append(executionOrder, "enter:paid")
		return nil
	})

	if err := sm.TransitTo(OrderPaid); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// 验证执行顺序
	expected := []string{"exit:created", "transition", "enter:paid"}
	if len(executionOrder) != len(expected) {
		t.Fatalf("expected %d hooks, got %d", len(expected), len(executionOrder))
	}

	for i, v := range expected {
		if executionOrder[i] != v {
			t.Errorf("expected hook[%d] to be %s, got %s", i, v, executionOrder[i])
		}
	}
}

func TestStateMachine_HookErrors(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid)

	testErr := errors.New("hook error")

	// 注册一个会失败的钩子
	sm.OnEnter(OrderPaid, func(state OrderStatus) error {
		return testErr
	})

	err := sm.TransitTo(OrderPaid)
	if err == nil {
		t.Error("expected error from hook, got nil")
	}

	// 验证状态已更新（即使钩子失败）
	if sm.Current() != OrderPaid {
		t.Errorf("expected state to be %v even after hook error, got %v", OrderPaid, sm.Current())
	}
}

func TestStateMachine_Validators(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid)

	// 添加验证器
	sm.AddValidator(func(from, to OrderStatus, event Event) error {
		if to == OrderPaid {
			return errors.New("payment not allowed")
		}
		return nil
	})

	err := sm.TransitTo(OrderPaid)
	if err == nil {
		t.Error("expected validator to reject transition")
	}

	// 验证状态未改变
	if sm.Current() != OrderCreated {
		t.Errorf("expected state to remain %v, got %v", OrderCreated, sm.Current())
	}
}

func TestStateMachine_History(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid).
		Allow(OrderPaid, OrderShipped)

	// 执行几次转移
	_ = sm.TransitTo(OrderPaid)
	time.Sleep(10 * time.Millisecond)
	_ = sm.TransitTo(OrderShipped)

	history := sm.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}

	// 验证第一条记录
	if history[0].From != OrderCreated || history[0].To != OrderPaid {
		t.Errorf("unexpected first history record: %v -> %v", history[0].From, history[0].To)
	}

	// 验证第二条记录
	if history[1].From != OrderPaid || history[1].To != OrderShipped {
		t.Errorf("unexpected second history record: %v -> %v", history[1].From, history[1].To)
	}

	// 验证时间戳
	if history[1].Timestamp.Before(history[0].Timestamp) {
		t.Error("expected second record to have later timestamp")
	}
}

func TestStateMachine_MaxHistorySize(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.SetMaxHistorySize(2)
	sm.Allow(OrderCreated, OrderPaid).
		Allow(OrderPaid, OrderCreated)

	// 执行多次转移
	for i := 0; i < 5; i++ {
		_ = sm.TransitTo(OrderPaid)
		_ = sm.TransitionTo(OrderCreated)
	}

	history := sm.History()
	if len(history) != 2 {
		t.Errorf("expected history size to be limited to 2, got %d", len(history))
	}
}

func TestStateMachine_Reset(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid)

	_ = sm.TransitTo(OrderPaid)

	if sm.Current() != OrderPaid {
		t.Errorf("expected current state to be %v, got %v", OrderPaid, sm.Current())
	}

	sm.Reset()

	if sm.Current() != OrderCreated {
		t.Errorf("expected state to be reset to %v, got %v", OrderCreated, sm.Current())
	}

	if len(sm.History()) != 0 {
		t.Errorf("expected history to be cleared, got %d records", len(sm.History()))
	}
}

func TestStateMachine_IsOneOf(t *testing.T) {
	sm := NewWithState(OrderCreated)

	if !sm.IsOneOf(OrderCreated, OrderPaid) {
		t.Error("expected IsOneOf to return true")
	}

	if sm.IsOneOf(OrderPaid, OrderShipped) {
		t.Error("expected IsOneOf to return false")
	}
}

func TestStateMachine_GetValidNextStates(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid, OrderCanceled)

	states := sm.GetValidNextStates(OrderCreated)
	if len(states) != 2 {
		t.Errorf("expected 2 next states, got %d", len(states))
	}
}

func TestStateMachine_GetAllStates(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid).
		Allow(OrderPaid, OrderShipped).
		Allow(OrderShipped, OrderDelivered)

	states := sm.GetAllStates()
	if len(states) != 4 {
		t.Errorf("expected 4 states, got %d", len(states))
	}
}

func TestStateMachine_OnError(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid)

	var errorCaught bool
	sm.OnError(func(from, to OrderStatus, event Event, err error) {
		errorCaught = true
	})

	// 尝试非法转移
	_ = sm.TransitTo(OrderShipped)

	if !errorCaught {
		t.Error("expected error handler to be called")
	}
}

func TestStateMachine_Concurrency(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid).
		Allow(OrderPaid, OrderCreated)

	// 并发读写测试
	done := make(chan bool, 100)

	for i := 0; i < 50; i++ {
		safe.Go(func() {
			// 并发切换时可能出现非法转移错误，这里只验证不会死锁/崩溃
			_ = sm.TransitTo(OrderPaid)
			done <- true
		})
		safe.Go(func() {
			_ = sm.TransitionTo(OrderCreated)
			done <- true
		})
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	// 验证状态机仍然可用
	_ = sm.Current()
	_ = sm.History()
}

func TestStateMachine_ToDot(t *testing.T) {
	sm := NewWithState(OrderCreated)
	sm.Allow(OrderCreated, OrderPaid, OrderCanceled).
		Allow(OrderPaid, OrderShipped)

	dot := sm.ToDot("OrderStateMachine")

	// 基本验证
	if dot == "" {
		t.Error("expected non-empty DOT output")
	}

	// 检查是否包含关键元素
	if !contains(dot, "digraph OrderStateMachine") {
		t.Error("DOT output should contain diagram name")
	}

	if !contains(dot, "CREATED") {
		t.Error("DOT output should contain states")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
