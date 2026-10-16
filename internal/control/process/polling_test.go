// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestPollingSubmissionBindsExpandedDefinitionAndRunIdentity(t *testing.T) {
	parsed := &spec.Pipeline{Namespace: "polling"}
	digest, err := service.PollingDefinitionDigest(parsed)
	require.NoError(t, err)
	scope := polling.Scope{Version: 1, ProjectID: "project", Provider: "gitlab", APIOrigin: "https://gitlab.example.test", Repository: "https://gitlab.example.test/group/repo", ProviderProjectID: "42", Stream: "merge_requests"}
	scopeDigest, err := scope.Digest()
	require.NoError(t, err)
	requestID, err := polling.EventRequestID(scope, "observation")
	require.NoError(t, err)
	target := polling.Target{MRID: 1, MRIID: 1, State: "opened", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), PipelineID: "pipeline", ObservationID: "observation", RequestID: requestID, SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), DefinitionCommitSHA: strings.Repeat("b", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: digest}
	evidence := polling.RunContext{Version: 1, BaselineID: "baseline", BaselineCutoff: time.Now().UTC().Format(time.RFC3339Nano), StreamID: "stream", Scope: scope, ScopeDigest: scopeDigest, ActivationGeneration: 1, ActorID: "operator", SelectionDigest: "v1:" + strings.Repeat("c", 64), BatchID: "batch", BatchRevision: 2, RunID: "run", Target: target}
	raw, err := evidence.Canonical()
	require.NoError(t, err)
	ed, err := evidence.Digest()
	require.NoError(t, err)
	run := model.PipelineRun{RunID: "run", PipelineID: "pipeline", RequestID: requestID, Branch: "feature", CommitSha: target.SourceSHA, DefinitionCommitSha: target.DefinitionCommitSHA, DefinitionPath: target.DefinitionPath, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT), TriggeredBy: polling.Actor, PollingContext: raw, PollingContextDigest: ed}
	yes, err := validatePollingSubmission(&run, parsed)
	require.True(t, yes)
	require.NoError(t, err)
	altered := &spec.Pipeline{Namespace: "unapproved"}
	yes, err = validatePollingSubmission(&run, altered)
	require.True(t, yes)
	require.Error(t, err)
	for _, name := range []string{"run", "pipeline", "request", "actor", "manual", "context", "digest", "definition", "source"} {
		t.Run(name, func(t *testing.T) {
			changed := run
			switch name {
			case "run":
				changed.RunID = "other"
			case "pipeline":
				changed.PipelineID = "other"
			case "request":
				changed.RequestID = "other"
			case "actor":
				changed.TriggeredBy = "manual"
			case "manual":
				changed.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
			case "context":
				changed.PollingContext = ""
			case "digest":
				changed.PollingContextDigest = "bad"
			case "definition":
				changed.DefinitionCommitSha = strings.Repeat("c", 40)
			case "source":
				changed.CommitSha = strings.Repeat("d", 40)
			}
			detected, e := validatePollingSubmission(&changed, parsed)
			require.True(t, detected)
			require.Error(t, e)
		})
	}
	manual := &model.PipelineRun{RunID: "manual", TriggeredBy: "user", TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)}
	yes, err = validatePollingSubmission(manual, parsed)
	require.False(t, yes)
	require.NoError(t, err)
}
