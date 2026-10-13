package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineRuntimeConfigCannotEnableBeforeAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[pipeline.offlineRuntime]\nenabled = true\nallowedImages = ['registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa']\nmaxCpu = '1'\nmaxMemory = '128Mi'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("unaccepted runtime production configuration enabled")
	}
}
