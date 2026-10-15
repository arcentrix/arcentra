// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandboxexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/sandbox"
)

// TestLiveStepStartDeadlineAfterCreate 验证创建返回时过期仍只清理而不启动任务
func TestLiveStepStartDeadlineAfterCreate(t *testing.T) {
	_, session, backend, _ := newStepSession(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	backend.beforeCreate = func(*sandbox.CreateOptions) error {
		<-ctx.Done()
		// 模拟创建已生效但响应延迟；真实 Start 必须再次验证原启动预算
		return nil
	}
	if err := session.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late create escaped start bound: %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	created := 0
	for _, event := range backend.events {
		if event == "create" {
			created++
		}
		if event == "start" || event == "execute" || event == "live-execute" || event == "live-resume" {
			t.Fatalf("expired creation advanced runtime effect: %s", event)
		}
	}
	if created != 1 {
		t.Fatalf("fixture did not cross real Session.Start creation: %d", created)
	}
	for _, state := range backend.states {
		if state != (sandbox.ResourceState{}) {
			t.Fatalf("expired startup leaked owned resources: %+v", state)
		}
	}
}
