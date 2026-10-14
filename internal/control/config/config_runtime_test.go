package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineRuntimeConfigRequiresExplicitBoundedPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[pipeline.offlineRuntime]\nenabled = true\nallowedImages = ['registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa']\nmaxCpu = '1'\nmaxMemory = '128Mi'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil || !cfg.Pipeline.OfflineRuntime.Enabled {
		t.Fatalf("explicit accepted policy rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("[pipeline.offlineRuntime]\nenabled = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("enabled policy without image/resource limits accepted")
	}
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfigFile(path)
	if err != nil || cfg.Pipeline.OfflineRuntime.Enabled {
		t.Fatalf("default policy unexpectedly enabled: %v", err)
	}
}
