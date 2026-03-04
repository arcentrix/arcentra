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

package scm

import "testing"

func TestWebhookRequest_Header_CaseInsensitive(t *testing.T) {
	req := WebhookRequest{
		Headers: map[string]string{
			"x-test": "v1",
		},
		Body: []byte("x"),
	}
	if got := req.Header("X-Test"); got != "v1" {
		t.Fatalf("expected v1, got %q", got)
	}
	if got := req.Header("x-test"); got != "v1" {
		t.Fatalf("expected v1, got %q", got)
	}
	if got := req.Header("x-missing"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
