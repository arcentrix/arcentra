# Expected source versus observed checkout

The protected source-observation path is implemented and conditionally wired.
`JobRunRepo.GetOrRecordJobInputsWithSourceExpectation` now implements the
transactional repository extension used by the authenticated Agent input RPC.
The independent startup bundle connects capture, Prepare/Confirm, dispatch,
claim-and-fetch, source observation and fresh approval. The release gate
`TerminalRerunAccepted` is still `false`, both operator opt-ins default off, and
real integrated MySQL/containerd/browser acceptance remains pending. Wiring or
local tests alone do not establish that acceptance. See the
[whole-job new-execution guide](../copilot-terminal-whole-job-execution.md).

## Sealed expectation and actual observation

`JobRunTaskPayload.SourceExpectation` contains version 1, a fixed ScheduleID,
preallocated immutable SealID and the original failed job's verified full source
SHA. It is part of serialized sealed task content. SealID is allocated first;
SealDigest is calculated over the sealed payload and is not placed inside that
payload. `Source.Commit` remains empty. Source repository/ref is preserved while
the run/job identity is new; a branch is never replaced by a SHA. This profile
supports one gated offline-container job with no upstream artifact inputs.

The request's `source_expectation_digest` binds canonical expectation JSON to
every JobInputIdentity field: pipeline, new run/job, Agent, claim, runtime and
source digests. Here "original claim" means the first claim established for
this **new execution**, not the failed job's historical claim; its bound Agent
and same process incarnation remain fixed after claim. Mutable controller lease
epochs are excluded.
Responses carry canonical expectation JSON and the same identity digest. The
Agent validates this against its sealed payload before workspace checkout.
Partial, malformed, missing or unexpected expectations fail closed, including an
old server that ignores the request extension. Ordinary tasks reject an
unexpected expectation in their response.

An initial nil proposal returns `unknown` with empty observed manifest/digest
and no approval. The Agent uses the exact-full-commit checkout interface and
only then proposes the actual resolved commit with an empty upstream artifact
set. The repository validates expected equality in the same transaction as
pinning the new identity's input manifest and creating its fresh pending
approval. A wrong SHA rolls back. Confirm, seal and claim do not manufacture a
pin or approval. Later exact pinned reads are receipts of a previously accepted
observation; they are not a substitute for the initial checkout. If the expected
commit is unavailable, execution fails closed without retrying current HEAD.
The definition comes from the prospectively captured resolved snapshot, never a
reload of the current pipeline definition.

## Authority and execution boundaries

- Claim-and-fetch checks the exact Gate/Schedule/Seal/Agent/claim and current
  payload-disclosure authority transactionally. It returns the original sealed
  bytes only to the authenticated matching process incarnation over the existing
  authenticated TLS transport.
- Source observation checks the enabled Agent originally bound to this new
  execution and its exact claimed, sealed execution identity. Like ordinary input registration, this preparation
  transaction does **not** reauthorize the human requester/candidate grants.
  After requester revocation it can still disclose the expected SHA or record
  an actual observation and create a pending approval. Neither permits commands.
- Fresh startup, retry-generation and step-start admission separately enforce
  the existing live requester/voter/policy, approval, input and execution
  controls. An old approval, observed SHA or historical receipt is never fresh
  execution permission. This is authenticated Agent observation, not remote
  attestation of arbitrary workspace contents.
- Ordinary `GetOrRecordJobInputs` rejects protected jobs when the expectation
  extension is omitted. A repository adapter without the expected-source
  interface still returns Unimplemented; it cannot fall back to ordinary
  input pinning. Compatible Control and Agent binaries are required.
- Claim response loss allows only bounded byte-identical transport retries
  within the same process incarnation. The transient admission nonce/start
  token cannot be restored after Agent restart. A dispatcher lease change or
  duplicate queue reference cannot transfer an already-claimed execution to
  another Agent/process or restart commands. Preserve unknown outcomes and
  existing journals/outbox receipts rather than manufacturing replacement work.

The relevant implementation is in
[`repo_copilot_rerun_inputs.go`](../../internal/control/repo/repo_copilot_rerun_inputs.go),
[`service_agent_inputs.go`](../../internal/control/service/service_agent_inputs.go),
[`service_agent_rerun_claim_bridge.go`](../../internal/control/service/service_agent_rerun_claim_bridge.go),
[`repo_retry_approval_admission.go`](../../internal/control/repo/repo_retry_approval_admission.go)
(`ReconcileJobRetryApproval` and `validateRetryApprovalExecution`) and
[`worker_rerun_claim.go`](../../internal/agent/taskqueue/worker_rerun_claim.go).
Local tests cover protocol validation, real local Git checkout, repository
transactions and bounded transport/HTTP wiring. They do not prove hosted MySQL
contention, real broker delivery, container execution/cleanup or browser outcomes.
Run the complete [migration 016 required-mode suites](../migrations/016_copilot_whole_job_rerun.md#required-verification)
and dedicated integrated acceptance before claiming this path accepted.

## Offline source transport validation

The offline-container DSL uses the same transport classifier as the hardened
Agent checkout. HTTPS uses system trust; HTTP is limited to anonymous literal
loopback addresses for disposable fixtures. Native `git://` is anonymous,
read-only upload-pack and has no encryption or authenticated-server guarantee;
use it only on a trusted network. Omit the `auth` object for native Git (including
an otherwise empty object). Userinfo, query/fragment tricks, custom helpers,
malformed native paths and ports are rejected before scheduling.

SSH and host-local/file sources are rejected by this offline profile. This does
not change source handling for independent local/bare-metal execution paths.
The shared classifier performs no connection or configuration change; actual
checkout still enforces the existing controlled Git environment and tree checks.
