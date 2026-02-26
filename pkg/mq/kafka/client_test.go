package kafka

import "testing"

func TestBuildBaseConfig_Required(t *testing.T) {
	if _, err := buildBaseConfig(Config{}); err == nil {
		t.Fatal("expected error when bootstrapServers is empty")
	}
}

func TestBuildBaseConfig_WithAuth(t *testing.T) {
	cfg := Config{
		BootstrapServers: "localhost:9092",
		SecurityProtocol: "SASL_SSL",
		Sasl: SaslConfig{
			Mechanism: "PLAIN",
			Username:  "user",
			Password:  "pass",
		},
		Ssl: SslConfig{
			CaFile:   "ca.pem",
			CertFile: "cert.pem",
			KeyFile:  "key.pem",
			Password: "secret",
		},
	}

	config, err := buildBaseConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, err := config.Get("bootstrap.servers", nil); err != nil || got != "localhost:9092" {
		t.Fatalf("expected bootstrap.servers to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("security.protocol", nil); err != nil || got != "SASL_SSL" {
		t.Fatalf("expected security.protocol to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("sasl.mechanism", nil); err != nil || got != "PLAIN" {
		t.Fatalf("expected sasl.mechanism to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("sasl.username", nil); err != nil || got != "user" {
		t.Fatalf("expected sasl.username to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("sasl.password", nil); err != nil || got != "pass" {
		t.Fatalf("expected sasl.password to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("ssl.ca.location", nil); err != nil || got != "ca.pem" {
		t.Fatalf("expected ssl.ca.location to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("ssl.certificate.location", nil); err != nil || got != "cert.pem" {
		t.Fatalf("expected ssl.certificate.location to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("ssl.key.location", nil); err != nil || got != "key.pem" {
		t.Fatalf("expected ssl.key.location to be set, got %v (err=%v)", got, err)
	}
	if got, err := config.Get("ssl.key.password", nil); err != nil || got != "secret" {
		t.Fatalf("expected ssl.key.password to be set, got %v (err=%v)", got, err)
	}
}
