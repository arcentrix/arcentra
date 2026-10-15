//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/containerd/containerd"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// TestContainerAcceptanceObserverGuards 验证测试旁观器不伪造底层调用、成功或授权
// 此测试只用普通 fake；真实安装器明确拒绝 fake 后端
func TestContainerAcceptanceObserverGuards(t *testing.T) {
	for _, flags := range [][2]string{{"", ""}, {"1", ""}, {"", "1"}, {"true", "1"}, {"1", "1"}} {
		t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", flags[0])
		t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", flags[1])
		if ObserveContainerAcceptanceRuntime(nil, ContainerAcceptanceObserver{}) == nil {
			t.Fatal("nil runtime accepted")
		}
		backend := &fakeBackend{}
		sb := &Containerd{backend: backend, namespace: "arcentra-containerjob-acceptance-synthetic"}
		if ObserveContainerAcceptanceRuntime(sb, ContainerAcceptanceObserver{}) == nil || sb.backend != backend {
			t.Fatal("installer admitted or mutated fake runtime")
		}
	}
	t.Run("post-effect-errors-never-hide-real-errors", func(t *testing.T) {
		ctx := context.Background()
		underlying := &fakeTask{fakeProcess: newFakeProcess(false), pid: 73}
		lost := errors.New("synthetic lost response")
		actual := errors.New("actual daemon error")
		var seen []ContainerAcceptanceEvent
		backend := &acceptanceRuntimeBackend{observer: ContainerAcceptanceObserver{Observe: func(_ context.Context, event ContainerAcceptanceEvent) { seen = append(seen, event) }, AfterResume: func(_ context.Context, event ContainerAcceptanceEvent) error {
			if underlying.resumes != len(seen) {
				t.Fatal("observer ran before actual Resume")
			}
			return lost
		}}}
		ref := ResourceRef{ID: "exact", Namespace: "fixture", Owner: "owner", Snapshotter: "fixture"}
		wrapped := &acceptanceRuntimeTask{runtimeTask: underlying, container: &acceptanceRuntimeContainer{backend: backend, ref: ref}}
		if err := wrapped.Resume(ctx); !errors.Is(err, lost) || underlying.status != containerd.Running {
			t.Fatal("ACK loss did not follow actual effect")
		}
		underlying.resumeHook = func(context.Context) error { return actual }
		if err := wrapped.Resume(ctx); !errors.Is(err, actual) || !errors.Is(err, lost) {
			t.Fatal("observer hid actual error")
		}
		if len(seen) != 2 || seen[0].Err != nil || seen[1].Err != actual || seen[0].Resource != ref || seen[0].PID != 73 {
			t.Fatal("observer altered actual identity/outcomes")
		}
	})
	t.Run("actual-exec-lifecycle-once", func(t *testing.T) {
		ctx := context.Background()
		underlying := &fakeTask{fakeProcess: newFakeProcess(false), pid: 74}
		underlying.process = newFakeProcess(true)
		var seen []ContainerAcceptanceEvent
		backend := &acceptanceRuntimeBackend{observer: ContainerAcceptanceObserver{Observe: func(_ context.Context, event ContainerAcceptanceEvent) { seen = append(seen, event) }}}
		wrapped := &acceptanceRuntimeTask{runtimeTask: underlying, container: &acceptanceRuntimeContainer{backend: backend}}
		process, err := wrapped.exec(ctx, "exact-exec", &specs.Process{Args: []string{"true"}}, nil, io.Discard, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := process.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := process.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := process.Delete(ctx); err != nil {
			t.Fatal(err)
		}
		var operations []string
		for _, event := range seen {
			operations = append(operations, event.Operation)
			if event.ExecID != "exact-exec" || event.PID != 74 || event.Err != nil {
				t.Fatal("event lost exec identity")
			}
		}
		if !reflect.DeepEqual(operations, []string{"exec-create", "exec-start", "exec-delete"}) || underlying.process.deleted != 1 {
			t.Fatal("observer duplicated or skipped real lifecycle")
		}
	})
}
