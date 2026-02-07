package kafka

import "testing"

func TestProducerOptionsApply(t *testing.T) {
	cfg := ProducerConfig{}
	WithProducerClientOptions(
		WithClientId("client-1"),
		WithSecurityProtocol("SASL_SSL"),
	).apply(&cfg)
	WithProducerAcks("1").apply(&cfg)
	WithProducerRetries(5).apply(&cfg)
	WithProducerCompression("gzip").apply(&cfg)

	if cfg.ClientId != "client-1" {
		t.Fatalf("expected ClientId to be set, got %s", cfg.ClientId)
	}
	if cfg.SecurityProtocol != "SASL_SSL" {
		t.Fatalf("expected SecurityProtocol to be set, got %s", cfg.SecurityProtocol)
	}
	if cfg.Acks != "1" {
		t.Fatalf("expected Acks to be set, got %s", cfg.Acks)
	}
	if cfg.Retries != 5 {
		t.Fatalf("expected Retries to be set, got %d", cfg.Retries)
	}
	if cfg.Compression != "gzip" {
		t.Fatalf("expected Compression to be set, got %s", cfg.Compression)
	}
}

func TestNormalizeProducerConfig_Defaults(t *testing.T) {
	cfg := ProducerConfig{}
	normalizeProducerConfig(&cfg)

	if cfg.Acks != "all" {
		t.Fatalf("expected default Acks to be all, got %s", cfg.Acks)
	}
	if cfg.Retries != 3 {
		t.Fatalf("expected default Retries to be 3, got %d", cfg.Retries)
	}
	if cfg.Compression != "snappy" {
		t.Fatalf("expected default Compression to be snappy, got %s", cfg.Compression)
	}
}

func TestConsumerOptionsApply(t *testing.T) {
	cfg := ConsumerConfig{}
	WithConsumerClientOptions(
		WithClientId("client-2"),
		WithSecurityProtocol("PLAINTEXT"),
	).apply(&cfg)
	WithConsumerAutoOffsetReset("latest").apply(&cfg)
	WithConsumerEnableAutoCommit(false).apply(&cfg)
	WithConsumerSessionTimeoutMs(15000).apply(&cfg)
	WithConsumerMaxPollIntervalMs(600000).apply(&cfg)

	if cfg.ClientId != "client-2" {
		t.Fatalf("expected ClientId to be set, got %s", cfg.ClientId)
	}
	if cfg.SecurityProtocol != "PLAINTEXT" {
		t.Fatalf("expected SecurityProtocol to be set, got %s", cfg.SecurityProtocol)
	}
	if cfg.AutoOffsetReset != "latest" {
		t.Fatalf("expected AutoOffsetReset to be set, got %s", cfg.AutoOffsetReset)
	}
	if cfg.EnableAutoCommit == nil || *cfg.EnableAutoCommit != false {
		t.Fatalf("expected EnableAutoCommit to be false, got %v", cfg.EnableAutoCommit)
	}
	if cfg.SessionTimeoutMs != 15000 {
		t.Fatalf("expected SessionTimeoutMs to be set, got %d", cfg.SessionTimeoutMs)
	}
	if cfg.MaxPollIntervalMs != 600000 {
		t.Fatalf("expected MaxPollIntervalMs to be set, got %d", cfg.MaxPollIntervalMs)
	}
}

func TestNormalizeConsumerConfig_Defaults(t *testing.T) {
	cfg := ConsumerConfig{}
	normalizeConsumerConfig(&cfg)

	if cfg.AutoOffsetReset != "earliest" {
		t.Fatalf("expected default AutoOffsetReset to be earliest, got %s", cfg.AutoOffsetReset)
	}
	if cfg.SessionTimeoutMs != 10000 {
		t.Fatalf("expected default SessionTimeoutMs to be 10000, got %d", cfg.SessionTimeoutMs)
	}
	if cfg.MaxPollIntervalMs != 300000 {
		t.Fatalf("expected default MaxPollIntervalMs to be 300000, got %d", cfg.MaxPollIntervalMs)
	}
}
