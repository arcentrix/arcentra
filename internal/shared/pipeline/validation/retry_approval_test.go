// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package validation

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
	"testing"
)

func TestDurableRetryApprovalRequiresExactGateAndIndependentOptIn(t *testing.T) {
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true}
	job := &pipelinev1.Job{Name: "build", Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Approval: &pipelinev1.Approval{Required: true, Type: "manual", Plugin: "builtin:retry-approval"}}
	if !IsDurableRetryApproval(job, policy) {
		t.Fatal("exact gate rejected")
	}
	for name, mutate := range map[string]func(*pipelinev1.Job){"missing type": func(j *pipelinev1.Job) { j.Approval.Type = "" }, "wrong plugin": func(j *pipelinev1.Job) { j.Approval.Plugin = "builtin:deployment" }, "optional": func(j *pipelinev1.Job) { j.Approval.Required = false }, "unbounded": func(j *pipelinev1.Job) { j.Retry.MaxAttempts = 0 }, "no retry": func(j *pipelinev1.Job) { j.Retry = nil }, "candidate injection": func(j *pipelinev1.Job) {
		j.Approval.Params, _ = structpb.NewStruct(map[string]any{"candidates": []any{"me"}})
	}} {
		t.Run(name, func(t *testing.T) {
			copy := proto.Clone(job).(*pipelinev1.Job)
			mutate(copy)
			if IsDurableRetryApproval(copy, policy) {
				t.Fatal("unsupported gate accepted")
			}
		})
	}
	policy.AllowWholeJobRetryApproval = false
	if IsDurableRetryApproval(job, policy) {
		t.Fatal("operator opt-in omitted")
	}
	p := &pipelinev1.Spec{Namespace: "test", Jobs: []*pipelinev1.Job{job}}
	if _, err := RetryApprovalJobNames(p, policy); err == nil {
		t.Fatal("baremetal gate accepted")
	}
}

func TestResolvedRetryApprovalDefinitionBindsExpandedContent(t *testing.T) {
	p := &pipelinev1.Spec{Namespace: "test", Variables: map[string]string{"B": "2", "A": "1"}, Jobs: []*pipelinev1.Job{{Name: "build"}}}
	first, err := ResolvedDefinitionDigest(p)
	if err != nil || !strings.HasPrefix(first, "v1:") {
		t.Fatal(first, err)
	}
	copy := proto.Clone(p).(*pipelinev1.Spec)
	same, _ := ResolvedDefinitionDigest(copy)
	if same != first {
		t.Fatal("digest nondeterministic")
	}
	copy.Jobs[0].Steps = []*pipelinev1.Step{{Name: "included", Uses: "shell"}}
	changed, _ := ResolvedDefinitionDigest(copy)
	if changed == first {
		t.Fatal("expanded definition not bound")
	}
}
