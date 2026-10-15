//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// TestContainerAcceptancePolicyGuards 验证测试注入不能扩大生产门禁或覆盖既有状态
func TestContainerAcceptancePolicyGuards(t *testing.T) {
	const image = "docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	for _, test := range []struct {
		name      string
		enabled   string
		required  string
		image     string
		change    func(*taskqueue.OfflineRuntimePolicy)
		configure func(*Process)
	}{
		{name: "missing-opt-in", required: "1", image: image},
		{name: "missing-mandatory-flag", enabled: "1", image: image},
		{name: "invalid-opt-in", enabled: "true", required: "1", image: image},
		{name: "missing-image", enabled: "1", required: "1"},
		{name: "wrong-image", enabled: "1", required: "1", image: "other@sha256:" + strings.Repeat("b", 64)},
		{name: "disabled-policy", enabled: "1", required: "1", image: image, change: func(p *taskqueue.OfflineRuntimePolicy) { p.Enabled = false }},
		{name: "multiple-images", enabled: "1", required: "1", image: image, change: func(p *taskqueue.OfflineRuntimePolicy) { p.AllowedImages = append(p.AllowedImages, image) }},
		{name: "mutable-image", enabled: "1", required: "1", image: "alpine:latest", change: func(p *taskqueue.OfflineRuntimePolicy) { p.AllowedImages = []string{"alpine:latest"} }},
		{name: "invalid-cpu", enabled: "1", required: "1", image: image, change: func(p *taskqueue.OfflineRuntimePolicy) { p.MaxCPU = "0" }},
		{name: "invalid-memory", enabled: "1", required: "1", image: image, change: func(p *taskqueue.OfflineRuntimePolicy) { p.MaxMemory = "unlimited" }},
		{name: "active-process", enabled: "1", required: "1", image: image, configure: func(e *Process) { e.runs.Store("already-submitted", struct{}{}) }},
		{name: "stopped-process", enabled: "1", required: "1", image: image, configure: func(e *Process) { e.stopped = true }},
		{name: "already-configured", enabled: "1", required: "1", image: image, configure: func(e *Process) { e.offlineRuntimePolicy = policy.Clone() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", test.enabled)
			t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", test.required)
			t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", test.image)
			e := &Process{}
			if test.configure != nil {
				test.configure(e)
			}
			before := e.offlineRuntimePolicy.Clone()
			candidate := policy.Clone()
			if test.change != nil {
				test.change(&candidate)
			}
			if err := e.ConfigureContainerAcceptancePolicy(candidate); err == nil {
				t.Fatal("out-of-scope acceptance policy was admitted")
			}
			if !reflect.DeepEqual(before, e.offlineRuntimePolicy) {
				t.Fatal("rejected injection changed existing policy")
			}
		})
	}
}

// TestContainerAcceptancePolicyClonesOnce 验证单次注入冻结允许列表且仍不能启用生产
func TestContainerAcceptancePolicyClonesOnce(t *testing.T) {
	const image = "docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", image)
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	e := &Process{}
	if taskqueue.OfflineRuntimeAccepted {
		if err := e.ConfigureContainerAcceptancePolicy(policy); err == nil {
			t.Fatal("accepted runtime allowed obsolete test-only injection")
		}
		if e.offlineRuntimePolicy.Enabled {
			t.Fatal("rejected obsolete injection mutated policy")
		}
		return
	}
	if err := e.ConfigureContainerAcceptancePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.offlineRuntimePolicy, policy) {
		t.Fatal("acceptance profile did not preserve the exact approved policy")
	}
	policy.AllowedImages[0] = "changed@sha256:" + strings.Repeat("b", 64)
	if e.offlineRuntimePolicy.AllowedImages[0] != image {
		t.Fatal("caller mutated frozen image policy")
	}
	if err := e.ConfigureContainerAcceptancePolicy(e.offlineRuntimePolicy.Clone()); err == nil {
		t.Fatal("acceptance policy may only be configured once")
	}
	if err := e.offlineRuntimePolicy.ValidateProduction(); err == nil {
		t.Fatal("test-only injection enabled production configuration")
	}
}

// TestContainerAcceptanceFailedStepPolicyGuards 验证独立候选注入不改变已接受的普通生产路径
func TestContainerAcceptanceFailedStepPolicyGuards(t *testing.T) {
	const image = "docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowFailedStepRetry: true, AllowedImages: []string{image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	for _, test := range []struct {
		name, enabled, required, allowedImage string
		active, configured                    bool
	}{
		{name: "missing-opt-in", required: "1", allowedImage: image},
		{name: "missing-mandatory", enabled: "1", allowedImage: image},
		{name: "wrong-image", enabled: "1", required: "1", allowedImage: "other"},
		{name: "active-process", enabled: "1", required: "1", allowedImage: image, active: true},
		{name: "already-configured", enabled: "1", required: "1", allowedImage: image, configured: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", test.enabled)
			t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", test.required)
			t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", test.allowedImage)
			e := &Process{}
			if test.active {
				e.runs.Store("active", struct{}{})
			}
			if test.configured {
				e.offlineRuntimePolicy = policy.Clone()
				e.offlineRuntimePolicy.AllowFailedStepRetry = false
			}
			before := e.offlineRuntimePolicy.Clone()
			if err := e.ConfigureContainerAcceptancePolicy(policy); err == nil {
				t.Fatal("out-of-scope failed-step injection accepted")
			}
			if !reflect.DeepEqual(before, e.offlineRuntimePolicy) {
				t.Fatal("rejected injection changed current policy")
			}
		})
	}
	t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", image)
	e := &Process{}
	if taskqueue.FailedStepRuntimeAccepted {
		if e.ConfigureContainerAcceptancePolicy(policy) == nil {
			t.Fatal("accepted runtime allowed obsolete candidate policy injection")
		}
		return
	}
	if err := e.ConfigureContainerAcceptancePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.offlineRuntimePolicy, policy) || e.offlineRuntimePolicy.ValidateProduction() == nil {
		t.Fatal("candidate injection changed policy or opened production gate")
	}
	policy.AllowedImages[0] = "changed"
	if e.offlineRuntimePolicy.AllowedImages[0] != image || e.ConfigureContainerAcceptancePolicy(e.offlineRuntimePolicy.Clone()) == nil {
		t.Fatal("candidate policy was mutable or injected twice")
	}
}

// TestContainerAcceptanceFailedStepApprovalGuards 验证审批组合候选只能在专用验收中单次注入
func TestContainerAcceptanceFailedStepApprovalGuards(t *testing.T) {
	const image = "fixture/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowFailedStepRetry: true, AllowFailedStepRetryApproval: true, AllowedImages: []string{image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	for _, tc := range []struct {
		name                        string
		mutate                      func(*taskqueue.OfflineRuntimePolicy)
		enabled, required           string
		active, stopped, configured bool
	}{
		{name: "missing-opt-in", required: "1"},
		{name: "missing-mandatory", enabled: "1"},
		{name: "disabled", enabled: "1", required: "1", mutate: func(p *taskqueue.OfflineRuntimePolicy) { p.Enabled = false }},
		{name: "missing-base-step-permission", enabled: "1", required: "1", mutate: func(p *taskqueue.OfflineRuntimePolicy) { p.AllowFailedStepRetry = false }},
		{name: "wrong-image", enabled: "1", required: "1", mutate: func(p *taskqueue.OfflineRuntimePolicy) {
			p.AllowedImages = []string{"other@sha256:" + strings.Repeat("b", 64)}
		}},
		{name: "multiple-images", enabled: "1", required: "1", mutate: func(p *taskqueue.OfflineRuntimePolicy) { p.AllowedImages = append(p.AllowedImages, image) }},
		{name: "active", enabled: "1", required: "1", active: true},
		{name: "stopped", enabled: "1", required: "1", stopped: true},
		{name: "already-configured", enabled: "1", required: "1", configured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", tc.enabled)
			t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", tc.required)
			t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", image)
			p := policy.Clone()
			if tc.mutate != nil {
				tc.mutate(&p)
			}
			e := &Process{stopped: tc.stopped}
			if tc.active {
				e.runs.Store("active", struct{}{})
			}
			if tc.configured {
				e.offlineRuntimePolicy = policy.Clone()
			}
			before := e.offlineRuntimePolicy.Clone()
			if e.ConfigureContainerAcceptancePolicy(p) == nil {
				t.Fatal("out-of-scope approval injection accepted")
			}
			if !reflect.DeepEqual(before, e.offlineRuntimePolicy) {
				t.Fatal("rejected candidate mutated policy")
			}
		})
	}
	t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_SANDBOX_TEST_IMAGE", image)
	e := &Process{}
	if taskqueue.FailedStepRetryApprovalAccepted {
		if e.ConfigureContainerAcceptancePolicy(policy) == nil {
			t.Fatal("accepted feature retained candidate injection")
		}
		return
	}
	if err := e.ConfigureContainerAcceptancePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.offlineRuntimePolicy, policy) || e.offlineRuntimePolicy.ValidateProduction() == nil {
		t.Fatal("candidate changed policy or opened production")
	}
	policy.AllowedImages[0] = "changed"
	if e.offlineRuntimePolicy.AllowedImages[0] != image || e.ConfigureContainerAcceptancePolicy(e.offlineRuntimePolicy.Clone()) == nil {
		t.Fatal("mutable or repeated candidate injection")
	}
}
