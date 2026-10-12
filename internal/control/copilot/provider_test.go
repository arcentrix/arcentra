package copilot

import (
	"context"
	"errors"
	"fmt"
	"github.com/bytedance/sonic"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderAdapters(t *testing.T) {
	for _, kind := range []string{"openai-compatible", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				if err := sonic.Unmarshal(raw, &body); err != nil {
					t.Error(err)
				}
				if body["model"] != "test-model" || body["stream"] != false || body["tools"] != nil {
					t.Errorf("unsafe request %s", raw)
				}
				if kind == "anthropic" {
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fake-key" || r.Header.Get("anthropic-version") != "2023-06-01" || body["max_tokens"] != float64(2048) {
						t.Errorf("wrong anthropic request %s", raw)
					}
					io.WriteString(w, `{"content":[{"type":"text","text":"analysis"}],"stop_reason":"end_turn"}`)
				} else {
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fake-key" || body["max_completion_tokens"] != float64(2048) || body["store"] != false {
						t.Errorf("wrong openai request %s", raw)
					}
					io.WriteString(w, `{"choices":[{"message":{"content":"analysis"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			t.Setenv("COPILOT_FAKE_KEY", "fake-key")
			p, _, _, err := newHTTPProvider(ProviderConfig{ID: "test", Kind: kind, BaseURL: server.URL + "/v1", APIKeyEnv: "COPILOT_FAKE_KEY", Models: []string{"test-model"}})
			if err != nil {
				t.Fatal(err)
			}
			p.client.Transport = server.Client().Transport
			out, err := p.Analyze(context.Background(), Input{Model: "test-model", System: "read only", User: "test", MaxOutputTokens: 2048})
			if err != nil || out.Text != "analysis" || calls != 1 {
				t.Fatalf("%+v %v calls %d", out, err, calls)
			}
		})
	}
}
func TestProviderFailsClosedAndDoesNotLeak(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"auth", "upstream secret-debug-body", 401, ErrProvider}, {"rate", "credential", 429, ErrRateLimited}, {"invalid", "not json", 200, ErrInvalidResponse}, {"tool", `{"choices":[{"message":{"content":"run this","tool_calls":[{}]},"finish_reason":"tool_calls"}]}`, 200, ErrInvalidResponse}, {"oversized", strings.Repeat("x", 512*1024+1), 200, ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			p := &httpProvider{kind: "openai-compatible", endpoint: server.URL, client: server.Client(), tokenField: "max_completion_tokens"}
			_, err := p.Analyze(context.Background(), Input{User: "test", MaxOutputTokens: 10})
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "credential") {
				t.Fatal("provider detail leaked")
			}
		})
	}
}
func TestProviderCancellationAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	p := &httpProvider{kind: "openai-compatible", endpoint: server.URL, client: server.Client(), tokenField: "max_completion_tokens"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := p.Analyze(ctx, Input{User: "test", MaxOutputTokens: 10})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KEY", "fake")
	p, _, _, err = newHTTPProvider(ProviderConfig{ID: "one", Kind: "openai-compatible", BaseURL: "https://example.invalid/v1", APIKeyEnv: "FAKE_KEY", Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("redirect allowed")
	}
}
func TestRegistryRequiresExplicitProjectAndValidConfig(t *testing.T) {
	t.Setenv("FAKE_KEY", "fake")
	good := ProviderConfig{ID: "one", Kind: "openai-compatible", BaseURL: "https://example.invalid/v1", APIKeyEnv: "FAKE_KEY", Models: []string{"m"}}
	for _, url := range []string{"http://example.test/v1", "https://user:password@example.test/v1", "https://example.test/v1?key=x", "https://example.test/v1#key"} {
		bad := good
		bad.BaseURL = url
		if _, _, _, err := newHTTPProvider(bad); err == nil {
			t.Fatal(url)
		}
	}
	r := NewRegistry(Config{Enabled: true, AllowedProjectIDs: []string{"a"}, Providers: []ProviderConfig{good}})
	if len(r.Options("a")) != 1 || len(r.Options("b")) != 0 {
		t.Fatal("project allowlist failed")
	}
	if _, err := r.Resolve("a", "one", "unlisted"); err == nil {
		t.Fatal("unlisted model")
	}
	if len(NewRegistry(Config{}).Options("a")) != 0 {
		t.Fatal("enabled by default")
	}
	data, _ := sonic.Marshal(r.Options("a"))
	if strings.Contains(string(data), "example.invalid") || strings.Contains(string(data), "fake") {
		t.Fatal("configuration leaked")
	}
}
func TestReadOnlyRequestsOwnerIsolationAndTombstone(t *testing.T) {
	r := &Requests{}
	ctx, done, err := r.Begin(context.Background(), "a", "p", "request")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err := r.Cancel("b", "p", "request"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("other user cancelled request")
	}
	if err := r.Cancel("a", "p", "request"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("request not cancelled")
	}
	if _, _, err := r.Begin(context.Background(), "b", "p", "request"); err == nil {
		t.Fatal("cancel-before-begin was ignored")
	}
}
func TestRedactionAndPathConfinement(t *testing.T) {
	secret := "long-registered-value"
	r := NewRedactor([]string{secret})
	raw := "safe\n" + secret + "\nPASSWORD=other-secret\nAuthorization: Bearer bearer-secret\n-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\nhttps://user:pass@example.test\nsk-abcdefghijklmnopqrst\n"
	result := r.Redact(raw)
	for _, value := range []string{secret, "other-secret", "bearer-secret", "\nkey\n", "user:pass", "sk-abcdefghijklmnopqrst"} {
		if strings.Contains(result, value) {
			t.Errorf("leaked %q: %s", value, result)
		}
	}
	for _, name := range []string{"../main.go", "/main.go", "src/../main.go", ".env", "src/.git/config", "private-key.txt", "secrets.yaml", "kubeconfig", "code.zip", "a%2fb.go", "src\\main.go"} {
		if CodeArtifactName(name) {
			t.Error(name)
		}
	}
	if !CodeArtifactName("src/main.go") || !CodeArtifactName("Dockerfile") {
		t.Fatal("source name denied")
	}
	if got := Prefix("你好世界", 5); got != "你" {
		t.Fatal(got)
	}
}

func TestSourcePreviewCapacityAndDeadline(t *testing.T) {
	var previews Previews
	finishes := []func(){}
	for i := 0; i < 16; i++ {
		ctx, finish, err := previews.Begin(context.Background(), fmt.Sprintf("user-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		finishes = append(finishes, finish)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 15*time.Second {
			t.Fatal("preview deadline missing")
		}
	}
	if _, _, err := previews.Begin(context.Background(), "extra"); !errors.Is(err, ErrBusy) {
		t.Fatal("global preview cap missing", err)
	}
	for _, finish := range finishes {
		finish()
		finish()
	}
	_, finish, err := previews.Begin(context.Background(), "reused")
	if err != nil {
		t.Fatal("preview capacity did not release", err)
	}
	finish()
}
