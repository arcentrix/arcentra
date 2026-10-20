// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	tracectx "github.com/arcentrix/arcentra/pkg/trace/context"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type rerunClaimStore struct {
	repo.IJobRunRepository
	calls                         int
	result                        *taskqueue.RerunClaimResult
	err                           error
	borrowed                      []byte
	panicValue                    any
	storageID, storageFingerprint string
}

func (s *rerunClaimStore) ClaimAndFetchCopilotRerun(_ context.Context, in taskqueue.RerunClaimRequest) (*taskqueue.RerunClaimResult, error) {
	s.calls++
	s.storageID, s.storageFingerprint = in.ArtifactStorageID, in.ArtifactStorageFingerprint
	s.borrowed = in.AdmissionNonce
	if s.panicValue != nil {
		panic(s.panicValue)
	}
	return s.result, s.err
}
func rerunClaimTestFixture(t *testing.T) (*AgentServiceImpl, *rerunClaimStore, *agentv1.ClaimAndFetchCopilotRerunRequest, context.Context) {
	t.Helper()
	f := newRerunSealFixture(t)
	sealed, err := f.build(t)
	require.NoError(t, err)
	require.Equal(t, repo.CopilotRerunMaxDefinitionBytes, taskqueue.MaxRerunClaimPayloadBytes)
	body, err := json.Marshal(sealed.Payload)
	require.NoError(t, err)
	digest, err := sealed.Binding.Digest()
	require.NoError(t, err)
	ref := taskqueue.RerunClaimReference{AgentID: f.agent.AgentID, PipelineID: f.intent.PipelineID, RunID: f.ids.RunID, JobRunID: f.ids.JobRunID, GateID: f.ids.GateID, ScheduleID: f.ids.ScheduleID, SealID: f.sealID, SealDigest: digest, PayloadDigest: sealed.Binding.PayloadDigest}
	request := &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{AgentId: ref.AgentID, PipelineId: ref.PipelineID, RunId: ref.RunID, JobRunId: ref.JobRunID, GateId: ref.GateID, ScheduleId: ref.ScheduleID, SealId: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest}, ClaimId: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", AdmissionNonce: bytes.Repeat([]byte("N"), 32)}
	store := &rerunClaimStore{result: &taskqueue.RerunClaimResult{Receipt: taskqueue.RerunClaimReceipt{Reference: ref, ClaimID: request.ClaimId, ClaimedAtUnixMilli: 12345}, PayloadJSON: body}}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}}})
	ctx = agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: ref.AgentID})
	return &AgentServiceImpl{agentService: &AgentService{jobRunRepo: store, rerunClaim: store}}, store, request, ctx
}

type rerunFakeTLS struct{}

func (rerunFakeTLS) AuthType() string { return "tls" }
func TestRerunClaimTransportPrechecks(t *testing.T) {
	svc, store, request, valid := rerunClaimTestFixture(t)
	for name, test := range map[string]struct {
		ctx  context.Context
		edit func(*agentv1.ClaimAndFetchCopilotRerunRequest)
		code codes.Code
	}{
		"plaintext":     {context.Background(), nil, codes.Unauthenticated},
		"forwarded":     {metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-forwarded-proto", "https", "agent-id", request.Reference.AgentId)), nil, codes.Unauthenticated},
		"fake-peer":     {peer.NewContext(context.Background(), &peer.Peer{AuthInfo: rerunFakeTLS{}}), nil, codes.Unauthenticated},
		"incomplete":    {peer.NewContext(valid, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}}}), nil, codes.Unauthenticated},
		"old-tls":       {peer.NewContext(valid, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS11, CipherSuite: tls.TLS_RSA_WITH_AES_128_CBC_SHA}}}), nil, codes.Unauthenticated},
		"missing-auth":  {peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}}}), nil, codes.Unauthenticated},
		"wrong-agent":   {valid, func(r *agentv1.ClaimAndFetchCopilotRerunRequest) { r.Reference.AgentId = "other" }, codes.PermissionDenied},
		"short-nonce":   {valid, func(r *agentv1.ClaimAndFetchCopilotRerunRequest) { r.AdmissionNonce = r.AdmissionNonce[:31] }, codes.InvalidArgument},
		"long-nonce":    {valid, func(r *agentv1.ClaimAndFetchCopilotRerunRequest) { r.AdmissionNonce = append(r.AdmissionNonce, 'N') }, codes.InvalidArgument},
		"missing-nonce": {valid, func(r *agentv1.ClaimAndFetchCopilotRerunRequest) { r.AdmissionNonce = nil }, codes.InvalidArgument},
		"alias-id":      {valid, func(r *agentv1.ClaimAndFetchCopilotRerunRequest) { r.ClaimId = strings.ToUpper(r.ClaimId) }, codes.InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			r := proto.Clone(request).(*agentv1.ClaimAndFetchCopilotRerunRequest)
			if test.edit != nil {
				test.edit(r)
			}
			response, err := svc.ClaimAndFetchCopilotRerun(test.ctx, r)
			require.Nil(t, response)
			require.Equal(t, test.code, status.Code(err))
			require.Zero(t, store.calls)
		})
	}
}
func TestRerunClaimTransportExactReceiptAndUnavailable(t *testing.T) {
	svc, store, request, ctx := rerunClaimTestFixture(t)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		c := peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: version, CipherSuite: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}})
		response, err := svc.ClaimAndFetchCopilotRerun(c, request)
		require.NoError(t, err)
		require.Equal(t, store.result.PayloadJSON, response.PayloadJson)
		require.True(t, proto.Equal(request.Reference, response.Receipt.Reference))
		require.Equal(t, request.ClaimId, response.Receipt.ClaimId)
		require.Equal(t, int64(12345), response.Receipt.ClaimedAtUnixMilli)
		require.Equal(t, make([]byte, 32), store.borrowed)
		require.Equal(t, bytes.Repeat([]byte("N"), 32), request.AdmissionNonce)
		response.PayloadJson[0] = 'x'
		require.Equal(t, byte('{'), store.result.PayloadJSON[0])
	}
	svc.agentService.rerunClaim = nil
	response, err := svc.ClaimAndFetchCopilotRerun(ctx, request)
	require.Nil(t, response)
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
func TestRerunClaimTransportRejectsMismatchedReceipt(t *testing.T) {
	svc, store, request, ctx := rerunClaimTestFixture(t)
	original := *store.result
	for name, change := range map[string]func(*taskqueue.RerunClaimResult){
		"agent":           func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.AgentID = "wrong" },
		"pipeline":        func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.PipelineID = "wrong" },
		"run":             func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.RunID = "wrong" },
		"job":             func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.JobRunID = "wrong" },
		"gate":            func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.GateID = "wrong" },
		"schedule":        func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.ScheduleID = "wrong" },
		"seal":            func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.SealID = "wrong" },
		"seal-digest":     func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.SealDigest = "wrong" },
		"payload-digest":  func(r *taskqueue.RerunClaimResult) { r.Receipt.Reference.PayloadDigest = "wrong" },
		"claim":           func(r *taskqueue.RerunClaimResult) { r.Receipt.ClaimID = "wrong" },
		"time":            func(r *taskqueue.RerunClaimResult) { r.Receipt.ClaimedAtUnixMilli = 0 },
		"bytes":           func(r *taskqueue.RerunClaimResult) { r.PayloadJSON = append(bytes.Clone(r.PayloadJSON), ' ') },
		"missing-payload": func(r *taskqueue.RerunClaimResult) { r.PayloadJSON = nil },
	} {
		t.Run(name, func(t *testing.T) {
			v := original
			change(&v)
			store.result = &v
			response, err := svc.ClaimAndFetchCopilotRerun(ctx, request)
			require.Nil(t, response)
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}
func TestRerunClaimTransportErrorsAndTraceArePrivate(t *testing.T) {
	svc, store, request, ctx := rerunClaimTestFixture(t)
	const canary = "private-rerun-payload-error-canary"
	store.err = errors.New(canary + string(request.AdmissionNonce))
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	_, err := tracectx.UnaryServerInterceptor()(ctx, request, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_ClaimAndFetchCopilotRerun_FullMethodName}, func(c context.Context, r any) (any, error) {
		return svc.ClaimAndFetchCopilotRerun(c, r.(*agentv1.ClaimAndFetchCopilotRerunRequest))
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), canary)
	require.NotContains(t, err.Error(), string(request.AdmissionNonce))
	require.NotEmpty(t, recorder.Ended())
	for _, span := range recorder.Ended() {
		s := fmt.Sprint(span.Status(), span.Attributes(), span.Events())
		require.NotContains(t, s, canary)
		require.NotContains(t, s, string(request.AdmissionNonce))
	}
}

func TestRerunClaimTransportNoncanonicalPayload(t *testing.T) {
	svc, store, request, ctx := rerunClaimTestFixture(t)
	body := bytes.Clone(store.result.PayloadJSON)
	for _, candidate := range [][]byte{append(bytes.Clone(body), ' '), append([]byte(`{"agentId":"shadow",`), body[1:]...), []byte(`{} {}`)} {
		store.result.PayloadJSON = candidate
		request.Reference.PayloadDigest = taskqueue.RerunPayloadBytesDigest(candidate)
		store.result.Receipt.Reference.PayloadDigest = request.Reference.PayloadDigest
		response, err := svc.ClaimAndFetchCopilotRerun(ctx, request)
		require.Nil(t, response)
		require.Equal(t, codes.Unavailable, status.Code(err))
	}
}

func TestRerunClaimTransportUnknownAndProvenRejection(t *testing.T) {
	svc, store, request, ctx := rerunClaimTestFixture(t)
	for _, failure := range []error{errors.New("unknown commit"), fmt.Errorf("wrapped: %w", taskqueue.ErrRerunClaimRejected)} {
		store.err = failure
		_, err := svc.ClaimAndFetchCopilotRerun(ctx, request)
		want := codes.Unavailable
		if errors.Is(failure, taskqueue.ErrRerunClaimRejected) {
			want = codes.FailedPrecondition
		}
		require.Equal(t, want, status.Code(err))
		require.Equal(t, make([]byte, 32), store.borrowed)
	}
	store.err = nil
	store.panicValue = "private-payload-panic" + string(request.AdmissionNonce)
	response, err := svc.ClaimAndFetchCopilotRerun(ctx, request)
	require.Nil(t, response)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), "private-payload-panic")
	require.NotContains(t, err.Error(), string(request.AdmissionNonce))
	require.Equal(t, make([]byte, 32), store.borrowed)
}

func TestRerunClaimStorageReceiptBinding(t *testing.T) {
	for _, kind := range []string{"valid", "partial", "id", "fingerprint", "type"} {
		t.Run(kind, func(t *testing.T) {
			svc, store, req, ctx := rerunClaimTestFixture(t)
			req.ArtifactStorageId = "current-store"
			req.ArtifactStorageFingerprint = strings.Repeat("a", 64)
			store.result.Receipt.ArtifactStorageID = req.ArtifactStorageId
			store.result.Receipt.ArtifactStorageFingerprint = req.ArtifactStorageFingerprint
			store.result.Receipt.ArtifactStorageType = "s3"
			switch kind {
			case "partial":
				req.ArtifactStorageFingerprint = ""
			case "id":
				store.result.Receipt.ArtifactStorageID = "old-store"
			case "fingerprint":
				store.result.Receipt.ArtifactStorageFingerprint = strings.Repeat("b", 64)
			case "type":
				store.result.Receipt.ArtifactStorageType = "unknown"
			}
			out, err := svc.ClaimAndFetchCopilotRerun(ctx, req)
			if kind == "valid" {
				require.NoError(t, err)
				require.Equal(t, req.ArtifactStorageId, out.Receipt.ArtifactStorageId)
				require.Equal(t, req.ArtifactStorageFingerprint, out.Receipt.ArtifactStorageFingerprint)
				require.Equal(t, "s3", out.Receipt.ArtifactStorageType)
				require.Equal(t, req.ArtifactStorageId, store.storageID)
				require.Equal(t, req.ArtifactStorageFingerprint, store.storageFingerprint)
			} else {
				require.Error(t, err)
				require.Nil(t, out)
			}
			if kind == "partial" {
				require.Zero(t, store.calls)
			}
		})
	}
}

// NewAgentRerunClaimTestService exists only in the test variant, for external TLS tests.
func NewAgentRerunClaimTestService(store CopilotRerunClaimRepository) *AgentServiceImpl {
	return NewAgentServiceImpl(&AgentService{rerunClaim: store}, nil)
}
