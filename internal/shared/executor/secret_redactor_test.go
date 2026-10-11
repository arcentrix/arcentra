package executor

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/go-resty/resty/v2"
	"strings"
	"testing"
)

func TestStreamingSecretMaskAcrossEverySplit(t *testing.T) {
	secret := "private-secret-value"
	for split := 0; split <= len(secret); split++ {
		r := newSecretRedactor(map[string]string{"secrets.password": secret})
		got := r.chunk("stdout", "prefix "+secret[:split], false) + r.chunk("stdout", secret[split:]+" suffix", false) + r.chunk("stdout", "", true)
		if got != "prefix *** suffix" {
			t.Fatalf("split %d: %q", split, got)
		}
	}
}

func TestManagerMasksStreamCaptureAndErrors(t *testing.T) {
	manager := NewExecutorManager()
	var observed strings.Builder
	failure := errors.New("password=sensitive-123")
	manager.Register(&mockExecutor{name: "test", canExecute: func(*ExecutionRequest) bool { return true }, executeFunc: func(_ context.Context, r *ExecutionRequest) (*ExecutionResult, error) {
		r.OutputHandler("stdout", "sensitive-")
		r.OutputHandler("stdout", "123\n")
		return &ExecutionResult{Output: "sensitive-123", ErrorOutput: "sensitive-123", Error: "sensitive-123"}, failure
	}})
	result, err := manager.Execute(context.Background(), &ExecutionRequest{Step: &StepInfo{Name: "test"}, Env: map[string]string{"secrets.password": "sensitive-123"}, OutputHandler: func(_, text string) { observed.WriteString(text) }})
	if !errors.Is(err, failure) {
		t.Fatal("redaction lost error identity")
	}
	for _, text := range []string{observed.String(), result.Output, result.ErrorOutput, result.Error, err.Error()} {
		if strings.Contains(text, "sensitive-123") {
			t.Fatalf("secret leaked: %q", text)
		}
	}
	if observed.String() != "***\n" {
		t.Fatalf("stream mismatch %q", observed.String())
	}
}

func TestHTTPExecutorOnlyTransmitsExplicitHeaders(t *testing.T) {
	executor := NewHTTPExecutor(log.Logger{})
	request := executor.buildHTTPRequest(resty.New().R(), &HTTPConfig{Headers: map[string]string{"X-Requested": "yes"}}, "GET", map[string]string{"secrets.token": "private", "PATH": "host"})
	if request.Header.Get("X-Requested") != "yes" {
		t.Fatal("explicit header removed")
	}
	for key := range request.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-env-") {
			t.Fatalf("environment leaked: %s", key)
		}
	}
}
