// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kafka

import "testing"

func TestProducerOptionsApply(t *testing.T) {
	cfg := ProducerConfig{}
	WithProducerOptions(
		WithSecurityProtocol("SASL_SSL"),
	)(&cfg)
	WithProducerAcks("1")(&cfg)
	WithProducerRetries(5)(&cfg)
	WithProducerCompression("gzip")(&cfg)

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
	cfg.Normalize()

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
	WithConsumerOptions(
		WithSecurityProtocol("PLAINTEXT"),
	)(&cfg)
	WithConsumerAutoOffsetReset("latest")(&cfg)
	WithConsumerEnableAutoCommit(false)(&cfg)
	WithConsumerSessionTimeoutMs(15000)(&cfg)
	WithConsumerMaxPollIntervalMs(600000)(&cfg)

	if cfg.SecurityProtocol != "PLAINTEXT" {
		t.Fatalf("expected SecurityProtocol to be set, got %s", cfg.SecurityProtocol)
	}
	if cfg.AutoOffsetReset != "latest" {
		t.Fatalf("expected AutoOffsetReset to be set, got %s", cfg.AutoOffsetReset)
	}
	if cfg.EnableAutoCommit == nil || *cfg.EnableAutoCommit {
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
	cfg.Normalize()

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
