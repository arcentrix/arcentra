// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package validation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func schemaRuntimeChainFixture() (*spec.Pipeline, taskqueue.OfflineRuntimePolicy) {
	pipeline, policy := schemaRuntimeFixture()
	for _, name := range []string{"test", "package"} {
		next, _ := schemaRuntimeFixture()
		job := next.Jobs[0]
		job.Name = name
		job.DependsOn = []string{pipeline.Jobs[len(pipeline.Jobs)-1].Name}
		pipeline.Jobs = append(pipeline.Jobs, job)
	}
	return pipeline, policy
}

// checkOfflineRuntimeValidation 同时覆盖保存时的 schema 校验和执行前的运行时解析
func checkOfflineRuntimeValidation(t *testing.T, pipeline *spec.Pipeline, policy taskqueue.OfflineRuntimePolicy, wantError string) {
	t.Helper()
	for _, check := range []struct {
		name string
		run  func(*testing.T) error
	}{
		{"schema", func(*testing.T) error { return NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline) }},
		{"resolve", func(t *testing.T) error {
			request, err := ResolveRuntime(pipeline, policy)
			if err == nil && (request == nil || request.Type != taskqueue.RuntimeContainerdOfflineV1) {
				t.Fatal("offline runtime silently resolved to host execution")
			}
			if err != nil && request != nil {
				t.Fatal("invalid offline runtime returned a dispatch request")
			}
			return err
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			err := check.run(t)
			if wantError == "" {
				if err != nil {
					t.Fatalf("valid offline runtime rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("expected error containing %q, got %v", wantError, err)
			}
		})
	}
}

func TestOfflineRuntimeAcceptsOnlyExplicitLinearChains(t *testing.T) {
	for _, entry := range []struct {
		name   string
		change func(*spec.Pipeline)
	}{
		{"single job compatibility", func(p *spec.Pipeline) { p.Jobs = p.Jobs[:1] }},
		{"two jobs", func(p *spec.Pipeline) { p.Jobs = p.Jobs[:2] }},
		{"three jobs", func(*spec.Pipeline) {}},
		{"declaration order is not execution order", func(p *spec.Pipeline) { p.Jobs[0], p.Jobs[2] = p.Jobs[2], p.Jobs[0] }},
		{"remote sources and agent selectors per job", func(p *spec.Pipeline) {
			for i, scheme := range []string{"https", "http", "ssh"} {
				p.Jobs[i].Source = &spec.Source{Type: "git", Repo: scheme + "://example.invalid/project.git"}
				p.Jobs[i].Steps[0].AgentSelector = &spec.AgentSelector{MatchLabels: map[string]string{"pool": p.Jobs[i].Name}}
			}
		}},
		{"artifact staging phases restart for each job", func(p *spec.Pipeline) {
			for i, job := range p.Jobs {
				if i > 0 {
					job.Steps = append([]*spec.Step{{Name: "download", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true,
						Args: spec.MapAsStruct(map[string]any{"job": p.Jobs[i-1].Name, "path": "out/result.txt"})}}, job.Steps...)
				}
				job.Steps = append(job.Steps, &spec.Step{Name: "upload", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true,
					Args: spec.MapAsStruct(map[string]any{"path": "out/result.txt"})})
			}
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			pipeline, policy := schemaRuntimeChainFixture()
			entry.change(pipeline)
			checkOfflineRuntimeValidation(t, pipeline, policy, "")
		})
	}
}

func TestOfflineRuntimeRejectsNonlinearOrMalformedChains(t *testing.T) {
	for _, entry := range []struct {
		name      string
		change    func(*spec.Pipeline)
		wantError string
	}{
		{"empty", func(p *spec.Pipeline) { p.Jobs = nil }, "at least one job"},
		{"nil root", func(p *spec.Pipeline) { p.Jobs[0] = nil }, "job[0] is nil"},
		{"nil middle", func(p *spec.Pipeline) { p.Jobs[1] = nil }, "job[1] is nil"},
		{"nil tail", func(p *spec.Pipeline) { p.Jobs[2] = nil }, "job[2] is nil"},
		{"duplicate names", func(p *spec.Pipeline) { p.Jobs[2].Name = p.Jobs[0].Name }, "duplicate job name"},
		{"blank name", func(p *spec.Pipeline) { p.Jobs[1].Name = " " }, "requires a name"},
		{"independent roots", func(p *spec.Pipeline) {
			for _, job := range p.Jobs {
				job.DependsOn = nil
			}
		}, "exactly one root job"},
		{"implicit list ordering", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = nil }, "exactly one root job"},
		{"fork", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{"build"} }, "multiple dependents"},
		{"join", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{"build", "test"} }, "exactly one explicit predecessor"},
		{"duplicate dependency", func(p *spec.Pipeline) { p.Jobs[1].DependsOn = []string{"build", "build"} }, "exactly one explicit predecessor"},
		{"unknown dependency", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{"missing"} }, "unknown job"},
		{"empty dependency", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{""} }, "unknown job"},
		{"exact dependency name", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{" test "} }, "unknown job"},
		{"whole chain cycle", func(p *spec.Pipeline) { p.Jobs[0].DependsOn = []string{"package"} }, "exactly one root job"},
		{"single job self cycle", func(p *spec.Pipeline) {
			p.Jobs = p.Jobs[:1]
			p.Jobs[0].DependsOn = []string{"build"}
		}, "exactly one root job"},
		{"detached self cycle", func(p *spec.Pipeline) { p.Jobs[2].DependsOn = []string{"package"} }, "one acyclic dependency chain"},
		{"detached two node cycle", func(p *spec.Pipeline) { p.Jobs[1].DependsOn = []string{"package"} }, "one acyclic dependency chain"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			pipeline, policy := schemaRuntimeChainFixture()
			entry.change(pipeline)
			checkOfflineRuntimeValidation(t, pipeline, policy, entry.wantError)
		})
	}
}

func TestOfflineRuntimeValidatesEveryJobInChain(t *testing.T) {
	for _, entry := range []struct {
		name   string
		change func(*spec.Job)
	}{
		{"host step", func(job *spec.Job) { job.Steps[0].RunOnAgent = false }},
		{"mixed host and agent steps", func(job *spec.Job) {
			job.Steps = append(job.Steps, &spec.Step{Name: "host", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "echo host"})})
		}},
		{"deployment target", func(job *spec.Job) { job.Target = &spec.Target{} }},
		{"retry", func(job *spec.Job) { job.Retry = &spec.Retry{} }},
		{"local source", func(job *spec.Job) { job.Source = &spec.Source{Type: "git", Repo: "/tmp/repo"} }},
		{"file source", func(job *spec.Job) { job.Source = &spec.Source{Type: "git", Repo: "file:///tmp/repo"} }},
		{"source without host", func(job *spec.Job) { job.Source = &spec.Source{Type: "git", Repo: "https:///repo"} }},
		{"malformed source", func(job *spec.Job) { job.Source = &spec.Source{Type: "git", Repo: "https://%invalid/repo"} }},
		{"non git source", func(job *spec.Job) { job.Source = &spec.Source{Type: "archive", Repo: "https://example.invalid/repo"} }},
		{"no steps", func(job *spec.Job) { job.Steps = nil }},
		{"nil step", func(job *spec.Job) { job.Steps = append(job.Steps, nil) }},
		{"http step", func(job *spec.Job) { job.Steps[0].Uses = "http"; job.Steps[0].Action = "request" }},
		{"native plugin", func(job *spec.Job) { job.Steps[0].Uses = "native-plugin@1.0.0" }},
		{"deployment step", func(job *spec.Job) { job.Steps[0].Uses = "builtin:kubernetes"; job.Steps[0].Action = "deploy" }},
		{"script action", func(job *spec.Job) { job.Steps[0].Action = "script" }},
		{"host shell argument", func(job *spec.Job) {
			job.Steps[0].Args = spec.MapAsStruct(map[string]any{"run": "echo ok", "workingDirectory": "/tmp"})
		}},
		{"download after shell", func(job *spec.Job) {
			job.Steps = append(job.Steps, &spec.Step{Name: "download", Uses: "builtin:artifacts", Action: "download", RunOnAgent: true})
		}},
		{"upload before shell", func(job *spec.Job) {
			job.Steps = append([]*spec.Step{{Name: "upload", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true}}, job.Steps...)
		}},
		{"shell after upload", func(job *spec.Job) {
			job.Steps = append(job.Steps, &spec.Step{Name: "upload", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true},
				&spec.Step{Name: "late-shell", Uses: "shell", RunOnAgent: true})
		}},
		{"artifact path escape", func(job *spec.Job) {
			job.Steps = append(job.Steps, &spec.Step{Name: "upload", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true,
				Args: spec.MapAsStruct(map[string]any{"path": "../outside"})})
		}},
	} {
		for index := range 3 {
			t.Run(fmt.Sprintf("%s/job-%d", entry.name, index), func(t *testing.T) {
				pipeline, policy := schemaRuntimeChainFixture()
				entry.change(pipeline.Jobs[index])
				checkOfflineRuntimeValidation(t, pipeline, policy, fmt.Sprintf("job %q", pipeline.Jobs[index].Name))
			})
		}
	}
}

func TestOfflineRuntimeChainRemainsDefaultDisabled(t *testing.T) {
	pipeline, _ := schemaRuntimeChainFixture()
	checkOfflineRuntimeValidation(t, pipeline, taskqueue.OfflineRuntimePolicy{}, "disabled")
	if err := NewSchemaValidator().Validate(pipeline); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("default constructor enabled offline chains: %v", err)
	}
}

func TestOfflineRuntimeChainSnapshotDoesNotAliasSpec(t *testing.T) {
	pipeline, policy := schemaRuntimeChainFixture()
	request, err := ResolveRuntime(pipeline, policy)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.Runtime.Image = "changed"
	pipeline.Runtime.Resources.CpuLimit = "999"
	pipeline.Runtime.Resources.MemoryLimit = "999Gi"
	if request.Image != schemaRuntimeImage || request.Resources.CPU != "500m" || request.Resources.Memory != "64Mi" || request.BindingDigest != "" {
		t.Fatalf("runtime snapshot aliases mutable spec or has a premature per-job binding: %+v", request)
	}
}

func TestOfflineRuntimeChainRestrictionDoesNotChangeLocalDAGs(t *testing.T) {
	for _, runtime := range []*spec.Runtime{nil, {Type: "local"}, {Type: "baremetal"}} {
		pipeline, policy := schemaRuntimeChainFixture()
		pipeline.Runtime = runtime
		pipeline.Jobs[2].DependsOn = []string{"build"}
		for _, job := range pipeline.Jobs {
			job.Steps[0].RunOnAgent = false
		}
		if err := NewSchemaValidator().Validate(pipeline); err != nil {
			t.Fatalf("local fork rejected: %v", err)
		}
		request, err := ResolveRuntime(pipeline, policy)
		if err != nil || request != nil {
			t.Fatalf("local fork resolved as isolated runtime: %+v, %v", request, err)
		}
	}
}
