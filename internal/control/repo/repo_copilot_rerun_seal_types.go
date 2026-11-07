// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// Internal trusted API only. No router, queue, claim or dispatcher invokes it.
type CopilotRerunSealInput struct {
	OwnerUserID, ProjectID, IntentID, AgentID, SealID                   string
	ExpectedIntentDigest, ExpectedReviewedRunContextDigest              string
	ExpectedIntentVersion, ExpectedGateVersion, ExpectedScheduleVersion uint64
}

type CopilotRerunSealedPayload struct {
	Reviewed   *CopilotRerunReviewedTarget
	Payload    *taskqueue.JobRunTaskPayload
	Binding    *CopilotRerunSealBinding
	Ciphertext []byte
}

// The callback uses only the supplied transaction-bound repositories, immutable
// definition/evidence and locked target scope/Agent. It must rebuild current
// policy and reject secret references until a transaction-bound version seam
// exists. Payload is transient; only its authenticated encrypted form is stored.
type CopilotRerunSealBuilder func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, string) (*CopilotRerunSealedPayload, error)

func CopilotRerunPayloadDigest(payload *taskqueue.JobRunTaskPayload) (string, error) {
	if payload == nil {
		return "", ErrCopilotRerunInvalid
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > CopilotRerunMaxDefinitionBytes {
		return "", ErrCopilotRerunInvalid
	}
	return rerunDigest("arcentra-copilot-rerun-payload-v1", payload), nil
}
func CopilotRerunEmptySecretVersionsDigest() string {
	return rerunDigest("arcentra-copilot-rerun-secret-versions-v1", []string{})
}
