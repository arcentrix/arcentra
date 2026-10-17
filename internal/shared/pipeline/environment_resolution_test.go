// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func environmentContext(t *testing.T) *ExecutionContext {
	t.Helper()
	definition := &spec.Pipeline{Namespace: "environment", Runtime: &spec.Runtime{Env: map[string]string{"PRIORITY": "runtime", "RUNTIME_ONLY": "runtime"}}, Variables: map[string]string{"VALUE": "base", "PRIORITY": "pipeline"}}
	root := NewExecutionContext(definition, nil, t.TempDir(), log.Logger{SugaredLogger: zap.NewNop().Sugar()})
	// 与 Coordinator 相同：触发参数覆盖运行级变量，variables 快照不包含后续作业/步骤覆盖
	root.Env["PRIORITY"] = "trigger"
	root.Pipeline.Variables = maps.Clone(root.Env)
	root.Env["secrets.fixture"] = "synthetic-environment-canary"
	root.PipelineIDRef, root.PipelineRunID = "pipeline", "run"
	return root
}

func TestEnvironmentLayersAreSinglePassIndependentSnapshots(t *testing.T) {
	root := environmentContext(t)
	root.Env["INDIRECT"] = "${{secrets.fixture}}"
	job := &spec.Job{Env: map[string]string{"VALUE": "${{VALUE}}-job", "PRIORITY": "job", "EMPTY": "", "INDIRECT": "${{INDIRECT}}", "PEER": "${{VALUE}}"}}
	step := &spec.Step{Env: map[string]string{"VALUE": "${{VALUE}}-step", "PRIORITY": "step", "TOKEN": "${{secrets.fixture}}", "EMPTY": ""}}
	originalBase, originalJob, originalStep := maps.Clone(root.Env), maps.Clone(job.Env), maps.Clone(step.Env)
	want := map[string]string{"VALUE": "base-job-step", "PRIORITY": "step", "RUNTIME_ONLY": "runtime", "EMPTY": "", "PEER": "base", "INDIRECT": "${{secrets.fixture}}", "TOKEN": "synthetic-environment-canary", "secrets.fixture": "synthetic-environment-canary"}
	for i := 0; i < 10; i++ {
		got := root.ResolveStepEnv(job, step)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("layered environment = %#v, want %#v", got, want)
		}
		got["VALUE"] = "caller mutation"
	}
	if !reflect.DeepEqual(root.Env, originalBase) || !reflect.DeepEqual(job.Env, originalJob) || !reflect.DeepEqual(step.Env, originalStep) {
		t.Fatal("resolution mutated a source environment")
	}
	if got := (&ExecutionContext{}).ResolveStepEnv(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("nil scope did not return an independent empty map: %#v", got)
	}

	scoped := root.forStep(job, step)
	params := map[string]any{
		"value": "${{VALUE}}", "empty": "before${EMPTY}after", "unknown": "${{missing}}", "expression": "${{ VALUE + 1 }}", "namespace": "${{variables.VALUE}}",
		"nested": map[string]any{"indirect": "${{INDIRECT}}", "items": []any{"${VALUE}", true, float64(12), map[string]any{"priority": "${{PRIORITY}}"}}},
	}
	resolved := scoped.ResolveVariables(params)
	if resolved["value"] != "base-job-step" || resolved["empty"] != "beforeafter" || resolved["unknown"] != "${{missing}}" || resolved["expression"] != "${{ VALUE + 1 }}" || resolved["namespace"] != "${{variables.VALUE}}" {
		t.Fatalf("existing template semantics changed: %#v", resolved)
	}
	nested := resolved["nested"].(map[string]any)
	if nested["indirect"] != "${{secrets.fixture}}" {
		t.Fatal("replacement recursively expanded an indirect secret reference")
	}
	items := nested["items"].([]any)
	if items[0] != "base-job-step" || items[1] != true || items[2] != float64(12) || items[3].(map[string]any)["priority"] != "step" {
		t.Fatalf("nested arguments lost values or types: %#v", items)
	}
	items[3].(map[string]any)["priority"] = "changed"
	if params["value"] != "${{VALUE}}" || params["nested"].(map[string]any)["items"].([]any)[3].(map[string]any)["priority"] != "${{PRIORITY}}" {
		t.Fatal("resolved arguments alias the definition")
	}
}

func TestLocalExecutionEnvironmentScopesStayStableAcrossRetries(t *testing.T) {
	for _, mode := range []string{"none", "failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			root := environmentContext(t)
			command := `test "$VALUE" = base-job-step; test '${{VALUE}}' = "$VALUE"; test "$TOKEN" = '${{secrets.fixture}}'; printf x >> attempts; printf '%s' "$VALUE" > actual`
			job := &spec.Job{Name: "build", Env: map[string]string{"VALUE": "${{VALUE}}-job"}, When: `VALUE == "base-job" && env.VALUE == "base-job" && variables.VALUE == "base"`, Steps: []*spec.Step{
				{Name: "scoped", Uses: "shell", Env: map[string]string{"VALUE": "${{VALUE}}-step", "TOKEN": "${{secrets.fixture}}"}, When: `VALUE == "base-job-step" && env.VALUE == "base-job-step" && variables.VALUE == "base"`, Args: spec.MapAsStruct(map[string]any{"run": command})},
				{Name: "sibling", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": `test "$VALUE" = base-job; test '${{VALUE}}' = "$VALUE"; printf '%s' "$VALUE" > sibling`})},
			}}
			wantAttempts := "x"
			if mode != "none" {
				job.Retry = &spec.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
				job.Steps[0].Args = spec.MapAsStruct(map[string]any{"run": command + `; if [ ! -f attempted ]; then touch attempted; exit 9; fi`})
				wantAttempts = "xx"
			}
			if err := NewTaskFramework(root, root.Logger).Execute(context.Background(), &Task{Name: job.Name, Job: job}); err != nil {
				t.Fatal(err)
			}
			for file, want := range map[string]string{"actual": "base-job-step", "sibling": "base-job", "attempts": wantAttempts} {
				got, err := os.ReadFile(filepath.Join(root.WorkspaceRoot, "environment", "build", file))
				if err != nil || string(got) != want {
					t.Fatalf("%s=%q, error=%v, want %q", file, got, err, want)
				}
			}
			if root.Env["VALUE"] != "base" || job.Env["VALUE"] != "${{VALUE}}-job" || job.Steps[0].Env["VALUE"] != "${{VALUE}}-step" {
				t.Fatal("execution or retry mutated the original environment")
			}
		})
	}
}

type environmentDispatchStore struct {
	jsonCreationStore
	binding *sourceprovenance.Binding
}

func (s *environmentDispatchStore) CaptureSourceExpectation(_ context.Context, _, _, _, _ string, binding *sourceprovenance.Binding) error {
	s.binding = binding
	return nil
}

type environmentDispatchQueue struct {
	nova.TaskQueue
	payload []byte
}

func (q *environmentDispatchQueue) Enqueue(task *nova.Task, _ ...nova.Option) (*nova.Result, error) {
	q.payload = append([]byte(nil), task.Payload...)
	return &nova.Result{}, nil
}

func TestAgentDispatchUsesSameScopedEnvironmentForArgumentsAndSource(t *testing.T) {
	root := environmentContext(t)
	root.Env["REF"] = "main"
	root.Env["REPOSITORY"] = "https://example.invalid/repository.git"
	root.RecordArtifact("upstream/result.txt", "opaque-object-key")
	job := &spec.Job{Name: "build", Env: map[string]string{"VALUE": "${{VALUE}}-job", "REF": "${{REF}}-job", "INPUT": "${{ARTIFACT_upstream_result_txt}}", "ARTIFACT_upstream_result_txt": "untrusted-job-override"}, Source: &spec.Source{Type: "git", Repo: "${{REPOSITORY}}", Branch: "${{REF}}", Auth: &spec.SourceAuth{Token: "${{secrets.fixture}}"}}, Steps: []*spec.Step{
		{Name: "scoped", Uses: "shell", RunOnAgent: true, Env: map[string]string{"VALUE": "${{VALUE}}-step"}, When: `VALUE == "base-job-step" && env.VALUE == "base-job-step" && variables.VALUE == "base"`, Args: spec.MapAsStruct(map[string]any{"run": "printf '%s' '${{VALUE}}'", "nested": map[string]any{"input": "${{INPUT}}"}})},
		{Name: "sibling", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "printf '%s' '${{VALUE}}'"})},
	}}
	store, queue := &environmentDispatchStore{}, &environmentDispatchQueue{}
	scoped := root.ForJob()
	scoped.JobRunStore, scoped.TaskQueue = store, queue
	scoped.SelectAgent = func(context.Context, *spec.Job) (string, error) { return "agent", nil }
	framework := NewTaskFramework(scoped, root.Logger)
	task := &Task{Name: job.Name, Job: job}
	ctx := context.Background()
	if err := framework.createRecords(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := framework.prepare(ctx, task); err != nil {
		t.Fatal(err)
	}
	// 重复读取同一个作业快照不得再次追加作业模板
	for i := 0; i < 3; i++ {
		if got := scoped.ResolveStepEnv(job, nil)["VALUE"]; got != "base-job" {
			t.Fatalf("job snapshot re-expanded: %q", got)
		}
	}
	if err := framework.queue(ctx, task); err != nil {
		t.Fatal(err)
	}
	var payload taskqueue.JobRunTaskPayload
	if err := sonic.Unmarshal(queue.payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Env["VALUE"] != "base-job" || payload.Env["INPUT"] != "opaque-object-key" || payload.Env["ARTIFACT_upstream_result_txt"] != "opaque-object-key" || payload.Variables["VALUE"] != "base" {
		t.Fatalf("job/variable/artifact snapshot drifted: %+v", payload)
	}
	for i, step := range payload.Steps {
		want := "base-job-step"
		if i == 1 {
			want = "base-job"
		}
		if step.Env["VALUE"] != want || step.Args["run"] != "printf '%s' '"+want+"'" || step.When != job.Steps[i].When {
			t.Fatalf("Agent step env/args/when disagree: %+v", step)
		}
	}
	if payload.Steps[0].Args["nested"].(map[string]any)["input"] != "opaque-object-key" || payload.Source.Branch != "main-job" || store.binding.Ref != "main-job" || payload.Source.Auth["token"] != "synthetic-environment-canary" {
		t.Fatal("nested arguments or source did not use their scoped snapshots")
	}
	if root.Env["VALUE"] != "base" || root.Env["ARTIFACT_upstream_result_txt"] != "" || job.Source.Branch != "${{REF}}" {
		t.Fatal("job preparation mutated run or source definition")
	}
}

func TestLocalSourceUsesSinglePassJobEnvironment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for local source regression")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	ctx := context.Background()
	repository := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repository
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Environment Test", "GIT_AUTHOR_EMAIL=environment@example.invalid", "GIT_COMMITTER_NAME=Environment Test", "GIT_COMMITTER_EMAIL=environment@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Git fixture: %v: %s", err, out)
		}
	}
	git("init", "-b", "main-job")
	if err := os.WriteFile(filepath.Join(repository, "source.txt"), []byte("selected-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("commit", "-m", "synthetic scoped source")
	root := environmentContext(t)
	root.Env["REF"], root.Env["REPOSITORY"] = "main", repository
	job := &spec.Job{Name: "checkout", Env: map[string]string{"REF": "${{REF}}-job"}, Source: &spec.Source{Type: "git", Repo: "${{REPOSITORY}}", Branch: "${{REF}}"}, Steps: []*spec.Step{{Name: "verify", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": `test "$(cat source.txt)" = selected-source; test "$REF" = main-job; test '${{REF}}' = "$REF"`})}}}
	if err := NewTaskFramework(root, root.Logger).Execute(ctx, &Task{Name: job.Name, Job: job}); err != nil {
		t.Fatal(err)
	}
	if got := root.Env["REF"]; got != "main" {
		t.Fatalf("checkout modified run environment: %q", got)
	}
}

func TestResolvedStepSecretStillRedactsShellOutput(t *testing.T) {
	root := environmentContext(t)
	capture := &localShellLogCapture{}
	root.LogPublisher = capture
	job := &spec.Job{Name: "secrets", Steps: []*spec.Step{{Name: "print", Uses: "shell", Env: map[string]string{"TOKEN": "${{secrets.fixture}}"}, Args: spec.MapAsStruct(map[string]any{"run": `printf '%s' "$TOKEN"; printf '%s' '${{TOKEN}}'`})}}}
	if err := NewTaskFramework(root, root.Logger).Execute(context.Background(), &Task{Name: job.Name, Job: job}); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if text := capture.output.String(); strings.Contains(text, "synthetic-environment-canary") || strings.Count(text, "***") != 2 {
		t.Fatalf("resolved secret was not redacted: %q", text)
	}
}

func TestConditionErrorsNeverExposeResolvedEnvironmentValues(t *testing.T) {
	const secret = "synthetic-environment-canary"
	for name, condition := range map[string]string{
		"non-boolean":    "TOKEN",
		"compile-source": "'" + secret + "' ==",
		"runtime-value":  "int(TOKEN) > 0",
	} {
		t.Run(name, func(t *testing.T) {
			root := environmentContext(t)
			scoped := root.forStep(nil, &spec.Step{Env: map[string]string{"TOKEN": "${{secrets.fixture}}"}})
			for _, evaluate := range []func(string) (bool, error){scoped.EvalCondition, func(value string) (bool, error) {
				return scoped.EvalConditionWithContext(value, nil)
			}} {
				allowed, err := evaluate(condition)
				if allowed || err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), condition) {
					t.Fatalf("condition accepted or returned sensitive diagnostics: allowed=%t err=%v", allowed, err)
				}
			}
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "condition.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
				t.Fatal(err)
			}
			root.JobRunStore = &retryRecordStore{db: db}
			job := &spec.Job{Name: "condition", Steps: []*spec.Step{{Name: "never", Uses: "shell", Env: map[string]string{"TOKEN": "${{secrets.fixture}}"}, When: condition, Args: spec.MapAsStruct(map[string]any{"run": "touch forbidden"})}}}
			err = NewTaskFramework(root, root.Logger).Execute(ctx, &Task{Name: job.Name, Job: job})
			if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), condition) {
				t.Fatalf("execution accepted or exposed condition diagnostics: %v", err)
			}
			var storedJob model.JobRun
			var storedStep model.StepRun
			if err := db.WithContext(ctx).Select("status", "error_message").First(&storedJob).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.WithContext(ctx).Select("status", "error_message").First(&storedStep).Error; err != nil {
				t.Fatal(err)
			}
			for _, message := range []string{storedJob.ErrorMessage, storedStep.ErrorMessage} {
				if message == "" || strings.Contains(message, secret) || strings.Contains(message, condition) {
					t.Fatalf("unsafe persisted condition error: %q", message)
				}
			}
			if storedJob.Status != model.JobRunStatusFailed || storedStep.Status != 5 {
				t.Fatalf("invalid condition did not fail job/step: %d/%d", storedJob.Status, storedStep.Status)
			}
			if _, err := os.Stat(filepath.Join(root.WorkspaceRoot, "environment", "condition", "forbidden")); !os.IsNotExist(err) {
				t.Fatalf("invalid condition executed a command: %v", err)
			}
		})
	}
}
