// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type rerunBridgeDatabase struct{ db *gorm.DB }

func (d rerunBridgeDatabase) Database() *gorm.DB { return d.db }

type rerunBridgeFixture struct {
	db           *gorm.DB
	repositories *repo.Repositories
	bridge       *agentRerunClaimBridge
	ctx          context.Context
	request      taskqueue.RerunClaimRequest
	saved        *model.CopilotRerunIntent
	original     []byte
}

func newRerunBridgeFixture(t *testing.T) *rerunBridgeFixture {
	t.Helper()
	db, scope, snapshot, _, _, key, policy := rerunReviewFixture(t)
	scope.Run.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
	scope.Run.InboxOrigin = model.RunInboxOriginVerifiedManual
	scope.Run.InboxActorID = scope.Run.TriggeredBy
	require.NoError(t, db.AutoMigrate(&model.CopilotDefinitionSnapshot{}, &model.CopilotRerunIntent{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}, &model.StorageConfig{}))
	for _, row := range []any{scope.Run, scope.Job, snapshot} {
		require.NoError(t, db.Create(row).Error)
	}
	r := repo.NewRepositories(rerunBridgeDatabase{db}, nil)
	authorize := func(ctx context.Context, view *repo.Repositories, s *repo.CopilotRerunScope, owner, action string) error {
		return authorizeRetryActor(ctx, view, owner, s.Project.ProjectID, s.Pipeline.PipelineID, "")
	}
	review, err := newCopilotRerunReviewBuilder(policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	saved, err := r.CopilotRerunPrepare(context.Background(), repo.CopilotRerunPrepareInput{OwnerUserID: "actor", ProjectID: scope.Project.ProjectID, OriginalJobRunID: scope.Job.JobRunID, RequestID: uuid.NewString(), SnapshotDigest: snapshot.BindingDigest, ExpiresAt: time.Now().Add(5 * time.Minute)}, authorize, review)
	require.NoError(t, err)
	saved, err = r.CopilotRerunConfirm(context.Background(), repo.CopilotRerunConfirmInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedVersion: saved.Version, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest}, authorize, review)
	require.NoError(t, err)
	now := time.Now()
	caps, err := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Agent{AgentID: "bridge-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}).Error)
	lease, err := r.CopilotRerunAcquireLease(context.Background(), saved.ScheduleID, "bridge-controller", time.Minute)
	require.NoError(t, err)
	builder, err := newCopilotRerunSealBuilder(policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	sealed, err := r.CopilotRerunSeal(context.Background(), *lease, repo.CopilotRerunSealInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, AgentID: "bridge-agent", SealID: uuid.NewString(), ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 1, ExpectedScheduleVersion: 2}, authorize, builder)
	require.NoError(t, err)
	b := sealed.Binding
	digest, err := b.Digest()
	require.NoError(t, err)
	var schedule model.CopilotRerunSchedule
	require.NoError(t, db.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
	_, original, err := openCopilotRerunDispatchBytes(saved.ProjectID, saved.PipelineID, &b, schedule.PayloadCiphertext, key)
	require.NoError(t, err)
	bridge, err := newAgentRerunClaimBridge(r, policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}}})
	ctx = agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "bridge-agent"})
	request := taskqueue.RerunClaimRequest{Reference: taskqueue.RerunClaimReference{AgentID: "bridge-agent", PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: b.SealID, SealDigest: digest, PayloadDigest: b.PayloadDigest}, ClaimID: uuid.NewString(), AdmissionNonce: bytes.Repeat([]byte("n"), 32), ArtifactStorageID: "bridge-storage", ArtifactStorageFingerprint: artifactutil.StorageFingerprint("s3", "https://storage.invalid", "bucket", "region", "base", true)}
	require.NoError(t, db.Create(&model.StorageConfig{StorageID: request.ArtifactStorageID, StorageType: "s3", IsEnabled: 1, Config: []byte(`{"endpoint":"https://storage.invalid","bucket":"bucket","region":"region","basePath":"base","useTLS":true}`)}).Error)
	return &rerunBridgeFixture{db, r, bridge, ctx, request, saved, original}
}
func TestRerunBridgeActualTransactionAndCurrentReplay(t *testing.T) {
	f := newRerunBridgeFixture(t)
	first, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	require.True(t, first.Receipt.Matches(f.request))
	require.Equal(t, f.original, first.PayloadJSON)
	second, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	require.Equal(t, first, second)
	var job model.JobRun
	require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&job).Error)
	require.Equal(t, f.request.ClaimID, job.ClaimID)
	require.Equal(t, f.request.ArtifactStorageID, job.ArtifactStorageID)
	require.Equal(t, f.request.ArtifactStorageFingerprint, job.ArtifactStorageFingerprint)
	require.Equal(t, "s3", job.ArtifactStorageType)
	require.Empty(t, job.InputManifest)
	require.Empty(t, job.RetryApprovalID)
	require.Empty(t, job.CurrentGenerationID)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
	denied, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.Error(t, err)
	require.Nil(t, denied)
	require.NotContains(t, err.Error(), string(f.request.AdmissionNonce))
	require.NotContains(t, err.Error(), "original-private-command")
}
func TestRerunBridgeRejectsChangedClaimAndLiveAuthority(t *testing.T) {
	for _, kind := range []string{"wrong-agent", "nonce", "claim", "storage", "storage-disabled", "grant", "candidate", "policy", "seal", "cipher", "plaintext", "missing-auth", "short-nonce"} {
		t.Run(kind, func(t *testing.T) {
			f := newRerunBridgeFixture(t)
			_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
			require.NoError(t, err)
			in, ctx := f.request, f.ctx
			switch kind {
			case "wrong-agent":
				ctx = agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "other"})
			case "nonce":
				in.AdmissionNonce = bytes.Repeat([]byte("x"), 32)
			case "short-nonce":
				in.AdmissionNonce = []byte("short")
			case "claim":
				in.ClaimID = uuid.NewString()
			case "storage":
				in.ArtifactStorageFingerprint = strings.Repeat("e", 64)
			case "storage-disabled":
				require.NoError(t, f.db.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("is_enabled", 0).Error)
			case "grant":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			case "candidate":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
			case "policy":
				f.bridge, err = newAgentRerunClaimBridge(f.repositories, &retryapproval.Policy{ID: "changed-policy", Candidates: []string{"actor", "reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}, rerunSnapshotPolicy(), NewSecretService(nil))
				require.NoError(t, err)
			case "seal":
				in.Reference.SealDigest = "v1:" + strings.Repeat("e", 64)
			case "cipher":
				require.NoError(t, f.db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", in.Reference.ScheduleID).Update("payload_ciphertext", []byte("bad-cipher")).Error)
			case "plaintext":
				ctx = agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: in.Reference.AgentID})
			case "missing-auth":
				ctx = peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}}})
			}
			out, err := f.bridge.ClaimAndFetchCopilotRerun(ctx, in)
			require.Error(t, err)
			require.Nil(t, out)
			var job model.JobRun
			require.NoError(t, f.db.Where("job_run_id = ?", in.Reference.JobRunID).First(&job).Error)
			require.Equal(t, f.request.ClaimID, job.ClaimID)
		})
	}
}

type rerunBridgeLostPool struct {
	gorm.ConnPool
	pool *sql.DB
	lose *atomic.Bool
}

func (p rerunBridgeLostPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &rerunBridgeLostTx{tx, p.lose}, nil
}

type rerunBridgeLostTx struct {
	*sql.Tx
	lose *atomic.Bool
}

func (t *rerunBridgeLostTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if t.lose.CompareAndSwap(true, false) {
		return errors.New("synthetic lost commit ACK")
	}
	return nil
}
func TestRerunBridgeActualLostCommitAcknowledgement(t *testing.T) {
	f := newRerunBridgeFixture(t)
	pool, err := f.db.DB()
	require.NoError(t, err)
	lose := &atomic.Bool{}
	lose.Store(true)
	wrapped := f.db.Session(&gorm.Session{NewDB: true, Initialized: true})
	wrapped.Statement.ConnPool = rerunBridgeLostPool{pool, pool, lose}
	uncertain := *f.bridge
	uncertain.repositories = repo.NewRepositories(rerunBridgeDatabase{wrapped}, nil)
	got, err := uncertain.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.ErrorIs(t, err, errRerunBridgeUnknown)
	require.NotErrorIs(t, err, taskqueue.ErrRerunClaimRejected)
	require.Nil(t, got)
	var job model.JobRun
	require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&job).Error)
	require.Equal(t, f.request.ClaimID, job.ClaimID)
	// Recovery uses the same claim/nonce with fresh authorization, never an inspector.
	replay, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	require.True(t, replay.Receipt.Matches(f.request))
	require.Equal(t, f.original, replay.PayloadJSON)
}
func TestRerunBridgeBeforeClaimDenialAndRollback(t *testing.T) {
	for _, kind := range []string{"owner", "candidate", "storage", "sql-write"} {
		t.Run(kind, func(t *testing.T) {
			f := newRerunBridgeFixture(t)
			switch kind {
			case "owner":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			case "candidate":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
			case "storage":
				require.NoError(t, f.db.Model(&model.StorageConfig{}).Where("storage_id = ?", f.request.ArtifactStorageID).Update("is_enabled", 0).Error)
			case "sql-write":
				require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("bridge-fail-schedule", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
						tx.AddError(errors.New("private SQL must not escape"))
					}
				}))
				t.Cleanup(func() { _ = f.db.Callback().Update().Remove("bridge-fail-schedule") })
			}
			got, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
			require.Error(t, err)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "private SQL")
			if kind == "sql-write" {
				require.ErrorIs(t, err, errRerunBridgeUnknown)
			} else {
				require.ErrorIs(t, err, taskqueue.ErrRerunClaimRejected)
			}
			var gate model.CopilotRerunAdmissionGate
			var job model.JobRun
			require.NoError(t, f.db.Where("gate_id = ?", f.saved.GateID).First(&gate).Error)
			require.Empty(t, gate.ClaimID)
			require.Empty(t, gate.AdmissionNonceDigest)
			require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&job).Error)
			require.Empty(t, job.ClaimID)
			require.Empty(t, job.ArtifactStorageID)
		})
	}
}
func TestRerunBridgeDefaultNilAndErrorClasses(t *testing.T) {
	_, store, req, ctx := rerunClaimTestFixture(t)
	ordinary := NewAgentService(nil, nil, nil, store)
	require.Nil(t, ordinary.rerunClaim)
	out, err := NewAgentServiceImpl(ordinary, nil).ClaimAndFetchCopilotRerun(ctx, req)
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Nil(t, out)
	require.Zero(t, store.calls)
	for _, err := range []error{repo.ErrCopilotRerunConflict, repo.ErrCopilotRerunUnavailable, repo.ErrCopilotRerunForbidden, errors.New("private nonce/payload")} {
		require.Equal(t, errRerunBridgeUnknown, rerunBridgeError(err))
	}
	require.Equal(t, taskqueue.ErrRerunClaimRejected, rerunBridgeError(errors.Join(repo.ErrCopilotRerunClaimRejected, repo.ErrCopilotRerunForbidden)))
}
func TestRerunBridgeConstructorFailsClosed(t *testing.T) {
	f := newRerunSealFixture(t)
	r := repo.NewRepositories(rerunBridgeDatabase{f.db}, nil)
	for _, kind := range []string{"repos", "policy", "runtime", "key"} {
		t.Run(kind, func(t *testing.T) {
			repositories, policy, runtime, key := r, f.policy, rerunSnapshotPolicy(), f.key
			switch kind {
			case "repos":
				repositories = nil
			case "policy":
				policy = nil
			case "runtime":
				runtime.Enabled = false
			case "key":
				key = nil
			}
			b, err := newAgentRerunClaimBridge(repositories, policy, runtime, key)
			require.Error(t, err)
			require.Nil(t, b)
		})
	}
}

func TestRerunBridgePrivateSlotActualRepositoryRPC(t *testing.T) {
	f := newRerunBridgeFixture(t)
	service := NewAgentServiceImpl(&AgentService{rerunClaim: f.bridge}, nil)
	r := f.request.Reference
	request := &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{AgentId: r.AgentID, PipelineId: r.PipelineID, RunId: r.RunID, JobRunId: r.JobRunID, GateId: r.GateID, ScheduleId: r.ScheduleID, SealId: r.SealID, SealDigest: r.SealDigest, PayloadDigest: r.PayloadDigest}, ClaimId: f.request.ClaimID, AdmissionNonce: bytes.Clone(f.request.AdmissionNonce), ArtifactStorageId: f.request.ArtifactStorageID, ArtifactStorageFingerprint: f.request.ArtifactStorageFingerprint}
	first, err := service.ClaimAndFetchCopilotRerun(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, f.original, first.PayloadJson)
	require.Equal(t, f.request.ClaimID, first.Receipt.ClaimId)
	require.Equal(t, f.request.ArtifactStorageID, first.Receipt.ArtifactStorageId)
	require.Equal(t, f.request.ArtifactStorageFingerprint, first.Receipt.ArtifactStorageFingerprint)
	require.Equal(t, "s3", first.Receipt.ArtifactStorageType)
	require.Equal(t, f.request.AdmissionNonce, request.AdmissionNonce)
	replay, err := service.ClaimAndFetchCopilotRerun(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, first.PayloadJson, replay.PayloadJson)
	require.Equal(t, first.Receipt.ClaimedAtUnixMilli, replay.Receipt.ClaimedAtUnixMilli)
}
