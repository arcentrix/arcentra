# Opt-in Kubernetes release job

This bounded controller-only step deploys one digest-pinned image from a registered artifact after every other DAG job has completed successfully. It is disabled by default. It is not an Agent Kubernetes execution runtime, arbitrary manifest apply, automatic rollback, or restart/resume facility. Development and tests do not contact a real cluster or use real credentials.

## Operator prerequisites

Use the explicit schema migration command before enabling the feature. Additive fields include:

- PipelineRun: allow_deployments, deployment_actor_id, deployment_plan, deployment_plan_hash, deployment_target_fingerprint
- StepRun: deployment_release_id
- DeploymentRelease: origin, execution_job_run_id, execution_step_run_id (nullable unique), producer_job_run_id, producer_step_run_id, deployment_plan_hash, approval_id (nullable unique), approval_evidence_hash, approval_target
- ApprovalRequest: deployment_release_id (nullable unique), evidence_hash, version; environment supports 64-character IDs
- ApprovalDecision: unique (approval_id, user_id); see [migration notes](../migrations/003_durable_deployment_approvals.md) before upgrading existing voting data

Starting the server does not migrate production databases. Back up and test the migration against a representative database first; tests here use SQLite, not production MySQL.

Configure a target as described in [Kubernetes releases](../kubernetes-releases.md), with both deployment.enabled and that exact target's allowPipelineDeployments set to true. Both default false. Existing mounted credentials, TLS trust, named Deployment RBAC, exact project/workload binding and target tier are operator responsibilities. This change neither creates credentials nor expands external access.

Each trigger, prepare, and immediate pre-PATCH authorization reloads the enabled actor, current enabled-team memberships, enabled roles, and unexpired grants. Project read/update, pipeline read/trigger and the operator target's release:deploy_dev/staging/prod permission are required. Disabled/archived projects, disabled pipelines, missing data, or revoked roles/memberships fail closed. This is a live authorization check, not an atomic cross-node transaction synchronized with an external Kubernetes PATCH; a request already admitted by Kubernetes cannot be recalled by later revocation.

## DSL

```yaml
namespace: example
version: 1.0.0
jobs:
  - name: build
    steps:
      - name: publish-manifest
        uses: builtin:artifacts
        action: upload
        args:
          paths: [image-manifest.json]
  - name: release
    dependsOn: [build]
    timeout: 10m
    steps:
      - name: deploy
        uses: builtin:kubernetes
        action: deploy
        args:
          environmentId: REPLACE_WITH_EXISTING_ENVIRONMENT_ID
          artifact:
            job: build
            step: publish-manifest
            name: image-manifest.json
```

The example assumes preceding build logic already produced the manifest. The only supported manifest is a 1–4096-byte JSON object with one image field containing a sha256-qualified OCI image reference. No image build/push is inferred from artifact upload. The exact artifact must be registered in the current project/pipeline/run and belong to the actual successful upload attempt used by the successful producer job. Stale, skipped, unused reserved retry attempts, unregistered files, checksum failures, and ambiguous artifact matches are rejected.

Exactly one deployment step is allowed. It must be the only step in the final job, which has all other jobs in its predecessor closure and directly depends on the named producer. Timeout is explicit, in whole seconds from 1s through 1h; a job timeout must cover the step timeout. Deployment args contain exactly environmentId and artifact.job/step/name literal identifiers. Endpoints, credentials, image overrides, interpolation, Agent routing/selectors, retry, continueOnError, source checkout, conditional execution, job environment, notifications and legacy targets are rejected for the deploy job. Deployment-bearing pipelines reject all legacy targets and all approval shapes except the final-job `required: true`, `type: manual`, `plugin: builtin:deployment` gate with no params. That gate requires the exact operator target to configure trusted approvers, quorum and timeout; the gate cannot nominate its own reviewers. A configured target policy cannot be bypassed by omitting the gate or preparing a public manual release. See [durable approvals](durable-deployment-approvals.md). Non-manual pipeline-level triggers are rejected during validation; external cron/webhook paths also refuse deployment-bearing definitions before creating a run. Includes are rejected for deployment-bearing triggers so a reviewed root revision cannot silently import changed templates. Existing nondeployment pipelines retain their behavior.

## Review and trigger contract

GET /api/v1/pipelines/:pipelineID/spec returns the normal parsed definition and headCommitSha, plus:

- deploymentPlan: null for ordinary CI, otherwise canonical bounded plan fields and a public target snapshot
- deploymentPlanHash: an opaque SHA-256 review token over the normalized plan and current operator target fingerprint; empty if target review is blocked
- deploymentReviewError: a blocking review explanation, if any; the valid definition remains readable/editable

The target snapshot includes its environment/project/name/ref/namespace/Deployment/container, configuration and per-target pipeline opt-in flags. It never exposes an API-server endpoint, credential, or bearer token.

POST /api/v1/pipelines/:pipelineID/trigger requires, for a deployment-bearing definition:

```json
{
  "requestId": "stable-key-for-this-exact-intent",
  "allowDeployments": true,
  "expectedDefinitionCommitSha": "FULL_REVIEWED_HEAD_SHA",
  "expectedDeploymentPlanHash": "REVIEW_TOKEN_FROM_GET_SPEC",
  "variables": {}
}
```

Consent defaults false. The verified HTTP authentication context supplies the actor; body triggeredBy cannot grant authority or impersonate another actor. Direct RPC, webhook, cron and arbitrary service Submit paths without the immutable verified-manual consent fail closed. The plan, its own canonical hash, target fingerprint, actor, reviewed definition revision and consent are persisted before scheduling. The separately cloned build source is not attested by definitionCommitSha; new deployment-bearing runs leave legacy commitSha empty rather than claim source provenance.

A repeated requestId returns the original run only when actor, consent, definition revision, target token and variables match. It never reschedules work. Missing consent returns 400, stale definition/target or conflicting duplicate intent 409, unverified/revoked authority 403, and unavailable services fail closed. The existing pipeline response envelope carries these codes.

## Runtime, cancellation and recovery

The controller atomically claims a pending deployment-bearing run. Existing job records or a nonpending persisted status prohibit resubmission, including after a failed final-status write or a process restart. There is no automatic restart/resume, replacement release, or automatic uncertain PATCH retry.

The final step prepares one durable release, stores its release ID on the StepRun before any PATCH, waits for its optional durable gate, then waits until Kubernetes observation proves that exact rollout succeeded. Gate waiting consumes the same explicit step/job timeout; use a timeout covering both human review and rollout. The approval deadline is never extended on re-entry or restart. Dispatch acknowledgment is not completion. Failed/superseded releases fail the step/run. The scoped run-jobs API includes steps[].deploymentReleaseId; release details remain guarded by project and pipeline access checks.

Cancellation/deadline stops waiting. A queued, unapplied release may be cancelled. An applying/observing release retains its state and environment reservation; stopping the pipeline does not claim a rollback or that no mutation occurred. A lost PATCH response leaves the same release unresolved and fails the waiting run.

- GET /api/v1/projects/:projectID/releases/:releaseID/inspect performs read-only Kubernetes inspection. Its bounded result includes releaseId, persistedStatus, status, reason and replica/generation observations. It neither resumes a write nor rewrites the original pipeline result.
- POST /api/v1/projects/:projectID/releases/:releaseID/recover with {"confirmRecovery":true} is a separate explicit same-ID recovery. It rechecks the current actor/target and may conditionally submit the original request only when needed. It never creates another release, changes its image, restarts the old pipeline, or starts a queued release from a stopped run.
- Ordinary advance cannot automatically recover an uncertain applying pipeline release. Page refresh reads only persisted state.

The offline recover-interrupted command preserves all releases and reservations, warns about linked queued/applying/observing releases, and only reconciles interrupted execution records. Inspect unresolved releases before starting new deployment work. No command here infers rollback.

## Local verification

Parser/validation, trigger actor/opt-in/review hash, duplicate identity, SQLite run claim, genuine artifact execution, release/step linkage, rollout waiting, cancellation, permission revocation, ambiguous acknowledgment, persistence-failure/no-replay, and offline recovery are covered by local fake/SQLite tests. Live Kubernetes, production credentials, admission policy, image pulls, serving traffic and production migrations remain unverified.
