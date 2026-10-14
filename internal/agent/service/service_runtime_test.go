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
