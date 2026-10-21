// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// CopilotRerunClaimRepository is intentionally not implemented by the ordinary
// JobRunRepo. A trusted adapter must validate Gate/claim/seal and CURRENT payload
// disclosure authorization in its atomic transaction on every call, including
// duplicate claims. Historical receipt inspection cannot implement this method.
// It returns the original authenticated decrypted bytes, never reserialized
// payload. Nonce is borrowed transient memory, cleared after return: do not retain,
// persist, log, or return it. No network or independent authorization transaction
// may occur inside the repository's transaction. No default fallback exists.
type CopilotRerunClaimRepository interface {
	ClaimAndFetchCopilotRerun(context.Context, taskqueue.RerunClaimRequest) (*taskqueue.RerunClaimResult, error)
}

func (a *AgentServiceImpl) ClaimAndFetchCopilotRerun(ctx context.Context, req *agentv1.ClaimAndFetchCopilotRerunRequest) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
	// Actual gRPC transport evidence only: server-auth TLS plus existing verified
	// Agent token. Client certificate chains are not required (this is not mTLS).
	if err := requireRerunConfidentialTransport(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Reference == nil {
		return nil, status.Error(codes.InvalidArgument, "rerun reference required")
	}
	agent, err := authenticatedAgent(ctx, req.Reference.AgentId)
	if err != nil {
		return nil, err
	}
	// Do not inspect/copy nonce until both transport and Agent authentication pass.
	ref := req.Reference
	in := taskqueue.RerunClaimRequest{Reference: taskqueue.RerunClaimReference{AgentID: agent, PipelineID: ref.PipelineId, RunID: ref.RunId, JobRunID: ref.JobRunId, GateID: ref.GateId, ScheduleID: ref.ScheduleId, SealID: ref.SealId, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest}, ClaimID: req.ClaimId, AdmissionNonce: req.AdmissionNonce, ArtifactStorageID: req.ArtifactStorageId, ArtifactStorageFingerprint: req.ArtifactStorageFingerprint}
	if in.Validate() != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid rerun claim")
	}
	if a == nil || a.agentService == nil {
		return nil, status.Error(codes.Unimplemented, "protected rerun claim unavailable")
	}
	store := a.agentService.rerunClaim
	if store == nil {
		return nil, status.Error(codes.Unimplemented, "protected rerun claim unavailable")
	}
	in.AdmissionNonce = bytes.Clone(req.AdmissionNonce)
	defer clear(in.AdmissionNonce)
	out, err := callRerunClaimStore(ctx, store, in)
	if err != nil || out == nil {
		if errors.Is(err, taskqueue.ErrRerunClaimRejected) {
			return nil, status.Error(codes.FailedPrecondition, "protected rerun claim or disclosure rejected")
		}
		return nil, status.Error(codes.Unavailable, "protected rerun claim outcome unknown; retain original identity")
	}
	if !out.Receipt.Matches(in) || !validRerunClaimPayload(in.Reference, out.PayloadJSON) {
		return nil, status.Error(codes.Unavailable, "protected rerun receipt unavailable; retain original identity")
	}
	return &agentv1.ClaimAndFetchCopilotRerunResponse{Receipt: &agentv1.CopilotRerunClaimReceipt{Reference: proto.Clone(ref).(*agentv1.CopilotRerunClaimReference), ClaimId: out.Receipt.ClaimID, ClaimedAtUnixMilli: out.Receipt.ClaimedAtUnixMilli, ArtifactStorageId: out.Receipt.ArtifactStorageID, ArtifactStorageFingerprint: out.Receipt.ArtifactStorageFingerprint, ArtifactStorageType: out.Receipt.ArtifactStorageType}, PayloadJson: bytes.Clone(out.PayloadJSON)}, nil
}

func validRerunClaimPayload(ref taskqueue.RerunClaimReference, body []byte) bool {
	_, err := taskqueue.ParseRerunClaimPayload(ref, body)
	return err == nil
}

// Catch only this private adapter boundary. Never format/log the recovered value;
// it may contain nonce/plaintext. A panic can follow commit, so it is uncertain.
func callRerunClaimStore(ctx context.Context, store CopilotRerunClaimRepository, in taskqueue.RerunClaimRequest) (out *taskqueue.RerunClaimResult, err error) {
	completed := false
	defer func() {
		if !completed {
			_ = recover()
			out = nil
			err = errors.New("protected rerun claim outcome unknown")
		}
	}()
	out, err = store.ClaimAndFetchCopilotRerun(ctx, in)
	completed = true
	return
}

func requireRerunConfidentialTransport(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return status.Error(codes.Unauthenticated, "confidential Agent transport required")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || !info.State.HandshakeComplete || info.State.Version < tls.VersionTLS12 || info.State.CipherSuite == 0 {
		return status.Error(codes.Unauthenticated, "confidential Agent transport required")
	}

	return nil
}
