// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type rerunFetchClient struct {
	agentv1.AgentServiceClient
	mu               sync.Mutex
	requests         []*agentv1.ClaimAndFetchCopilotRerunRequest
	payload          []byte
	failures         int
	mutate           func(*agentv1.ClaimAndFetchCopilotRerunResponse)
	reports          int
	generationClaims []string
}

func (c *rerunFetchClient) ClaimAndFetchCopilotRerun(_ context.Context, r *agentv1.ClaimAndFetchCopilotRerunRequest, _ ...grpc.CallOption) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, proto.Clone(r).(*agentv1.ClaimAndFetchCopilotRerunRequest))
	if c.failures > 0 {
		c.failures--
		return nil, status.Error(codes.Unavailable, "unknown fixture commit")
	}
	out := &agentv1.ClaimAndFetchCopilotRerunResponse{Receipt: &agentv1.CopilotRerunClaimReceipt{Reference: proto.Clone(r.Reference).(*agentv1.CopilotRerunClaimReference), ClaimId: r.ClaimId, ClaimedAtUnixMilli: 12345, ArtifactStorageId: r.ArtifactStorageId, ArtifactStorageFingerprint: r.ArtifactStorageFingerprint}, PayloadJson: bytes.Clone(c.payload)}
	if r.ArtifactStorageId != "" {
		out.Receipt.ArtifactStorageType = "s3"
	}
	if c.mutate != nil {
		c.mutate(out)
	}
	return out, nil
}
func (c *rerunFetchClient) ReportJobRunStatus(context.Context, *agentv1.ReportJobRunStatusRequest, ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports++
	return nil, status.Error(codes.Unimplemented, "test must not generic claim")
}
func (c *rerunFetchClient) ReconcileJobGeneration(_ context.Context, r *agentv1.ReconcileJobGenerationRequest, _ ...grpc.CallOption) (*agentv1.ReconcileJobGenerationResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var transition taskpayload.JobGenerationTransition
	if json.Unmarshal([]byte(r.TransitionJson), &transition) != nil {
		return nil, status.Error(codes.InvalidArgument, "fixture transition")
	}
	c.generationClaims = append(c.generationClaims, transition.Identity.ClaimID)
	return nil, status.Error(codes.FailedPrecondition, "fixture stops before allocation")
}
func rerunFetchFixture(t *testing.T) (context.Context, *config.AgentConfig, *agentgrpc.ClientWrapper, *rerunFetchClient, *rerunIncarnation, *rerunAdmissionHandle, taskpayload.RerunClaimReference) {
	t.Helper()
	conf, p, r, _ := fakeGenerationFixture(t, 1)
	p.PipelineRunID = id.GetUUID()
	p.JobRunID = id.GetUUID()
	p.RetryApprovalRequired = true
	p.RetryApprovalTimeoutSeconds = 60
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
	conf.Agent.Sandbox.Containerd.TerminalRerunEnabled = true
	source, err := sourceprovenance.NewBinding(p.PipelineID, p.PipelineRunID, p.JobRunID, "git", "https://example.com/repo.git", "main")
	require.NoError(t, err)
	p.Source = &taskpayload.SourcePayload{Type: "git", Repo: source.Repository, Branch: source.Ref, BindingDigest: source.Digest}
	p.SourceExpectation = &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: id.GetUUID(), SealID: id.GetUUID(), ExpectedSourceCommit: strings.Repeat("a", 40)}
	p.Runtime.BindingDigest, err = taskpayload.RuntimeBindingDigest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime)
	require.NoError(t, err)
	bindFakeGenerationPlan(t, p)
	body, err := json.Marshal(p)
	require.NoError(t, err)
	ref := taskpayload.RerunClaimReference{AgentID: p.AgentID, PipelineID: p.PipelineID, RunID: p.PipelineRunID, JobRunID: p.JobRunID, GateID: id.GetUUID(), ScheduleID: p.SourceExpectation.ScheduleID, SealID: p.SourceExpectation.SealID, SealDigest: "v1:" + strings.Repeat("b", 64), PayloadDigest: taskpayload.RerunPayloadBytesDigest(body)}
	require.NoError(t, ref.Validate())
	_, err = taskpayload.ParseRerunClaimPayload(ref, body)
	require.NoError(t, err)
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	fake := &rerunFetchClient{payload: body}
	client.AgentClient = fake
	manager, err := newRerunIncarnation(8)
	require.NoError(t, err)
	h, err := manager.reserve(rerunBinding(ref))
	require.NoError(t, err)
	return withOfflineJobRuntime(context.Background(), r), conf, client, fake, manager, h, ref
}
func TestRerunHandlerLostResponseKeepsIdentityAndOnceOnlyToken(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	fake.failures = 2
	p, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.NoError(t, err)
	require.Len(t, fake.requests, 3)
	for _, r := range fake.requests {
		require.Equal(t, fake.requests[0].ClaimId, r.ClaimId)
		require.Equal(t, fake.requests[0].AdmissionNonce, r.AdmissionNonce)
	}
	token, err := m.takeStartToken(h)
	require.NoError(t, err)
	claim, err := token.consume(p)
	require.NoError(t, err)
	require.Equal(t, fake.requests[0].ClaimId, claim)
	fixed := context.WithValue(ctx, receiptJobClaimContextKey{}, receiptJobClaim{runID: p.JobRunID, claimID: claim})
	fixed = withJobExecutionClaim(fixed, p.JobRunID)
	require.Equal(t, claim, executionClaimID(fixed, "job", p.JobRunID))
	require.Equal(t, claim, fixed.Value(jobExecutionClaimContextKey{}))
	_, err = token.consume(p)
	require.Error(t, err)
	_, err = m.takeStartToken(h)
	require.Error(t, err)
	require.NoError(t, m.finish(h, false))
	_, err = fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.Error(t, err)
	require.Len(t, fake.requests, 3)
}
func TestRerunHandlerPolicyTLSAndPayloadFailBeforeStart(t *testing.T) {
	for _, name := range []string{"plaintext", "policy", "wrong-seal", "wrong-claim", "changed-bytes", "nil-response-body"} {
		t.Run(name, func(t *testing.T) {
			ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
			switch name {
			case "plaintext":
				client = &agentgrpc.ClientWrapper{AgentClient: fake}
			case "policy":
				conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = false
			case "wrong-seal":
				fake.mutate = func(r *agentv1.ClaimAndFetchCopilotRerunResponse) { r.Receipt.Reference.SealId = id.GetUUID() }
			case "wrong-claim":
				fake.mutate = func(r *agentv1.ClaimAndFetchCopilotRerunResponse) { r.Receipt.ClaimId = id.GetUUID() }
			case "changed-bytes":
				fake.payload = append(fake.payload, ' ')
			case "nil-response-body":
				fake.payload = nil
			}
			_, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
			require.Error(t, err)
			_, err = m.takeStartToken(h)
			require.Error(t, err)
			if name == "plaintext" {
				require.Empty(t, fake.requests)
			}
			require.Zero(t, fake.reports)
		})
	}
}
func TestRerunHandlerConcurrentDuplicateAndRestart(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	owners := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fetchRerunClaim(ctx, conf, client, m, h, ref); err == nil {
				if _, err := m.takeStartToken(h); err == nil {
					mu.Lock()
					owners++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, owners)
	original := fake.requests[0]
	restarted, err := newRerunIncarnation(8)
	require.NoError(t, err)
	newHandle, err := restarted.reserve(rerunBinding(ref))
	require.NoError(t, err)
	// Server fixture returns old committed receipt: a new worker cannot adopt it.
	fake.mutate = func(r *agentv1.ClaimAndFetchCopilotRerunResponse) { r.Receipt.ClaimId = original.ClaimId }
	_, err = fetchRerunClaim(ctx, conf, client, restarted, newHandle, ref)
	require.Error(t, err)
	last := fake.requests[len(fake.requests)-1]
	require.NotEqual(t, original.ClaimId, last.ClaimId)
	require.NotEqual(t, original.AdmissionNonce, last.AdmissionNonce)
	_, err = restarted.takeStartToken(newHandle)
	require.Error(t, err)
}
func TestRerunHandlerUnknownBudgetAndGenericBypass(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	fake.failures = 100
	_, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.Error(t, err)
	require.Len(t, fake.requests, 3)
	_, err = m.takeStartToken(h)
	require.Error(t, err)
	p, err := taskpayload.ParseRerunClaimPayload(ref, fake.payload)
	require.NoError(t, err)
	require.Error(t, executeJobRun(ctx, conf, client, nil, p))
	require.Error(t, executeJobRunBody(ctx, conf, client, nil, p, nil))
	require.Zero(t, fake.reports)
	// Notification ACK on missing readiness/outbox is not a Job outcome. It does
	// not invoke RPC or delete any outbox evidence, and cannot enter Nova retry.
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	handler := newWorkerHandler(ctx, conf, client, nil, nil, m)
	require.NoError(t, handler.ProcessTask(ctx, &nova.Task{Type: taskpayload.TaskTypeCopilotRerunReference, Payload: body}))
	require.Len(t, fake.requests, 3)
}
func TestRerunHandlerTokenForgeryAndFailedConsumeStayClosed(t *testing.T) {
	ctx, conf, client, _, m, h, ref := rerunFetchFixture(t)
	p, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.NoError(t, err)
	token, err := m.takeStartToken(h)
	require.NoError(t, err)
	forged := &rerunStartToken{owner: m, handle: h}
	_, err = forged.consume(p)
	require.Error(t, err)
	changed := *p
	changed.JobName += "changed"
	_, err = token.consume(&changed)
	require.Error(t, err)
	_, err = token.consume(p)
	require.Error(t, err)
}

func TestRerunHandlerPrivateBodySkipsGenericClaimBeforeAllocation(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	p, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.NoError(t, err)
	token, err := m.takeStartToken(h)
	require.NoError(t, err)
	require.Error(t, executeJobRunBody(ctx, conf, client, nil, p, token))
	require.Zero(t, fake.reports)
	require.Equal(t, []string{fake.requests[0].ClaimId}, fake.generationClaims)
	require.Zero(t, offlineRuntimeFromContext(ctx).(*fakeGenerationRuntime).allocations)
	require.NoError(t, m.finish(h, false))
	require.Error(t, executeJobRunBody(ctx, conf, client, nil, p, token))
	require.Len(t, fake.generationClaims, 1)
}
func TestRerunHandlerEveryReceiptIdentityIsExact(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	fields := []string{"AgentId", "PipelineId", "RunId", "JobRunId", "GateId", "ScheduleId", "SealId", "SealDigest", "PayloadDigest"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			fake.mutate = func(r *agentv1.ClaimAndFetchCopilotRerunResponse) {
				reflect.ValueOf(r.Receipt.Reference).Elem().FieldByName(field).SetString("changed")
			}
			_, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
			require.Error(t, err)
			_, err = m.takeStartToken(h)
			require.Error(t, err)
		})
	}
}

func TestRerunHandlerNotificationBudgetPreservesOutboxAndPanicPrivacy(t *testing.T) {
	ctx, conf, client, fake, m, _, ref := rerunFetchFixture(t)
	conf.Grpc.ServerAddr = "127.0.0.1:1"
	outbox, err := newTerminalOutbox(ctx, t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.close() })
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	fake.failures = 100
	handler := newWorkerHandler(ctx, conf, client, nil, outbox, m)
	require.NoError(t, handler.ProcessTask(ctx, &nova.Task{Type: taskpayload.TaskTypeCopilotRerunReference, Payload: body}))
	require.Len(t, fake.requests, 3)
	original := fake.requests[0]
	fake.failures = 0
	fake.mutate = func(*agentv1.ClaimAndFetchCopilotRerunResponse) { panic("private-payload-panic") }
	require.ErrorIs(t, handleRerunReference(ctx, conf, client, nil, outbox, m, body), errRerunIncarnationUnconfirmed)
	require.Len(t, fake.requests, 4)
	require.Equal(t, original.ClaimId, fake.requests[3].ClaimId)
	require.Equal(t, original.AdmissionNonce, fake.requests[3].AdmissionNonce)
	h, err := m.reserve(rerunBinding(ref))
	require.NoError(t, err)
	_, err = m.takeStartToken(h)
	require.Error(t, err)
	require.NoError(t, outbox.replayBound(ctx, client, conf))
}

func TestRerunHandlerStoragePinSurvivesDefaultSwitchAndUsesNewNamespace(t *testing.T) {
	ctx, conf, client, fake, _, _, ref := rerunFetchFixture(t)
	var output taskpayload.JobRunTaskPayload
	require.NoError(t, json.Unmarshal(fake.payload, &output))
	ids := []string{id.GetUUID(), id.GetUUID(), id.GetUUID()}
	output.Steps = append(output.Steps, taskpayload.StepPayload{Name: "publish", Uses: "builtin:artifacts", Action: "upload", StepIndex: 1, StepRunID: ids[0], AttemptRunIDs: ids, Args: map[string]any{"paths": []string{"out.txt"}}})
	bindFakeGenerationPlan(t, &output)
	body, marshalErr := json.Marshal(&output)
	require.NoError(t, marshalErr)
	fake.payload = body
	ref.PayloadDigest = taskpayload.RerunPayloadBytesDigest(body)
	storageMu.RLock()
	old, oldRef := storageInstance, storageReference
	storageMu.RUnlock()
	t.Cleanup(func() { SetStorage(old, oldRef) })
	first, other := &memoryArtifactStorage{}, &memoryArtifactStorage{}
	reference := artifactTestReference()
	SetStorage(first, reference)
	m, err := newRerunIncarnation(8)
	require.NoError(t, err)
	h, err := reserveRerunStorage(m, ref)
	require.NoError(t, err)
	p, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.NoError(t, err)
	require.Equal(t, reference.StorageID, fake.requests[0].ArtifactStorageId)
	require.Equal(t, reference.Fingerprint, fake.requests[0].ArtifactStorageFingerprint)
	changed := reference
	changed.StorageID = "other-store"
	SetStorage(other, changed)
	_, err = reserveRerunStorage(m, ref)
	require.Error(t, err, "same claim cannot change storage")
	token, err := m.takeStartToken(h)
	require.NoError(t, err)
	_, err = token.consume(p)
	require.NoError(t, err)
	require.Same(t, first, token.artifacts.instance)
	require.Equal(t, reference, token.artifacts.reference)
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "out.txt", "new execution")
	files := &artifactExecutor{job: p, storage: token.artifacts.instance, reference: token.artifacts.reference}
	uploadRequest := artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"out.txt"}})
	uploadRequest.StepRunID = p.Steps[1].StepRunID
	result, err := files.Execute(ctx, uploadRequest)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, first.uploads)
	require.Zero(t, other.uploads)
	require.Len(t, files.results, 1)
	require.True(t, strings.HasPrefix(files.results[0].StorageKey, "artifacts/"+ref.RunID+"/"+ref.JobRunID+"/"+p.Steps[1].StepRunID+"/"))
	require.Equal(t, reference, files.reference)
}
func TestRerunHandlerStorageInvalidAndStaleReceiptNeverStarts(t *testing.T) {
	for _, kind := range []string{"partial", "missing-client", "unbound-client", "stale", "receipt-id", "receipt-type", "receipt-fingerprint"} {
		t.Run(kind, func(t *testing.T) {
			ctx, conf, client, fake, _, _, ref := rerunFetchFixture(t)
			storageMu.RLock()
			old, oldRef := storageInstance, storageReference
			storageMu.RUnlock()
			t.Cleanup(func() { SetStorage(old, oldRef) })
			st := &memoryArtifactStorage{}
			reference := artifactTestReference()
			switch kind {
			case "partial":
				reference.Fingerprint = ""
			case "missing-client":
				st = nil
			case "unbound-client":
				reference = taskpayload.ArtifactReference{}
			}
			if st == nil {
				SetStorage(nil, reference)
			} else {
				SetStorage(st, reference)
			}
			m, err := newRerunIncarnation(8)
			require.NoError(t, err)
			h, err := reserveRerunStorage(m, ref)
			if kind == "partial" || kind == "missing-client" || kind == "unbound-client" {
				require.Error(t, err)
				require.Empty(t, fake.requests)
				return
			}
			require.NoError(t, err)
			fake.mutate = func(r *agentv1.ClaimAndFetchCopilotRerunResponse) {
				switch kind {
				case "stale", "receipt-fingerprint":
					r.Receipt.ArtifactStorageFingerprint = strings.Repeat("b", 64)
				case "receipt-id":
					r.Receipt.ArtifactStorageId = "different"
				case "receipt-type":
					r.Receipt.ArtifactStorageType = "unsupported"
				}
			}
			_, err = fetchRerunClaim(ctx, conf, client, m, h, ref)
			require.Error(t, err)
			_, err = m.takeStartToken(h)
			require.Error(t, err)
		})
	}
}
func TestRerunHandlerArtifactUploadRequiresStorageBeforeStart(t *testing.T) {
	ctx, conf, client, fake, m, _, ref := rerunFetchFixture(t)
	var p taskpayload.JobRunTaskPayload
	require.NoError(t, json.Unmarshal(fake.payload, &p))
	ids := []string{id.GetUUID(), id.GetUUID(), id.GetUUID()}
	p.Steps = append(p.Steps, taskpayload.StepPayload{Name: "publish", Uses: "builtin:artifacts", Action: "upload", StepIndex: 1, StepRunID: ids[0], AttemptRunIDs: ids, Args: map[string]any{"paths": []string{"out.txt"}}})
	bindFakeGenerationPlan(t, &p)
	body, err := json.Marshal(&p)
	require.NoError(t, err)
	fake.payload = body
	ref.PayloadDigest = taskpayload.RerunPayloadBytesDigest(body)
	m, err = newRerunIncarnation(8)
	require.NoError(t, err)
	h, err := m.reserve(rerunBinding(ref))
	require.NoError(t, err)
	_, err = fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.Error(t, err)
	_, err = m.takeStartToken(h)
	require.Error(t, err)
}
