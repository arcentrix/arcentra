package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentConfigurationIsIndependentStartupSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	original := []byte("[grpc]\nserverAddr = 'synthetic-controller:9443'\ntoken = 'synthetic-agent:signature'\n[agent]\nid = 'synthetic-agent'\n[agent.labels]\nsource = 'initial'\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.IdentityPersistence.Enabled || first.Agent.IdentityPersistence.Directory != "" {
		t.Fatal("identity persistence not default-off")
	}
	if err := os.WriteFile(path, []byte("[grpc]\nserverAddr = 'other-controller:9443'\n[agent]\nid = ''\n[agent.labels]\nsource = 'changed'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if first == second || first.Agent.ID != "synthetic-agent" || first.Grpc.Token != "synthetic-agent:signature" || first.Agent.Labels["source"] != "initial" || first.Grpc.ServerAddr != "synthetic-controller:9443" {
		t.Fatal("configuration reload mutated existing runtime snapshot")
	}
	if second.Agent.ID != "" || second.Agent.Labels["source"] != "changed" {
		t.Fatal("new lifecycle did not read current configuration")
	}
	if _, err := NewConf(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("configuration failure did not return an error")
	}
}

func TestAgentIdentityPersistenceConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("[grpc]\nserverAddr = 'synthetic-controller:9443'\n[agent.identityPersistence]\nenabled = true\ndirectory = '/var/lib/synthetic/identity'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conf, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	if !conf.Agent.IdentityPersistence.Enabled || conf.Agent.IdentityPersistence.Directory != "/var/lib/synthetic/identity" {
		t.Fatal("persistence configuration not decoded")
	}
}
