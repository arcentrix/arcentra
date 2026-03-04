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

package rocketmq

import (
	"testing"

	"github.com/apache/rocketmq-client-go/v2/primitive"
)

func TestNewRocketMQClient_Required(t *testing.T) {
	if _, err := NewRocketMQClient(nil); err == nil {
		t.Fatal("expected error when nameServers is empty")
	}
	if _, err := NewRocketMQClient([]string{}); err == nil {
		t.Fatal("expected error when nameServers is empty")
	}
}

func TestResolveCredentials(t *testing.T) {
	if _, err := resolveCredentials(ClientConfig{AccessKey: "only"}); err == nil {
		t.Fatal("expected error when only accessKey is set")
	}
	if _, err := resolveCredentials(ClientConfig{SecretKey: "only"}); err == nil {
		t.Fatal("expected error when only secretKey is set")
	}

	cred, err := resolveCredentials(ClientConfig{AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil || cred.AccessKey != "a" || cred.SecretKey != "b" {
		t.Fatalf("unexpected credentials: %+v", cred)
	}

	provided := &primitive.Credentials{AccessKey: "x", SecretKey: "y"}
	cred, err = resolveCredentials(ClientConfig{
		AccessKey:   "a",
		SecretKey:   "b",
		Credentials: provided,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred != provided {
		t.Fatal("expected provided credentials to take precedence")
	}
}
