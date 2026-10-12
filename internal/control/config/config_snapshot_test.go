package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerConfigurationIsIndependentStartupSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.toml")
	original := []byte(`[grpc]
host = "127.0.0.1"
port = 9443
reflection = false
[grpc.tls]
enabled = true
certFile = "/synthetic/controller.pem"
keyFile = "/synthetic/controller.key"
[copilot]
allowedProjectIDs = ["original"]
[[copilot.providers]]
id = "synthetic"
models = ["original-model"]
`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Grpc.Port != 9443 || !first.Grpc.TLS.Enabled || first.Grpc.TLS.CertFile != "/synthetic/controller.pem" || first.Grpc.TLS.KeyFile != "/synthetic/controller.key" || first.Grpc.Reflection {
		t.Fatal("server TLS configuration was not decoded")
	}
	second, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	second.Copilot.AllowedProjectIDs[0] = "mutated"
	second.Copilot.Providers[0].Models[0] = "mutated-model"
	second.Grpc.TLS.Enabled = false
	if first == second || first.Copilot.AllowedProjectIDs[0] != "original" || first.Copilot.Providers[0].Models[0] != "original-model" || !first.Grpc.TLS.Enabled {
		t.Fatal("configuration snapshots share mutable runtime state")
	}
	if err := os.WriteFile(path, []byte("[grpc]\nport = 9090\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := NewConf(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !first.Grpc.TLS.Enabled || first.Grpc.Port != 9443 || third.Grpc.TLS.Enabled || third.Grpc.Port != 9090 {
		t.Fatal("configuration file change modified a running snapshot")
	}
	if len(third.Copilot.AllowedProjectIDs) != 0 {
		t.Fatal("new load retained omitted previous fields")
	}
}

func TestControllerConfigurationErrorsReturnWithoutPanic(t *testing.T) {
	if _, err := ProvideConf(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("[grpc\ninvalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConf(path); err == nil {
		t.Fatal("malformed file accepted")
	}
}
