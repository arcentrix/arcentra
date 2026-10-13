// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package deploymentcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func source(t *testing.T, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "../..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPublishedImageContainsOperatorCLIAndPersistentWorkingDirectory(t *testing.T) {
	text := source(t, "Dockerfile.ci")
	stage := strings.Split(strings.Split(text, "FROM debian:bookworm-slim AS arcentra\n")[1], "FROM debian:bookworm-slim AS arcentra-agent")[0]
	for _, required := range []string{"COPY dist/linux/${TARGETARCH}/arcentra-cli /usr/local/bin/arcentra-cli", "WORKDIR /var/lib/arcentra", `"-plugin-conf", "/conf.d/plugins.toml"`} {
		if !strings.Contains(stage, required) {
			t.Errorf("missing control image contract: %s", required)
		}
	}
	if strings.Contains(stage, "migrate") {
		t.Fatal("image entrypoint must not automatically migrate")
	}
}

func TestSystemdUnitsUseRealFlagsAndExplicitRestart(t *testing.T) {
	for _, name := range []string{"arcentra", "arcentra-agent"} {
		text := source(t, "deploy/"+name+".service")
		for _, required := range []string{"StateDirectory=" + name, "WorkingDirectory=/var/lib/" + name, "ReadWritePaths=/var/lib/" + name} {
			if !strings.Contains(text, required+"\n") {
				t.Errorf("%s missing %s", name, required)
			}
		}
		if strings.Contains(text, "ExecReload=") || strings.Contains(text, " -plugin ") {
			t.Errorf("%s has unsupported command", name)
		}
		if name == "arcentra" && !strings.Contains(text, "-plugin-conf /opt/arcentra/conf.d/plugins.toml") {
			t.Error("missing real plugin flag")
		}
	}
}

func TestDeploymentProbesAndServiceSelectors(t *testing.T) {
	for _, name := range []string{"arcentra", "arcentra-agent"} {
		t.Run(name, func(t *testing.T) {
			decoder := yaml.NewDecoder(strings.NewReader(source(t, "deploy/"+name+"-deployment.yaml")))
			var deployment struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Template struct {
						Metadata struct {
							Labels map[string]string `yaml:"labels"`
						} `yaml:"metadata"`
						Spec struct {
							Containers []struct {
								Startup struct {
									HTTP struct {
										Path string `yaml:"path"`
									} `yaml:"httpGet"`
									Period   int `yaml:"periodSeconds"`
									Failures int `yaml:"failureThreshold"`
								} `yaml:"startupProbe"`
								Liveness struct {
									HTTP struct {
										Path string `yaml:"path"`
									} `yaml:"httpGet"`
								} `yaml:"livenessProbe"`
								Readiness struct {
									HTTP struct {
										Path string `yaml:"path"`
									} `yaml:"httpGet"`
								} `yaml:"readinessProbe"`
							} `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			if err := decoder.Decode(&deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Kind != "Deployment" || len(deployment.Spec.Template.Spec.Containers) != 1 {
				t.Fatal("unexpected deployment shape")
			}
			container := deployment.Spec.Template.Spec.Containers[0]
			if container.Startup.HTTP.Path != "/live" || container.Startup.Period*container.Startup.Failures < 90 {
				t.Fatal("startup budget must exceed the initial sixty-second sync")
			}
			if container.Liveness.HTTP.Path != "/live" || container.Readiness.HTTP.Path != "/ready" {
				t.Fatal("liveness must not restart an unready process")
			}
			var service struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Selector map[string]string `yaml:"selector"`
				} `yaml:"spec"`
			}
			if err := decoder.Decode(&service); err != nil {
				t.Fatal(err)
			}
			if service.Kind != "Service" || service.Spec.Selector["app"] != name || deployment.Spec.Template.Metadata.Labels["app"] != name {
				t.Fatal("service must select actual pod labels")
			}
		})
	}
}

func TestRealAcceptanceCIHasNoProductionAuthorityOrSilentSkip(t *testing.T) {
	workflow := source(t, ".github/workflows/ci.yml")
	for _, forbidden := range []string{"pull_request_target", "secrets.", "contents: write", "packages: write", "id-token: write", "--privileged"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("acceptance workflow must not contain %s", forbidden)
		}
	}
	for _, required := range []string{`ARCENTRA_REAL_STACK: "1"`, `ARCENTRA_REQUIRE_REAL_STACK: "1"`, "'^TestRealStackAcceptance$'", "run_actual_app.py", "--browser-cwd", "down --volumes --remove-orphans", "retention-days: 7"} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("missing acceptance contract %s", required)
		}
	}
	// MinIO 社区分发仅提供源码，CI 只能编译官方固定提交且禁止拉取替代镜像
	for _, required := range []string{"repository: minio/minio", "ref: 9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a", "CGO_ENABLED=0 go build -mod=readonly -trimpath", "timeout-minutes: 12"} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("missing pinned source build contract %s", required)
		}
	}
	containerfile := source(t, "scripts/acceptance/minio.Dockerfile")
	for _, required := range []string{"FROM scratch", "COPY LICENSE NOTICE", "USER 65532:65532"} {
		if !strings.Contains(containerfile, required) {
			t.Fatalf("missing disposable image contract %s", required)
		}
	}
	if strings.Contains(containerfile, "RUN ") || strings.Contains(workflow, "docker push") {
		t.Fatal("test image must not download unpinned content or be published")
	}
	var compose struct {
		Services map[string]struct {
			Image       string   `yaml:"image"`
			PullPolicy  string   `yaml:"pull_policy"`
			ReadOnly    bool     `yaml:"read_only"`
			Ports       []string `yaml:"ports"`
			Privileged  bool     `yaml:"privileged"`
			NetworkMode string   `yaml:"network_mode"`
			Volumes     []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(source(t, "scripts/acceptance/compose.yml")), &compose); err != nil {
		t.Fatal(err)
	}
	if len(compose.Services) != 4 {
		t.Fatal("expected only four isolated dependency services")
	}
	minio := compose.Services["minio"]
	if minio.PullPolicy != "never" || !minio.ReadOnly || minio.Image != "arcentra-ci-minio:9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a" {
		t.Fatal("MinIO must use only the locally built pinned read-only test image")
	}
	for name, service := range compose.Services {
		if service.Image == "" || !strings.Contains(service.Image, ":") || strings.HasSuffix(service.Image, ":latest") || service.Privileged || service.NetworkMode != "" || len(service.Volumes) > 0 {
			t.Errorf("%s has unsafe or unpinned dependency configuration", name)
		}
		for _, port := range service.Ports {
			if !strings.HasPrefix(port, "127.0.0.1:") {
				t.Errorf("%s port not loopback-only", name)
			}
		}
	}
}
