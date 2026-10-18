package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const pipelineRuntimeImage = "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func pipelineRuntimePolicy() taskqueue.OfflineRuntimePolicy {
	return taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{pipelineRuntimeImage}, MaxCPU: "1", MaxMemory: "128Mi"}
}

func pipelineRuntimeSpec() *pipelinev1.Spec {
	return &pipelinev1.Spec{
		Namespace: "offline",
		Runtime: &pipelinev1.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: pipelineRuntimeImage,
			Resources: &pipelinev1.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}},
		Jobs: []*pipelinev1.Job{
			{Name: "a", Steps: []*pipelinev1.Step{{Name: "run", Uses: "shell", Action: "run", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "echo a"})}}},
			{Name: "b", DependsOn: []string{"a"}, Steps: []*pipelinev1.Step{{Name: "run", Uses: "shell", Action: "run", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "echo b"})}}},
		},
	}
}

// pipelineRuntimeFixture 仅使用临时 SQLite 和完全替换的 git，禁止测试触及远端仓库
func pipelineRuntimeFixture(t *testing.T, parsed *pipelinev1.Spec) (pipelineServiceTestDB, *repo.Repositories, string, string) {
	t.Helper()
	directory := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(directory, "runtime.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := repo.MigrateSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	adapter := pipelineServiceTestDB{db}
	repositories := repo.NewRepositories(adapter, nil)
	ctx := context.Background()
	if err := repositories.Project.Create(ctx, &model.Project{ProjectID: "runtime-project", Name: "runtime", RepoURL: "https://fixture.invalid/repo.git", DefaultBranch: "main", IsEnabled: 1, Status: model.ProjectStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pipeline.Create(ctx, &model.Pipeline{PipelineID: "runtime-pipeline", ProjectID: "runtime-project", Name: "runtime", RepoURL: "https://fixture.invalid/repo.git", DefaultBranch: "main", PipelineFilePath: "pipeline.json", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	content, err := spec.MarshalProtoByFormat(parsed, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(directory, "source.json")
	if err := os.WriteFile(fixture, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	calls, captured := filepath.Join(directory, "git.calls"), filepath.Join(directory, "saved.json")
	// No real git is invoked, including for clone, commit, or push.
	script := `#!/bin/sh
set -eu
if [ "$1" = -c ]; then
  [ "$2" = user.name=Arcentra ] && [ "$3" = -c ] && [ "$4" = user.email=noreply@arcentra.local ] && [ "$5" = commit ] || exit 1
  shift 4
fi
printf '%s\n' "$1" >> "$ARCENTRA_TEST_GIT_CALLS"
case "$1" in
clone) cp "$ARCENTRA_TEST_PIPELINE_FILE" pipeline.json ;;
rev-parse) printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
add) cp pipeline.json "$ARCENTRA_TEST_SAVED_PIPELINE" ;;
commit|push) ;;
*) echo "unexpected fake git operation" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARCENTRA_TEST_PIPELINE_FILE", fixture)
	t.Setenv("ARCENTRA_TEST_GIT_CALLS", calls)
	t.Setenv("ARCENTRA_TEST_SAVED_PIPELINE", captured)
	return adapter, repositories, calls, captured
}

func TestPipelineRuntimePolicyAppliesToEveryDefinitionAPI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		allowed bool
		mutate  func(*pipelinev1.Spec)
	}{
		{name: "default-off"},
		{name: "operator-enabled", enabled: true, allowed: true},
		{name: "default baremetal", allowed: true, mutate: func(p *pipelinev1.Spec) { p.Runtime = nil }},
		{name: "unapproved image", enabled: true, mutate: func(p *pipelinev1.Spec) { p.Runtime.Image = strings.Replace(pipelineRuntimeImage, "aaaa", "bbbb", 1) }},
		{name: "CPU maximum", enabled: true, mutate: func(p *pipelinev1.Spec) { p.Runtime.Resources.CpuLimit = "2" }},
		{name: "memory maximum", enabled: true, mutate: func(p *pipelinev1.Spec) { p.Runtime.Resources.MemoryLimit = "256Mi" }},
		{name: "forged request policy", mutate: func(p *pipelinev1.Spec) {
			p.Meta = spec.MapAsStruct(map[string]any{"offlineRuntime": map[string]any{"enabled": true, "allowedImages": []any{pipelineRuntimeImage}, "maxCpu": "999", "maxMemory": "999Gi"}})
			p.Variables = map[string]string{"offlineRuntime.enabled": "true"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := pipelineRuntimeSpec()
			if tc.mutate != nil {
				tc.mutate(parsed)
			}
			adapter, repositories, calls, captured := pipelineRuntimeFixture(t, parsed)
			var services *Services
			if tc.enabled {
				var err error
				services, err = ProvideServices(adapter, nil, repositories, pipelineRuntimePolicy())
				if err != nil {
					t.Fatal(err)
				}
			} else {
				services = NewServices(adapter, nil, repositories)
			}
			t.Cleanup(func() { _ = services.LogAggregator.Close(context.Background()) })
			engine := &triggerCaptureEngine{}
			if err := services.BindPipelineEngine(engine); err != nil {
				t.Fatal(err)
			}
			svc := NewPipelineServiceImpl(services)
			ctx := context.Background()
			get, err := svc.GetPipelineSpec(ctx, &pipelinev1.GetPipelineSpecRequest{PipelineId: "runtime-pipeline"})
			if err != nil || get.Success != tc.allowed || (!tc.allowed && get.GetError().GetCode() != 400) {
				t.Fatalf("read policy mismatch: %+v %v", get, err)
			}
			for _, request := range []*pipelinev1.ValidatePipelineSpecRequest{{Spec: parsed}, {PipelineId: "runtime-pipeline"}} {
				response, err := svc.ValidatePipelineSpec(ctx, request)
				if err != nil || response.Success != tc.allowed || (!tc.allowed && response.GetError().GetCode() != 400) {
					t.Fatalf("validation policy mismatch: %+v %v", response, err)
				}
			}
			saved, err := svc.SavePipelineSpec(ctx, &pipelinev1.SavePipelineSpecRequest{PipelineId: "runtime-pipeline", Spec: parsed, Format: pipelinev1.SpecFormat_SPEC_FORMAT_JSON, RequestId: "runtime-save", Editor: "fixture"})
			if err != nil || saved.Success != tc.allowed || (!tc.allowed && saved.GetError().GetCode() != 400) {
				t.Fatalf("save policy mismatch: %+v %v", saved, err)
			}
			triggered, err := svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "runtime-trigger", TriggeredBy: "fixture"})
			if err != nil || triggered.Success != tc.allowed || (!tc.allowed && triggered.GetError().GetCode() != 400) {
				t.Fatalf("trigger policy mismatch: %+v %v", triggered, err)
			}
			operations, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				if engine.calls != 1 || engine.run.TotalJobs != 2 || !strings.Contains(string(operations), "push\n") {
					t.Fatalf("enabled definition did not reach fake save/engine: %s %+v", operations, engine)
				}
				content, err := os.ReadFile(captured)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := spec.ParseContentToProto(string(content), pipelinev1.SpecFormat_SPEC_FORMAT_JSON)
				if err != nil || !proto.Equal(parsed, roundTrip) {
					t.Fatalf("runtime spec changed on save: %+v %v", roundTrip, err)
				}
			} else if engine.calls != 0 || strings.Contains(string(operations), "add\n") || strings.Contains(string(operations), "commit\n") || strings.Contains(string(operations), "push\n") {
				t.Fatalf("disabled runtime produced a save or execution: %s %+v", operations, engine)
			}
		})
	}
}

func TestPipelineRuntimeServiceCopiesPolicyAndRejectsRequestEscalation(t *testing.T) {
	parsed := pipelineRuntimeSpec()
	adapter, repositories, _, _ := pipelineRuntimeFixture(t, parsed)
	policy := pipelineRuntimePolicy()
	services, err := ProvideServices(adapter, nil, repositories, policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.LogAggregator.Close(context.Background()) })
	policy.AllowedImages[0] = "changed"
	policy.MaxCPU = "999"
	policy.Enabled = false
	svc := NewPipelineServiceImpl(services)
	services.offlineRuntimePolicy.AllowedImages[0] = "changed-again"
	services.offlineRuntimePolicy.Enabled = false
	valid, err := svc.ValidatePipelineSpec(context.Background(), &pipelinev1.ValidatePipelineSpecRequest{Spec: parsed})
	if err != nil || !valid.Success {
		t.Fatalf("service policy aliases its source: %+v %v", valid, err)
	}
	for name, mutate := range map[string]func(*pipelinev1.Spec){
		"unapproved image": func(p *pipelinev1.Spec) { p.Runtime.Image = strings.Replace(pipelineRuntimeImage, "aaaa", "bbbb", 1) },
		"CPU maximum":      func(p *pipelinev1.Spec) { p.Runtime.Resources.CpuLimit = "2" },
		"memory maximum":   func(p *pipelinev1.Spec) { p.Runtime.Resources.MemoryLimit = "256Mi" },
		"request policy": func(p *pipelinev1.Spec) {
			p.Runtime.Config = spec.MapAsStruct(map[string]any{"enabled": true, "allowedImages": []any{pipelineRuntimeImage}, "maxCpu": "999", "maxMemory": "999Gi"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			unsafe := proto.Clone(parsed).(*pipelinev1.Spec)
			mutate(unsafe)
			unsafe.Meta = spec.MapAsStruct(map[string]any{"offlineRuntime": map[string]any{"enabled": true, "maxCpu": "999", "maxMemory": "999Gi"}})
			unsafe.Variables = map[string]string{"offlineRuntime.enabled": "true", "offlineRuntime.maxCpu": "999"}
			response, err := svc.ValidatePipelineSpec(context.Background(), &pipelinev1.ValidatePipelineSpecRequest{Spec: unsafe})
			if err != nil || response.Success || response.GetError().GetCode() != 400 {
				t.Fatalf("request expanded operator policy: %+v %v", response, err)
			}
		})
	}
	defaultSvc := NewPipelineServiceImpl(&Services{})
	parsed.Meta = spec.MapAsStruct(map[string]any{"offlineRuntime": map[string]any{"enabled": true, "allowedImages": []any{pipelineRuntimeImage}, "maxCpu": "999", "maxMemory": "999Gi"}})
	response, err := defaultSvc.ValidatePipelineSpec(context.Background(), &pipelinev1.ValidatePipelineSpecRequest{Spec: parsed})
	if err != nil || response.Success || !strings.Contains(response.GetError().GetMessage(), "disabled") {
		t.Fatalf("request enabled default-off service: %+v %v", response, err)
	}
}

func TestProvideServicesRejectsInvalidRuntimePolicyBeforeInitialization(t *testing.T) {
	services, err := ProvideServices(nil, nil, nil, taskqueue.OfflineRuntimePolicy{Enabled: true})
	if err == nil || services != nil {
		t.Fatalf("invalid startup policy accepted: %+v %v", services, err)
	}
}
