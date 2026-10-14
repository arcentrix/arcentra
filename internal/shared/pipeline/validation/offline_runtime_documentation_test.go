// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package validation

import (
	"os"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestDocumentedOfflineChainDefinition(t *testing.T) {
	document, err := os.ReadFile("../../../../docs/offline-runtime-acceptance.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(document), "```yaml\n")
	if !found {
		t.Fatal("operator guide has no runnable chain definition")
	}
	content, _, found := strings.Cut(rest, "\n```")
	if !found {
		t.Fatal("operator guide definition fence is incomplete")
	}
	definition, err := spec.ParseContentToProto(content, pipelinev1.SpecFormat_SPEC_FORMAT_YAML)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Jobs) != 2 || len(definition.Jobs[1].DependsOn) != 1 || definition.Jobs[1].DependsOn[0] != definition.Jobs[0].Name {
		t.Fatal("operator example must demonstrate an explicit two-job chain")
	}
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{definition.Runtime.Image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(definition); err != nil {
		t.Fatal(err)
	}
	if err := NewSchemaValidator().Validate(definition); err == nil {
		t.Fatal("documented definition bypassed default-disabled policy")
	}
}

func TestOfflineArtifactContinueOnErrorRejectedForEveryJob(t *testing.T) {
	for position := 0; position < 3; position++ {
		pipeline, policy := schemaRuntimeChainFixture()
		job := pipeline.Jobs[position]
		job.Steps = append(job.Steps, &spec.Step{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, ContinueOnError: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"result.txt"}})})
		checkOfflineRuntimeValidation(t, pipeline, policy, "artifact staging cannot continue on error")
	}
}
