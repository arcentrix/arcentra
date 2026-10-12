# Pipeline Kubernetes releases: bounded controller engine

Pipeline deployments are a separate, explicit opt-in to the [manual release workflow](kubernetes-releases.md). They use the same exact operator target, Kubernetes adapter, release history, environment reservation, authorization tiers, UID/generation/resourceVersion tests, and observed rollout criteria. No live cluster or credentials were used to implement or test this feature.

## Authority and accepted source

- Only authenticated manual starts may opt in. Each accepted run records its verified actor, reviewed immutable definition revision, normalized deployment-plan hash and reviewed target fingerprint. Client attribution strings, cron, webhook and unverified direct RPC calls grant no deployment authority.
- The operator must separately enable `allowPipelineDeployments = true` on the exact target; its default is false. This flag does not change legacy manual-release target fingerprints. The normal deployment feature itself also remains disabled by default.
- Current user enabled state, project/pipeline ownership, team membership, role/grant policy and the operator-selected `release:deploy_dev`, `release:deploy_staging` or `release:deploy_prod` permission are reloaded at trigger, preparation and immediately before a possible PATCH. The final check happens after the adapter's last GET, not just before starting the adapter call.
- The definition must have one controller-only deployment step, alone in the final DAG job. Every other job must be a predecessor. Agent execution, mixed build/deployment jobs, deployment-job retries, continue-on-error, legacy target blocks and approval gates are rejected for this milestone. Approval gates do not yet have reliable durable run linkage.
- Every predecessor job must genuinely succeed. All expected logical steps must have a successful latest executed attempt. An unused preallocated retry slot does not hide that attempt. A failed/skipped newer whole-job round cannot fall back to an older successful artifact, and a failed continue-on-error step cannot qualify.
- The source is the exact registered artifact from the selected successful upload-step attempt in the same active run. There is no latest-by-name search or source-run override. Missing registration, including delayed Agent terminal acknowledgment, stops preparation without touching Kubernetes.
- Existing checks still require exact project/pipeline/run/job/step/name ownership, nonexpired artifact metadata, 1–4096 manifest bytes, a matching SHA256 and one digest-pinned image field. This is not a build-source attestation. `definitionCommitSha` and release `commitSha` identify the pipeline definition; an unknown source checkout commit stays unknown.

The public manual release endpoint still requires the entire source run to succeed. It can use the definition revision of a completed opt-in run without claiming a verified source checkout revision. There is no external active-run bypass flag or preparation endpoint.

## Durable preparation and execution

The internal controller preparation validates persisted execution identities against the pinned plan, then stores:

- origin `pipeline_job`, verified actor and definition/plan/target identity
- execution job and step IDs, exact producer job and step-attempt IDs
- selected artifact ID/SHA256 and immutable image reference

A deterministic execution request key and a nullable unique execution-step index prevent a second release for the same step, even after the original release is terminal. Release creation, step-to-release linkage and environment reservation commit in one transaction. The controller's additional persistence callback must finish before any cluster request. Re-entry uses the same record and never reselects source bytes.

Before the first apply, the service rechecks the still-active run, exact successful predecessors, pinned artifact bytes, actor and target. The target baseline is durable before PATCH. PATCH acceptance means `observing`, not success. Observation is bounded by the reviewed step timeout; only a genuinely successful rollout completes the deployment step. Failed or superseded observations fail the step.

The pipeline performs at most one apply attempt. An ambiguous response immediately stops pipeline deployment and leaves the record `applying`. Bounded retries are only for transient read-only observation errors. There is no new-release retry, deployment job retry, restart takeover or HA reconciliation loop.

## Cancellation, inspection and explicit recovery

Cancellation and deadline expiry stop pipeline waiting. They only cancel a still-queued release. Once a release is `applying` or `observing`, its reservation remains until a verified terminal observation or explicit same-ID recovery. Cancellation is not rollback; a request already issued may have taken effect.

For pipeline-origin releases:

| Request | Effect |
| --- | --- |
| Existing release GET / refresh | Read persisted data only |
| `GET /releases/:releaseID/inspect` | Current authorized, read-only observation of the exact workload; no PATCH, persisted state change or reservation release |
| Existing `POST /releases/:releaseID/advance` | Observe an observing pipeline release; queued pipeline releases are controller-owned and ambiguous `applying` pipeline releases require explicit recovery |
| `POST /releases/:releaseID/recover` with `{"confirmRecovery":true}` | Independently confirmed recovery of that same applying/observing release under the current actor's fresh permissions |

Recovery can complete the originally selected image update even after the pipeline has stopped. The user must explicitly confirm that consequence. Before a potential recovery mutation, `lastRecoveryBy` and `lastRecoveryAt` are durably recorded with compare-and-swap; audit persistence failure aborts the action. Original `createdBy` stays unchanged. These fields record the latest authorized recovery attempt, not a complete event history. It is never triggered by refresh, cancellation, timeout, automatic re-entry or controller restart. It cannot select a new image, artifact, target or release. Read-only inspection reports `unresolved` when an applying release cannot be confirmed, rather than treating an unchanged baseline as proof of supersession. If the original patch was admitted, its marker and image are recognized without another PATCH; if not admitted, the original conditional update may be submitted. UID/generation changes still fail closed.

Turning off pipeline opt-in blocks new automatic deployment. It does not redirect historical targets or prevent a separately authorized explicit same-ID recovery. Restore an intentionally changed physical target binding before recovery. Do not delete reservations or create replacement releases to resolve uncertainty.

If the release succeeds but pipeline-step status persistence fails, the successful release remains a historical fact and the pipeline can remain failed/interrupted. Never deploy again merely to repair its status.

## Verification and limits

Local suites use disposable SQLite, real controlled artifact files, fake/in-process HTTP Kubernetes endpoints, and injected policies. They cover exact attempt selection, disabled consent/targets, changed definition/plan/target identities, current authorization revocation, atomic linkage and rollback, environment/step uniqueness, missing or expired/tampered manifests, ambiguous apply, cancelled/deadline state boundaries and explicit same-ID recovery. A separate Process integration suite exercises actual controller execution and artifact registration across these boundaries.

No production migration, MySQL locking behavior, external registry provenance, cluster admission, live rollout health, mounted credentials or real Kubernetes deployment was validated. Operators must explicitly migrate the schema before enabling this capability. Approval-bearing deployments, unattended/scheduled deployment principals, automatic rollback, arbitrary manifests and autonomous restart recovery remain unsupported.
