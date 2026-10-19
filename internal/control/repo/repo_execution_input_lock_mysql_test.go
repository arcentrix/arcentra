// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

// A producer's first terminal publication and a consumer's first immutable input
// dispatch capture must contend on the same scope prefix, rather than holding each other's
// Job/Run locks. The artifact is registered only by the real publication method.
func TestStepRetryApprovalMySQLActiveProducerInputLockOrder(t *testing.T) {
	for _, producerFirst := range []bool{true, false} {
		name := "consumer_first"
		if producerFirst {
			name = "producer_first"
		}
		t.Run(name, func(t *testing.T) {
			producer, schema := stepRetryApprovalMySQLFixture(t, 1)
			assertActiveProducerInputLockOrder(t, producer, schema, producerFirst)
		})
	}
}

func TestActiveProducerInputFixtureContracts(t *testing.T) {
	for _, first := range []bool{true, false} {
		t.Run(map[bool]string{true: "producer_first", false: "consumer_first"}[first], func(t *testing.T) {
			fixture := stepRetryApprovalSourceFixture(t, 1)
			installRetryApprovalTestAuthority(t, fixture.generationFixture, nil)
			assertActiveProducerInputLockOrder(t, fixture, "", first)
		})
	}
}

func assertActiveProducerInputLockOrder(t *testing.T, producer *stepAttemptFixture, schema string, producerFirst bool) {
	t.Helper()
	original, artifact, _ := prepareActiveInputProducer(t, producer)
	var count int64
	require.NoError(t, producer.db.Model(&model.ProjectArtifact{}).Count(&count).Error)
	require.Zero(t, count)
	runtime := &taskqueue.RuntimeRequest{Type: original.RuntimeType, Image: original.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: original.RuntimeCPU, Memory: original.RuntimeMemory}}
	digest, err := taskqueue.RuntimeBindingDigest(original.PipelineID, original.PipelineRunID, "input-consumer", original.AgentID, runtime)
	require.NoError(t, err)
	size := artifact.Size
	manifest, err := taskqueue.NewJobInputManifest("", map[string]string{original.JobName + "/" + artifact.Name: artifact.StoragePath}, map[string]taskqueue.ArtifactReference{original.JobName + "/" + artifact.Name: {StorageID: artifact.StorageID, Fingerprint: artifact.StorageFingerprint, SHA256: artifact.SHA256, Size: &size}})
	require.NoError(t, err)
	encoded, err := manifest.Canonical()
	require.NoError(t, err)
	// Use actual dispatch capture and claim methods; no input artifact is
	// registered or pinned on behalf of the consumer fixture.
	consumer := &model.JobRun{JobRunID: "input-consumer", JobName: "input-consumer", PipelineID: original.PipelineID, PipelineRunID: original.PipelineRunID, Status: model.JobRunStatusQueued}
	require.NoError(t, producer.jobs.Create(producer.ctx, consumer))
	runtime.BindingDigest = digest
	publish := func(ctx context.Context) error { return stepAttemptMySQLPublish(producer, ctx, original, artifact) }
	consume := func(ctx context.Context) error {
		return producer.jobs.CaptureRuntimeInputExpectation(ctx, original.PipelineID, original.PipelineRunID, consumer.JobRunID, original.AgentID, nil, runtime, map[string]string{original.JobName + "/" + artifact.Name: artifact.StoragePath}, map[string]taskqueue.ArtifactReference{original.JobName + "/" + artifact.Name: {StorageID: artifact.StorageID, Fingerprint: artifact.StorageFingerprint, SHA256: artifact.SHA256, Size: &size}})
	}
	first, second := publish, consume
	if !producerFirst {
		first, second = consume, publish
	}
	var outcomes []error
	if schema == "" {
		outcomes = []error{first(producer.ctx), second(producer.ctx)}
	} else {
		outcomes = stepAttemptMySQLOrderedRace(t, producer, schema, first, second, true)
	}
	stored, e := producer.jobs.GetByJobRunID(producer.ctx, consumer.JobRunID)
	require.NoError(t, e)
	if producerFirst {
		require.NoError(t, outcomes[0])
		require.NoError(t, outcomes[1])
		require.True(t, stored.SourceExpectationCaptured)
		require.Equal(t, encoded, stored.InputExpectation)
		require.NoError(t, producer.jobs.PatchReportedStatus(producer.ctx, stored, map[string]any{"status": model.JobRunStatusRunning, "claim_id": "input-consumer-claim"}))
		claimed, e := producer.jobs.GetByJobRunID(producer.ctx, consumer.JobRunID)
		require.NoError(t, e)
		identity := generationIdentity(claimed)
		pinned, e := producer.jobs.GetOrRecordJobInputs(producer.ctx, identity, manifest)
		require.NoError(t, e)
		require.NotNil(t, pinned)
		want, e := manifest.Digest(identity)
		require.NoError(t, e)
		require.Equal(t, "pinned", pinned.InputManifestState)
		require.Equal(t, encoded, pinned.InputManifest)
		require.Equal(t, want, pinned.InputManifestDigest)
	} else {
		require.EqualError(t, outcomes[0], "input artifact has no unique registered producer")
		require.NoError(t, outcomes[1])
		require.False(t, stored.SourceExpectationCaptured)
		require.Empty(t, stored.ClaimID)
		require.Equal(t, model.JobRunStatusQueued, stored.Status)
		require.Empty(t, stored.InputExpectation)
		require.Empty(t, stored.InputManifest)
		require.Empty(t, stored.InputManifestDigest)
	}
	assertStepAttemptMySQLPublication(t, producer, model.JobRunStatusSuccess, artifact)
	// No replay is used to turn the rejected consumer-first operation green.
	if schema != "" {
		t.Log("exact Pipeline row contention; one producer terminal receipt/artifact; input pin only after publication")
	}
}

func prepareActiveInputProducer(t *testing.T, producer *stepAttemptFixture) (*model.JobRun, *model.ProjectArtifact, *taskqueue.StepAttemptAdmission) {
	t.Helper()
	approveRetryApproval(t, producer.generationFixture)
	stepRetryApprovalStartup(t, producer)
	var upload *taskqueue.StepAttemptAdmission
	for index, step := range producer.plan.Steps {
		admission := producer.admitStep(t, index)
		producer.finishStep(t, admission, model.JobRunStatusSuccess)
		upload = admission
		for _, id := range step.AttemptRunIDs[1:] {
			require.NoError(t, producer.reportStep(t, id, nil, model.JobRunStatusSkipped, false))
		}
	}
	artifact := stepAttemptMySQLArtifact(t, producer, upload.Scope.StepRunID)
	original := stepAttemptMySQLReadJob(t, producer)
	require.Equal(t, model.JobRunStatusRunning, original.Status)
	return original, artifact, upload
}

// The locator must be observed before the terminal commit while the actual
// receipt lookup waits for its exact Job row. It must return historical bytes
// without turning the receipt into new execution permission.
func TestStepRetryApprovalMySQLTerminalPublicationInspectWait(t *testing.T) {
	producer, schema := stepRetryApprovalMySQLFixture(t, 1)
	original, artifact, upload := prepareActiveInputProducer(t, producer)
	request := producer.request(t, len(producer.plan.Steps)-1)
	request.Operation = "inspect"
	request.RequestDigest, _ = request.Digest()
	var sawActiveLocator atomic.Bool
	const callback = "test:terminal-publication-pretransaction-locator"
	require.NoError(t, producer.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) != "second" || tx.Statement.Table != (model.JobRun{}).TableName() {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			return
		}
		if _, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
			return
		}
		if job, ok := tx.Statement.Dest.(*model.JobRun); ok && tx.RowsAffected == 1 && job.JobRunID == original.JobRunID && job.Status == model.JobRunStatusRunning {
			sawActiveLocator.Store(true)
		}
	}))
	defer func() { require.NoError(t, producer.db.Callback().Query().Remove(callback)) }()
	var receipt *taskqueue.StepAttemptResult
	publish := func(ctx context.Context) error { return stepAttemptMySQLPublish(producer, ctx, original, artifact) }
	inspect := func(ctx context.Context) error {
		var err error
		receipt, err = producer.jobs.ReconcileStepAttempt(ctx, request)
		return err
	}
	outcomes := stepAttemptMySQLOrderedRace(t, producer, schema, publish, inspect)
	require.NoError(t, outcomes[0])
	require.NoError(t, outcomes[1])
	require.True(t, sawActiveLocator.Load(), "second locator must precede terminal commit")
	require.NotNil(t, receipt)
	require.False(t, receipt.ExecutionAllowed)
	require.Equal(t, upload, receipt.Admission)
	assertStepAttemptMySQLPublication(t, producer, model.JobRunStatusSuccess, artifact)
	t.Log("active pretransaction locator followed by exact Job wait observes committed terminal receipt without execution authority")
}
