package config

import (
	"testing"

	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestProvideOfflineRuntimePolicyDefaultsAndStartupSnapshot(t *testing.T) {
	for _, conf := range []*AppConfig{nil, {}} {
		policy, err := ProvideOfflineRuntimePolicy(conf)
		if err != nil || policy.Enabled {
			t.Fatalf("default runtime enabled: %+v %v", policy, err)
		}
	}
	const image = "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	conf := &AppConfig{Pipeline: PipelineConfig{OfflineRuntime: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}}}
	policy, err := ProvideOfflineRuntimePolicy(conf)
	if err != nil {
		t.Fatal(err)
	}
	conf.Pipeline.OfflineRuntime.AllowedImages[0] = "changed"
	conf.Pipeline.OfflineRuntime.MaxCPU = "999"
	conf.Pipeline.OfflineRuntime.Enabled = false
	if !policy.Enabled || policy.AllowedImages[0] != image || policy.MaxCPU != "1" {
		t.Fatalf("startup policy aliases source config: %+v", policy)
	}
	policy.AllowedImages[0] = "returned-copy"
	if conf.Pipeline.OfflineRuntime.AllowedImages[0] != "changed" {
		t.Fatal("returned policy mutates startup config")
	}
}

func TestProvideOfflineRuntimePolicyRejectsInvalidEnabledConfig(t *testing.T) {
	conf := &AppConfig{Pipeline: PipelineConfig{OfflineRuntime: taskqueue.OfflineRuntimePolicy{Enabled: true}}}
	policy, err := ProvideOfflineRuntimePolicy(conf)
	if err == nil || policy.Enabled || len(policy.AllowedImages) != 0 {
		t.Fatalf("invalid policy survived startup: %+v %v", policy, err)
	}
}
