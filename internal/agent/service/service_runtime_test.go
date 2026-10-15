package service

import (
	"github.com/arcentrix/arcentra/internal/agent/config"
	"testing"
)

func TestOfflineRuntimeCapabilityRequiresActualReadinessProbe(t *testing.T) {
	service := &AgentServiceImpl{agentConf: &config.AgentConfig{Agent: config.AgentInfo{Mode: "sandbox"}}}
	if service.runtimeCapabilitiesLocked().ContainerdOfflineV1 || service.runtimeCapabilitiesLocked().ExecutionMode != "containerd-offline-v1" {
		t.Fatal("new sandbox Agent must advertise mode without readiness")
	}
	ready := false
	service.SetOfflineRuntimeReadiness(func() bool { return ready })
	if service.runtimeCapabilitiesLocked().ContainerdOfflineV1 {
		t.Fatal("failed runtime advertised capability")
	}
	ready = true
	if !service.runtimeCapabilitiesLocked().ContainerdOfflineV1 {
		t.Fatal("ready runtime capability missing")
	}
	ready = false
	if service.runtimeCapabilitiesLocked().ContainerdOfflineV1 {
		t.Fatal("revoked readiness cached")
	}
	service.SetOfflineRuntimeReadiness(nil)
	if service.runtimeCapabilitiesLocked().ContainerdOfflineV1 {
		t.Fatal("nil probe advertises runtime")
	}
}

func TestWholeJobRetryCapabilityRequiresReadinessAndOperatorOptIn(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "sandbox"}}
	service := NewAgentServiceImpl(conf, nil, nil)
	ready := true
	service.SetOfflineRuntimeReadiness(func() bool { return ready })
	if service.runtimeCapabilitiesLocked().ContainerdWholeJobRetryV1 {
		t.Fatal("default config advertised retry")
	}
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry = true
	if !service.runtimeCapabilitiesLocked().ContainerdWholeJobRetryV1 {
		t.Fatal("ready opt-in retry capability missing")
	}
	ready = false
	if service.runtimeCapabilitiesLocked().ContainerdWholeJobRetryV1 {
		t.Fatal("failed runtime retained retry capability")
	}
}

func TestFailedStepCapabilityRequiresOperatorAndBothReadinessProbes(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "sandbox"}}
	service := NewAgentServiceImpl(conf, nil, nil)
	offlineReady, stepReady := true, true
	service.SetOfflineRuntimeReadiness(func() bool { return offlineReady })
	service.SetFailedStepRetryReadiness(func() bool { return stepReady })
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("default-off policy advertised failed-step retry")
	}
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetry = true
	if !service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("accepted opt-in ready implementation did not advertise failed-step retry")
	}
	stepReady = false
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("revoked step readiness remained advertised")
	}
	stepReady, offlineReady = true, false
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("revoked runtime readiness remained advertised")
	}
	offlineReady = true
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetry = false
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("removed operator opt-in remained advertised")
	}
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetry = true
	service.SetFailedStepRetryReadiness(nil)
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("missing live adapter advertised retry")
	}
	service.SetFailedStepRetryReadiness(func() bool { return true })
	conf.Agent.Mode = "baremetal"
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryV1 {
		t.Fatal("baremetal mode advertised live container retry")
	}
}

func TestFailedStepApprovalCapabilityRemainsBehindIndependentAcceptance(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "sandbox"}}
	service := NewAgentServiceImpl(conf, nil, nil)
	service.SetOfflineRuntimeReadiness(func() bool { return true })
	service.SetFailedStepRetryReadiness(func() bool { return true })
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry = true
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetry = true
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryApprovalV1 {
		t.Fatal("conjunction of old flags advertised new capability")
	}
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetryApproval = true
	if service.runtimeCapabilitiesLocked().ContainerdFailedStepRetryApprovalV1 {
		t.Fatal("unaccepted combination advertised")
	}
}
