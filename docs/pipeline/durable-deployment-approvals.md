# Durable approvals for the final Kubernetes deployment job

This is a bounded, opt-in extension of the [pipeline release engine](../pipeline-release-engine.md). It connects one final controller deployment job to the existing persisted approval and decision records. It does not add an IM callback, approval plugin, automatic restart, HA takeover, or background reconciliation service. All validation uses local disposable SQLite and fake/TLS Kubernetes fixtures; no live cluster, provider, or credentials were used.

## Operator policy and explicit trigger consent

Both `deployment.enabled` and the target's `allowPipelineDeployments` remain false by default. Before enabling either, follow the [migration checklist](../migrations/003_durable_deployment_approvals.md), provide the existing approved exact target configuration, and optionally add this operator-controlled policy to that target:

```toml
[deployment.targets.pipelineApproval]
approvers = ["REPLACE_WITH_REVIEWER_USER_ID", "REPLACE_WITH_SECOND_REVIEWER_ID"]
minApprovals = 2
timeoutSeconds = 600
```

The policy belongs to the immediately preceding `[[deployment.targets]]` entry. It accepts 1–100 distinct platform user IDs, a quorum from 1 through the candidate count, and 1–3600 whole seconds. The requester is removed from the frozen candidate set; preparation/trigger fails if the remaining candidates cannot meet quorum. Current permissions are still required, so configured IDs alone never authorize a vote.

An operator policy is mandatory for its target. Omitting the DSL gate does not bypass it. Public manual release preparation and first apply are rejected on protected targets, even when a source run succeeded or has no persisted approval rows. Existing targets with no policy keep their prior manual and ungated explicitly opted-in pipeline workflows.

The final deployment job declares only:

```yaml
approval:
  required: true
  type: manual
  plugin: builtin:deployment
```

No params are accepted. Requesters cannot set candidates, quorum, timeout, policy IDs, or actor identities. A gate without an operator policy fails closed. Predecessor gates, optional gates, `auto`, external plugins, legacy target blocks, deployment retries and IM decisions remain unsupported. Existing ordinary CI approval behavior is unchanged and is not treated as deployment authorization.

The definition review includes `deploymentPlan.approvalRequired` and `deploymentPlan.target.pipelineApproval`. The review token binds the normalized gate flag and the exact target fingerprint, including normalized policy candidates/quorum/timeout. Changing the definition, target, or operator policy invalidates earlier trigger consent. Starting still requires explicit `allowDeployments` and the reviewed definition/token.

## Exactly what gets approved

After successful predecessor attempts and exact artifact registration, preparation atomically writes all of:

- The single immutable pipeline-origin release and its exact registered artifact ID/SHA256/image digest
- The execution step's release link and environment reservation
- One approval request linked uniquely to that release and execution request

Any write failure rolls the whole transaction back. Neither preparing nor voting contacts Kubernetes. The request is created only after the artifact is pinned, so the reviewer sees actual evidence rather than a promise to select a later image.

The evidence digest includes the release/project/pipeline/run IDs; definition revision and plan hash; exact execution and producer attempt IDs; artifact ID and byte SHA256; image reference; immutable target tuple and fingerprint; original requester; frozen candidates/quorum; and original expiry. `commitSha` remains a pipeline-definition revision, not a verified source-checkout attestation.

The release exposes `approvalId`, `approvalEvidenceHash` and an immutable `approvalTarget` object containing `projectId`, `environmentId`, `targetRef`, `namespace`, `deployment`, and `container`. It contains no endpoint or credentials. The approval request exposes the matching `deploymentReleaseId` and `evidenceHash`.

## Decision API and authorization

The existing authenticated approval endpoints are used:

- `GET /api/v1/approvals?pipeline_run_id=...`: read the actual run's scoped approvals
- `GET /api/v1/approvals/:id`: read persisted context and current decision eligibility
- `POST /api/v1/approvals/:id/approve` or `/reject`: body `{ "reason": "Reviewed the exact target and image" }`

The actor always comes from authenticated server context. Body/query attribution cannot impersonate another user. A reviewer must be in the frozen operator candidate set, must not be the requester, and must currently hold project/pipeline read, the relevant approval permission and the exact target's release tier permission. Reviewer authority does not grant pipeline-trigger or release-execution authority.

Each new vote rechecks the active linked run/job/step, queued release, environment reservation, evidence and current permissions. Decision insertion, distinct-voter counts, quorum and the pending-to-final transition commit in one transaction. A unique `(approval_id, user_id)` index prevents duplicate voters. Repeating the same user's same committed vote returns the existing result; an opposing vote conflicts. One rejection is terminal. Expiry and cancellation cannot be reversed by a delayed callback or retry. Legacy rows without trustworthy candidates fail closed and cannot be promoted into deployment authority.

Only the existing running controller waits for the result. Once quorum is reached, it revalidates the original run, artifact bytes/digest, target and permissions before reading Kubernetes. Immediately after the adapter's final GET and before a possible PATCH, it revalidates the release/version/reservation, execution actor, approved vote rows, every approver's current permissions, original expiry and immutable evidence. A newly revoked approval permission or membership blocks the mutation even after quorum. This is not a distributed transaction with Kubernetes; revocation cannot recall an already admitted request.

## Expiry, cancellation, restart and recovery

Human waiting consumes the same bounded step/job timeout as rollout. Configure enough total time for both. The approval deadline begins when the artifact and release are prepared and is never extended on a retry, reload or restart.

Rejecting, expiring, stopping or timing out before apply stops the waiting controller. The queued release can be cancelled; cancellation atomically cancels any still-pending approval and frees the reservation. An already-approved/rejected/expired vote history stays intact. Cancelling an approved queued release revokes its ability to execute without rewriting history. Repeated clicks and concurrent cancel/advance cannot resurrect it.

Restart reconstructs reviewable records, not a running coordinator. It cannot create a replacement approval, reset expiry, change the pinned source, or automatically resume any run. A later approval decision by itself never starts work. An interrupted queued release must be reviewed and explicitly cancelled if its original controller will not complete; the existing offline execution recovery workflow may reconcile interrupted run records separately. There is no queued-release takeover or resume button.

An `applying` or `observing` release is not rolled back by pipeline cancellation. Its reservation remains until a verified terminal observation or an already-supported explicit same-ID recovery. Recovery never selects a new image, target, source attempt or release, and never resumes the cancelled pipeline. It preserves original requester/approval evidence and records the separately authorized recovery actor.

Expired, revoked or changed approval evidence blocks potentially mutating recovery. In this bounded implementation, an `applying` record with expired approval remains reserved even if its PATCH might have landed. Read-only `inspect` remains available; an already-`observing` release can use ordinary read-only observation to reach a verified terminal state. There is no approval renewal, un-approve, replacement release, reservation deletion, or automatic repair workaround. A future narrowly authorized read-only reconciliation feature would be separate work.

## Verification and deployment limits

Local suites cover unique linkage and rollback, restart preservation, immutable evidence including JSON normalization, candidate/self-vote checks, multi-voter quorum and replay, expiry/rejection/cancellation, missing/tampered artifact and target evidence, current role revocation during the last Kubernetes GET, lost PATCH responses, same-ID recovery and no automatic restart. Frontend tests cover review/Back/Close, stale scope/navigation, repeat clicks and uncertain decision refresh.

Production MySQL locking/migrations, cross-node HA behavior, real credentials, real cluster admission/rollout and provider delivery are not validated. Migrate and review operational settings explicitly before enabling this feature.
