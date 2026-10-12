package deployment

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureDeployment() *kubeDeployment {
	d := &kubeDeployment{}
	d.Metadata.UID = "uid-one"
	d.Metadata.Generation = 3
	d.Metadata.ResourceVersion = "42"
	replicas := int32(2)
	d.Spec.Replicas = &replicas
	d.Spec.Template.Metadata.Annotations = map[string]string{releaseAnnotation: "release"}
	d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}{"app", "registry.example/team/app@sha256:" + strings.Repeat("a", 64)})
	d.Status.ObservedGeneration = 3
	d.Status.Replicas = 2
	d.Status.UpdatedReplicas = 2
	d.Status.AvailableReplicas = 2
	return d
}
func adapterFixture(t *testing.T, handler func(*http.Request) *kubeDeployment) *KubernetesAdapter {
	t.Helper()
	a, err := NewKubernetesAdapter("https://cluster.invalid", &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		d := handler(r)
		body, _ := sonic.Marshal(d)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestKubernetesObservationRequiresExactCurrentRollout(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*kubeDeployment)
	}{
		{"complete", "succeeded", func(*kubeDeployment) {}},
		{"old observed generation", "observing", func(d *kubeDeployment) { d.Status.ObservedGeneration = 2 }},
		{"old replicas remain", "observing", func(d *kubeDeployment) { d.Status.Replicas = 3 }},
		{"not available", "observing", func(d *kubeDeployment) { d.Status.AvailableReplicas = 1 }},
		{"not updated", "observing", func(d *kubeDeployment) { d.Status.UpdatedReplicas = 1 }},
		{"paused", "observing", func(d *kubeDeployment) { d.Spec.Paused = true }},
		{"scaled zero", "observing", func(d *kubeDeployment) {
			zero := int32(0)
			d.Spec.Replicas = &zero
			d.Status.Replicas = 0
			d.Status.UpdatedReplicas = 0
			d.Status.AvailableReplicas = 0
		}},
		{"UID replacement", "superseded", func(d *kubeDeployment) { d.Metadata.UID = "replacement" }},
		{"new generation", "superseded", func(d *kubeDeployment) { d.Metadata.Generation = 4 }},
		{"wrong image", "superseded", func(d *kubeDeployment) { d.Spec.Template.Spec.Containers[0].Image = "other:latest" }},
		{"wrong owner", "superseded", func(d *kubeDeployment) { d.Spec.Template.Metadata.Annotations[releaseAnnotation] = "other" }},
		{"deadline", "failed", func(d *kubeDeployment) {
			d.Status.Conditions = append(d.Status.Conditions, struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			}{"Progressing", "False", "ProgressDeadlineExceeded"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureDeployment()
			tc.mutate(d)
			a := adapterFixture(t, func(r *http.Request) *kubeDeployment {
				if r.Method != "GET" {
					t.Fatal("observation wrote cluster")
				}
				return d
			})
			result, err := a.Observe(context.Background(), Target{"default", "web", "app"}, "release", "registry.example/team/app@sha256:"+strings.Repeat("a", 64), Receipt{"uid-one", 3})
			if err != nil || result.Status != tc.want {
				t.Fatalf("got %+v %v", result, err)
			}
		})
	}
}
func TestKubernetesApplyConditionalAndCrashReplay(t *testing.T) {
	d := fixtureDeployment()
	d.Metadata.Generation = 2
	delete(d.Spec.Template.Metadata.Annotations, releaseAnnotation)
	patches := 0
	a := adapterFixture(t, func(r *http.Request) *kubeDeployment {
		if r.URL.Path != "/apis/apps/v1/namespaces/default/deployments/web" {
			t.Fatalf("unexpected resource %s", r.URL.Path)
		}
		if r.Method == "PATCH" {
			patches++
			if r.Header.Get("Content-Type") != "application/json-patch+json" {
				t.Fatal("wrong patch type")
			}
			raw, _ := io.ReadAll(r.Body)
			var ops []map[string]any
			if err := sonic.Unmarshal(raw, &ops); err != nil {
				t.Fatal(err)
			}
			for i, path := range []string{"/metadata/uid", "/metadata/resourceVersion", "/metadata/generation", "/spec/template/spec/containers/0/name"} {
				if ops[i]["op"] != "test" || ops[i]["path"] != path {
					t.Fatalf("missing precondition %+v", ops)
				}
			}
			if len(ops) != 6 {
				t.Fatalf("unexpected writes %+v", ops)
			}
			d.Metadata.Generation = 3
			d.Spec.Template.Metadata.Annotations[releaseAnnotation] = "release"
		}
		return d
	})
	target := Target{"default", "web", "app"}
	baseline, err := a.Inspect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		receipt, err := a.Apply(context.Background(), target, "release", d.Spec.Template.Spec.Containers[0].Image, baseline)
		if err != nil || receipt.Generation != 3 {
			t.Fatalf("replay %+v %v", receipt, err)
		}
	}
	if patches != 1 {
		t.Fatalf("duplicate patch count %d", patches)
	}
	d.Metadata.UID = "replacement"
	if _, err := a.Apply(context.Background(), target, "release", d.Spec.Template.Spec.Containers[0].Image, baseline); err != ErrSuperseded {
		t.Fatalf("replacement accepted %v", err)
	}
	if patches != 1 {
		t.Fatal("replaced workload was modified")
	}
}
func TestKubernetesValidationAndRedirection(t *testing.T) {
	for _, image := range []string{"app:latest", "app@sha256:abc", "https://x/app@sha256:" + strings.Repeat("a", 64), "a\nAuthorization: token"} {
		if ValidImage(image) {
			t.Fatalf("invalid image accepted %s", image)
		}
	}
	for _, target := range []Target{{"../default", "web", "app"}, {"default", "../web", "app"}, {"default", "web", "app/x"}} {
		if ValidTarget(target) {
			t.Fatal("invalid target")
		}
	}
	for _, endpoint := range []string{"http://cluster.invalid", "https://u:p@cluster.invalid", "https://cluster.invalid/path", "https://cluster.invalid?x=1"} {
		if _, err := NewKubernetesAdapter(endpoint, &http.Client{}); err == nil {
			t.Fatal("unsafe endpoint")
		}
	}
	calls := 0
	a, _ := NewKubernetesAdapter("https://cluster.invalid", &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://another.invalid"}}, Body: io.NopCloser(strings.NewReader("secret"))}, nil
	})})
	if _, err := a.Inspect(context.Background(), Target{"default", "web", "app"}); err != ErrUnavailable || calls != 1 {
		t.Fatalf("redirect followed %v %d", err, calls)
	}
}
func TestConfiguredAdapterLoadsOnlyExistingFiles(t *testing.T) {
	config := TargetConfig{Server: "https://cluster.invalid", Namespace: "default", Deployment: "web", Container: "app", BearerTokenFile: filepath.Join(t.TempDir(), "token")}
	if err := os.WriteFile(config.BearerTokenFile, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewConfiguredAdapter(context.Background(), config)
	if err != nil || adapter == nil {
		t.Fatalf("config %v", err)
	}
	config.Server = "http://private-secret.invalid"
	if _, err := NewConfiguredAdapter(context.Background(), config); err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("unsafe error %v", err)
	}
	config.Server = "https://cluster.invalid"
	config.CAFile = filepath.Join(t.TempDir(), "ca")
	_ = os.WriteFile(config.CAFile, []byte("not a certificate"), 0600)
	if _, err := NewConfiguredAdapter(context.Background(), config); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
