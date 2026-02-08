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
