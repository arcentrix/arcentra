# Terminal whole-job new execution

This path creates a separately authorized execution from an eligible failed run.
It is implemented and conditionally wired, but **not released**:
`TerminalRerunAccepted = false`, operator settings default off, and integrated
hosted MySQL, containerd/Agent and browser acceptance remains pending. Local
unit/race tests or a submitted confirmation are not that acceptance.

This is separate from [Copilot's existing retry-approval vote](copilot-bound-retry-execution.md).
It does not need a model provider, a conversation or the retry-vote feature to be
enabled. It neither resumes the original run nor changes its failed status.

## Operator prerequisites

1. Back up and rehearse restoration, stop old Control writers, then explicitly
   apply [migration 016](migrations/016_copilot_whole_job_rerun.md) and its
   prerequisites with `arcentra-cli migrate --conf <reviewed-config>`. Normal
   startup never migrates. Keep compatible Control/Agent binaries together.
2. Preserve the existing managed encryption key, authenticated Controller TLS,
   Agent identity, Kafka, output-storage and private runtime/journal settings.
   This feature creates no credential, trust configuration or permission grant.
3. Configure the existing [offline runtime](offline-runtime-acceptance.md) and
   [whole-job retry approval](pipeline/retry-approval.md): both Control's
   `pipeline.offlineRuntime` and Agent's `agent.sandbox.containerd.policy` need
   enabled offline runtime, `allowWholeJobRetry` and `allowWholeJobRetryApproval`,
   with approved immutable images/resource limits. Control also needs its trusted
   `pipeline.retryApproval` candidate/quorum/timeout policy. These prerequisites
   do not themselves enable terminal new execution.
4. The two additional startup-only opt-ins are shown **off**, as required until
   independent acceptance and release approval:

   ```toml
   # Control
   [copilot.execution]
   terminalRerunEnabled = false

   # Agent
   [agent.sandbox.containerd]
   terminalRerunEnabled = false
   ```

   Setting these true in an ordinary release while the release gate is closed
   fails validation; it is not an operator bypass. Dedicated `sandbox_acceptance`
   builds have a separate bounded test gate and are not production approval.
   Configuration changes require restart. When legitimately enabled, startup
   binds capture/API/dispatcher/claim/cancellation together; the Agent advertises
   the matching capability only when its required offline runtime is ready.

## Which original runs qualify?

The original must be a verified manual trigger with a **committed prospective
snapshot**, one failed Run containing exactly one failed Job, complete terminal
and actual-source/input evidence, and an unexpired snapshot. The captured job
must use `containerd-offline-v1`, finite `whole_job` retry with `idempotent: true`,
and the built-in manual retry-approval gate. No upstream artifact inputs,
managed secret references or nonempty `Source.Auth` are supported. Only the
validator's bounded steps/runtime/source profile is eligible; SSH and host-local
source checkout are excluded from this offline path.

When capture is enabled, the original trigger transaction saves its existing
loaded/includes-expanded/parsed definition snapshot with the Run and Inbox event.
It does not fetch or parse the definition a second time. Ordinary parsing is not
capture. Historical runs without that committed snapshot remain unsupported:
never synthesize one, backfill a duplicate trigger, or reload current HEAD to
make an old run eligible. The new job expects the original **observed source
commit**, separately from the captured pipeline-definition revision.

## Prepare, confirm and approve

The authenticated preparer owns the intent. Eligibility and intent operations
require current project/pipeline read and pipeline trigger permissions;
Prepare/Confirm also require `approval:read`. Availability only checks project
read access. Ownership is not supplied by `TriggeredBy` or another body field.
Only that owner can recover, inspect, confirm or cancel the prepared intent;
current permissions and active project/pipeline scope are checked again.

The independent API base is
`/api/v1/projects/:projectID/copilot/rerun-intents`:

1. Read `GET /availability`, then
   `GET /eligibility?originalJobRunId=<original-job-id>`. Eligibility writes
   nothing and reserves no new identities. Review the exact original run/job,
   snapshot, expected source SHA, finite retry budget and current approval policy.
2. Explicitly `POST /` with one fixed opaque client `requestId` (such as a UUID),
   `originalJobRunId` and
   the reviewed `snapshotDigest`. Prepare saves the detached intent and fixed
   target IDs, not a running execution. Keep that RequestID if the reply is lost;
   recover with `GET /by-request/:requestID`, or `GET /:intentID` once known.
   A replay retains the original expiry and IDs.
3. Refresh the saved review. Explicitly `POST /:intentID/confirm` with
   `expectedVersion`, `intentDigest`, `reviewedRunContextDigest` and
   `acknowledgeNewExecution: true`. A successful transaction creates the fixed
   new Run/Job/Step-attempt, admission and schedule records. Repeated Confirm
   returns those same identities. It neither approves nor proves dispatch/start.
4. Follow the **new run's** approvals/jobs/logs. The dispatcher seals only the
   captured definition. The selected Agent process establishes a fresh claim for
   this new execution and checks out the exact expected commit; it does not reuse
   the failed job's claim. Once claimed, the same-incarnation restrictions below
   apply. Actual input observation creates a **new pending approval**.
   Eligible independent candidates must review and vote on it before user commands
   can start. Old votes, confirmation consent and source observations cannot
   satisfy this approval. Current admission checks continue after voting.

If review is stale, expired, unavailable or denied, do not confirm. Preserve
opaque recovery IDs when an outcome is unknown; do not generate a new RequestID
as an automatic retry. Archive/disable or permission revocation can make intent
history temporarily unavailable even when its durable receipt still exists.
Never treat an unavailable lookup as proof that Prepare/Confirm did not commit.

## Cancellation and uncertain outcomes

- `POST /:intentID/cancel-prepared` uses the current version and both review
  digests. It can win only while `prepared`. After Confirm commits it cannot undo
  the new execution; deleting Copilot history also cannot undo it.
- Stop the actual new run through the existing
  `/api/v1/pipelines/:pipelineID/runs/:runID/stop` operation. This uses current
  `pipeline:cancel`, independently of trigger permission or intent ownership;
  a nonowner with that capability can cancel. It does not impersonate the saved
  original requester or change provenance.
- Before claim, cancellation closes the admission gate transactionally against
  claim. After claim, it records cancellation for the exact existing claim and
  requests notification on the existing Agent control queue. The notification
  attempt has a 10-second bound, preserving an earlier caller deadline. A durable
  controller receipt or successful enqueue is **not** physical stop/cleanup proof.
  `agentTerminalAcknowledged` requires the exact Job and Step terminal receipts;
  runtime cleanup retains its own evidence. Unknown notification results are not
  automatically retried; an explicit authorized stop retry may notify the same claim.
- Lost claim replies permit only bounded same-process transport retries. Agent
  restart, a dispatcher lease change or duplicate delivery cannot adopt an
  already-claimed execution, recreate its transient nonce/start token, or rerun
  commands. Keep fixed receipts, journals and outbox data; use existing bounded
  recovery/inspection rather than inventing a replacement claim.

See [expected versus observed source](security/rerun-source-observation.md) for
preparation versus execution authority, and [migration verification](migrations/016_copilot_whole_job_rerun.md#required-verification)
for all eight required MySQL roots. The new path's hosted acceptance is still
pending; prior offline-runtime or retry-vote acceptance does not establish it.


## Additional hosted browser coverage, not yet run

The isolated acceptance extension adds two real-response-loss recovery checks to
Prepare/Confirm. The browser must let each real backend POST commit before losing
its response, reload exactly once after each loss, and recover using the fixed
RequestID/intent with one read-only inspection per phase. The preparation receipt
must record both losses, two reloads, one Prepare inspection and one Confirm
inspection, while still recording exactly one Prepare POST, one Confirm POST and
no approval vote. A synthetic response or repeated mutation is not recovery proof.

After the independent second reviewer has approved and observed actual success,
a third browser phase signs in as the original requester. It observes the original
failed execution and the same new successful execution, log marker and artifact,
then downloads and checks the same output bytes. Its separate
`copilot-rerun-owner-outcome-receipt.json` records `ownerOutcomeComplete: true`,
`runtimeAcceptanceComplete: false`, zero Prepare/Confirm/approval POSTs and one
artifact download. It does not open an approval dialog or claim to have reviewed
approval evidence. The original reviewer contract remains unchanged; only this
phase's login identity is selected from its original-owner scope.

Go reauthenticates the owner through the real login route before replaying Confirm.
After dispatcher join and queue drain, authoritative database checks require one
intent for the browser RequestID and one target Run, Job, admission gate and
schedule for that intent. The whole journey still has exactly four intents and
three materialized targets: the primary success, two existing auxiliary targets,
and a separately cancelled preparation with no target. The runtime proof adds the
validated response-recovery evidence, `ownerObserved`, owner download/mutation
counts and authoritative target counts. Existing approval, original-row equality,
actual command/upload, replay, source and cleanup checks remain required.

These are additional test obligations, **not completed hosted acceptance**. The
matching frontend revision still needs deliberate integration after the frozen
candidate's CI diagnosis. Only the new phase's bounded summary and receipt are
added to the existing upload allowlist, never diagnostics. This isolated change
does not update workflow pins, runtime opt-ins, production defaults, credentials,
network privileges or any existing timeout. Offline contract/wiring checks cannot
establish that the lost responses, browser reloads, owner journey, MySQL or runtime
actually passed; final-code hosted results and cleanup evidence are still required.
