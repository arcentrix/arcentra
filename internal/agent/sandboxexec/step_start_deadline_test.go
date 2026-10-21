// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandboxexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/arcentrix/arcentra/pkg/sandbox"
)

// TestLiveStepStartDeadlineAfterCreate 验证创建返回时过期仍只清理而不启动任务
func TestLiveStepStartDeadlineAfterCreate(t *testing.T) {
	_, session, backend, _ := newStepSession(t, 2)
	// 假时钟只在创建入口等待取消时推进，不让宿主调度或日志 IO 耗尽启动预算
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		deadline, _ := ctx.Deadline()
		backend.beforeCreate = func(createCtx context.Context, _ *sandbox.CreateOptions) error {
			if actual, ok := createCtx.Deadline(); !ok || !actual.Equal(deadline) {
				t.Fatalf("runtime creation changed the original deadline: %v", actual)
			}
			if err := createCtx.Err(); err != nil {
				t.Fatalf("fixture expired before runtime creation: %v", err)
			}
			// 等待实际传给运行时的子上下文，而不是尚未完成传播的父取消信号
			<-createCtx.Done()
			// 模拟创建已生效但响应延迟；真实 Start 必须再次验证原启动预算
			return nil
		}
		if err := session.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("late create escaped start bound: %v (events: %v)", err, backend.events)
		}
	})
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

// TestLiveStepStartDeadlineBeforeCreate 验证启动前过期不会进入创建或破坏可清理日志
func TestLiveStepStartDeadlineBeforeCreate(t *testing.T) {
	c, session, backend, _ := newStepSession(t, 2)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		<-ctx.Done()
		if err := session.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired context admitted startup: %v", err)
		}
	})
	if countEvent(backend.events, "create") != 0 || c.fatal != nil {
		t.Fatalf("expired startup reached creation or poisoned the journal: events=%v fatal=%v", backend.events, c.fatal)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestLiveStepStartDeadlineDuringJournalSync 验证日志提交中断仍保留证据并要求人工恢复
func TestLiveStepStartDeadlineDuringJournalSync(t *testing.T) {
	c, session, backend, _ := newStepSession(t, 2)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		c.hook = func(point string) error {
			if point == "record.sync" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		if err := session.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("interrupted journal commit lost deadline: %v", err)
		}
	})
	if countEvent(backend.events, "create") != 0 || !errors.Is(c.fatal, context.DeadlineExceeded) {
		t.Fatalf("interrupted journal commit did not fail closed: events=%v fatal=%v", backend.events, c.fatal)
	}
	if err := session.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain journal allowed cleanup: %v", err)
	}
	if countEvent(backend.events, "remove-owned") != 0 {
		t.Fatal("uncertain journal allowed runtime deletion")
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatalf("uncertain journal lost workspace evidence: %v", err)
	}
	pending, err := filepath.Glob(filepath.Join(c.opts.JournalRoot, ".pending-*"))
	if err != nil || len(pending) != 1 {
		t.Fatalf("interrupted journal lost pending commit evidence: %v %v", pending, err)
	}
	simulateCrash(t, c)
}
