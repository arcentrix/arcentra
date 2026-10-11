// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestProjectResponseOmitsSecrets(t *testing.T) {
	project := Project{ProjectID: "project", Credential: "private-token", WebhookSecret: "private-webhook"}
	encoded, err := sonic.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "webhookSecret") {
		t.Fatalf("project response exposes secrets: %s", encoded)
	}
	if project.Credential != "private-token" || project.WebhookSecret != "private-webhook" {
		t.Fatal("response redaction must preserve internal credentials")
	}
	var request CreateProjectReq
	if err := sonic.Unmarshal([]byte(`{"credential":"new-token","webhookSecret":"new-secret"}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Credential != "new-token" || request.WebhookSecret != "new-secret" {
		t.Fatal("write-only credentials must remain accepted in request DTOs")
	}
}
