// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type prospectiveCaptureEngine struct {
	triggerCaptureEngine
	parsed *pipelinev1.Spec
}

func (e *prospectiveCaptureEngine) SubmitVerified(_ context.Context, run *model.PipelineRun, p *pipelinev1.Spec) error {
	e.parsed = proto.Clone(p).(*pipelinev1.Spec)
	return e.Submit(run, p)
}
func prospectiveCaptureFixture(t *testing.T) (*PipelineServiceImpl, *gorm.DB, *prospectiveCaptureEngine, context.Context, *pipelinev1.TriggerPipelineRequest) {
	t.Helper()
	db, _, _, _, _, key, policy := rerunReviewFixture(t)
	repositories := repo.NewRepositories(rerunBridgeDatabase{db}, nil)
	services := &Services{repos: repositories, Secret: key}
	require.NoError(t, services.ConfigureRetryApprovalPolicy(policy))
	require.NoError(t, services.ConfigureCopilotRerunCapture(CopilotRerunCaptureEligibleVerifiedManual))
	engine := &prospectiveCaptureEngine{}
	svc := &PipelineServiceImpl{services: services, pipelineRepo: repositories.Pipeline, projectRepo: repositories.Project, engine: engine, offlineRuntimePolicy: rerunSnapshotPolicy()}
	require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_GIT_CALLS"), nil, 0600))
	return svc, db, engine, WithVerifiedPipelineActor(context.Background(), "actor"), &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "prospective-request", TriggeredBy: "actor", Variables: map[string]string{"input": "private-literal"}}
}
func captureCounts(t *testing.T, db *gorm.DB, runs, snapshots int64) {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.PipelineRun{}).Count(&n).Error)
	require.Equal(t, runs, n)
	require.NoError(t, db.Model(&model.CopilotDefinitionSnapshot{}).Count(&n).Error)
	require.Equal(t, snapshots, n)
}
func TestProspectiveCaptureExactSingleLoadAndDuplicate(t *testing.T) {
	s, db, e, ctx, req := prospectiveCaptureFixture(t)
	out, err := s.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, out.Success, out.Message)
	captureCounts(t, db, 1, 1)
	require.Equal(t, 1, e.calls)
	var snapshot model.CopilotDefinitionSnapshot
	require.NoError(t, db.First(&snapshot).Error)
	require.Equal(t, out.RunId, snapshot.RunID)
	require.NotContains(t, string(snapshot.PayloadCiphertext), "private-literal")
	parsed, vars, refs, err := openCopilotDefinitionSnapshot(&snapshot, s.offlineRuntimePolicy, s.services.Secret)
	require.NoError(t, err)
	require.True(t, proto.Equal(e.parsed, parsed))
	require.Equal(t, req.Variables, vars)
	require.Empty(t, refs)
	calls, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_GIT_CALLS"))
	require.NoError(t, err)
	require.Equal(t, "clone\nrev-parse\n", string(calls))
	require.NoError(t, os.Remove(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")))
	again, err := s.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, again.Success)
	require.Equal(t, out.RunId, again.RunId)
	require.Equal(t, 1, e.calls)
	captureCounts(t, db, 1, 1)
	changed := proto.Clone(req).(*pipelinev1.TriggerPipelineRequest)
	changed.Variables["input"] = "changed"
	conflict, err := s.TriggerPipeline(ctx, changed)
	require.NoError(t, err)
	require.False(t, conflict.Success)
}

type captureRepositoryWrapper struct {
	repo.IPipelineRepository
	create func(context.Context, string, *model.PipelineRun, *model.CopilotDefinitionSnapshot, repo.VerifiedManualTriggerAuthorize) error
}

func (w captureRepositoryWrapper) CreateVerifiedManualTriggerRunWithDefinition(ctx context.Context, a string, r *model.PipelineRun, s *model.CopilotDefinitionSnapshot, authorize repo.VerifiedManualTriggerAuthorize) error {
	return w.create(ctx, a, r, s, authorize)
}
func TestProspectiveCaptureRollbackAndUnknownCommit(t *testing.T) {
	for _, scenario := range []string{"crypto", "config-drift", "snapshot-write", "lost-ack"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, e, ctx, req := prospectiveCaptureFixture(t)
			original := s.pipelineRepo.(repo.IVerifiedManualDefinitionCreator)
			switch scenario {
			case "crypto":
				s.services.Secret = &SecretService{}
			case "snapshot-write":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:deny-snapshot", func(tx *gorm.DB) {
					if tx.Statement.Table == "copilot_definition_snapshot" {
						tx.AddError(errors.New("synthetic snapshot write failed"))
					}
				}))
			default:
				s.pipelineRepo = captureRepositoryWrapper{s.pipelineRepo, func(c context.Context, a string, r *model.PipelineRun, snap *model.CopilotDefinitionSnapshot, authorize repo.VerifiedManualTriggerAuthorize) error {
					if scenario == "config-drift" {
						require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", r.PipelineID).Update("default_branch", "changed").Error)
					}
					err := original.CreateVerifiedManualTriggerRunWithDefinition(c, a, r, snap, authorize)
					if err == nil && scenario == "lost-ack" {
						return errors.New("synthetic commit ACK lost")
					}
					return err
				}}
			}
			out, err := s.TriggerPipeline(ctx, req)
			require.NoError(t, err)
			if scenario == "lost-ack" {
				require.True(t, out.Success, out.Message)
				require.NotEmpty(t, out.RunId)
				captureCounts(t, db, 1, 1)
			} else {
				require.False(t, out.Success)
				captureCounts(t, db, 0, 0)
			}
			require.Zero(t, e.calls)
		})
	}
}
func TestProspectiveCaptureUnsupportedPreservesOrdinaryExecution(t *testing.T) {
	for _, scenario := range []string{"off", "multi-job", "source-auth", "managed-reference", "no-approval", "legacy-actor"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, e, ctx, req := prospectiveCaptureFixture(t)
			body, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"))
			require.NoError(t, err)
			parsed, err := spec.ParseContentToProto(string(body), pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
			require.NoError(t, err)
			switch scenario {
			case "off":
				require.NoError(t, s.services.ConfigureCopilotRerunCapture(CopilotRerunCaptureOff))
			case "multi-job":
				second := proto.Clone(parsed.Jobs[0]).(*pipelinev1.Job)
				second.Name = "second"
				second.DependsOn = []string{parsed.Jobs[0].Name}
				parsed.Jobs = append(parsed.Jobs, second)
			case "source-auth":
				parsed.Jobs[0].Source.Auth = &pipelinev1.SourceAuth{Username: "literal-user"}
			case "managed-reference":
				parsed.Jobs[0].Env = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}
			case "no-approval":
				parsed.Jobs[0].Approval = nil
			case "legacy-actor":
				ctx = context.Background()
				parsed.Jobs[0].Approval = nil
			}
			content, err := spec.MarshalProtoByFormat(parsed, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"), []byte(content), 0600))
			out, err := s.TriggerPipeline(ctx, req)
			require.NoError(t, err)
			require.True(t, out.Success, out.Message)
			captureCounts(t, db, 1, 0)
			require.Equal(t, 1, e.calls)
			if scenario != "off" {
				require.Contains(t, out.Message, "prospective re-execution unavailable:")
			}
			require.NotContains(t, out.Message, "literal-user")
			require.NotContains(t, out.Message, "secrets.TOKEN")
		})
	}
}
func TestProspectiveCapturePolicyAndReceiptBounds(t *testing.T) {
	s, _, _, _, _ := prospectiveCaptureFixture(t)
	require.Error(t, s.services.ConfigureCopilotRerunCapture("AUTO"))
	require.Equal(t, CopilotRerunCaptureEligibleVerifiedManual, s.services.rerunCapturePolicy)
	require.NoError(t, s.services.ConfigureCopilotRerunCapture(""))
	p := &model.Pipeline{PipelineID: "p", ProjectID: "q", RepoURL: "https://fixture.invalid/repo.git", DefaultBranch: "main", PipelineFilePath: "pipeline.json"}
	q := &model.Project{ProjectID: "q"}
	for _, revision := range []string{"main", strings.Repeat("A", 40), ""} {
		_, err := makeCopilotDefinitionReceipt(p, q, "{}", "{}", revision, pipelineRuntimeSpec(), nil)
		require.Error(t, err)
	}
}

func TestProspectiveCaptureFreezesOriginalIncludeExpansion(t *testing.T) {
	s, db, e, ctx, req := prospectiveCaptureFixture(t)
	body, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"))
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(body, &document))
	templateBody, err := json.Marshal(map[string]any{"jobs": document["jobs"]})
	require.NoError(t, err)
	delete(document, "jobs")
	document["include"] = []any{map[string]any{"template": "frozen"}}
	body, err = json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"), body, 0600))
	templates := &rerunSnapshotTemplateRepository{template: &model.PipelineTemplate{Name: "frozen", SpecContent: string(templateBody)}}
	s.templateResolver = NewPipelineTemplateService(templates, nil)
	out, err := s.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, out.Success, out.Message)
	require.Equal(t, 1, templates.calls)
	templates.template.SpecContent = `{"jobs":[]}`
	var snapshot model.CopilotDefinitionSnapshot
	require.NoError(t, db.First(&snapshot).Error)
	parsed, _, _, err := openCopilotDefinitionSnapshot(&snapshot, s.offlineRuntimePolicy, s.services.Secret)
	require.NoError(t, err)
	require.True(t, proto.Equal(e.parsed, parsed))
	require.Equal(t, 1, templates.calls)
}

func TestProspectiveCaptureDoesNotBackfillHistoricalDuplicate(t *testing.T) {
	s, db, e, ctx, req := prospectiveCaptureFixture(t)
	require.NoError(t, s.services.ConfigureCopilotRerunCapture(CopilotRerunCaptureOff))
	out, err := s.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, out.Success)
	captureCounts(t, db, 1, 0)
	require.NoError(t, s.services.ConfigureCopilotRerunCapture(CopilotRerunCaptureEligibleVerifiedManual))
	require.NoError(t, os.Remove(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")))
	again, err := s.TriggerPipeline(ctx, req)
	require.NoError(t, err)
	require.True(t, again.Success)
	require.Equal(t, out.RunId, again.RunId)
	require.Equal(t, 1, e.calls)
	captureCounts(t, db, 1, 0)
}

func TestProspectiveCaptureMissingCreatorAndUnexpandedIncludes(t *testing.T) {
	for _, scenario := range []string{"missing-creator", "unexpanded-include", "spoofed-actor"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, e, ctx, req := prospectiveCaptureFixture(t)
			switch scenario {
			case "missing-creator":
				s.pipelineRepo = struct{ repo.IPipelineRepository }{s.pipelineRepo}
			case "spoofed-actor":
				req.TriggeredBy = "reviewer"
			case "unexpanded-include":
				body, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"))
				require.NoError(t, err)
				var doc map[string]any
				require.NoError(t, json.Unmarshal(body, &doc))
				doc["include"] = []any{map[string]any{"template": "unresolved"}}
				body, err = json.Marshal(doc)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"), body, 0600))
			}
			out, err := s.TriggerPipeline(ctx, req)
			require.NoError(t, err)
			require.False(t, out.Success)
			captureCounts(t, db, 0, 0)
			require.Zero(t, e.calls)
			if scenario == "unexpanded-include" {
				// Preserve the ordinary parser's rejection; capture must not repair or
				// silently drop an unresolved include to manufacture eligibility.
				require.Equal(t, "spec parse failed", out.Message)
			}
		})
	}
}
