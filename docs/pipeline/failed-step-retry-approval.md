# Approval-gated live failed-step retry: exact gate-on acceptance

The independent compiled `taskqueue.FailedStepRetryApprovalAccepted` gate is
enabled in the accepted candidate. Its ordinary production `NewProcess`/
`StartWorker` entries passed the exact hosted acceptance recorded below.
The earlier closed-gate results remain separate implementation-level evidence.
Neither result is a production rollout. Operator policies remain disabled by
default and require explicit destination-specific review and configuration.

The runtime fixture derives its authenticated heartbeat capabilities from the
started worker’s readiness and explicit policy flags. It constructs that heartbeat
request itself; the Agent service’s capability emitter is covered separately by
unit tests. This does not claim the service heartbeat lifecycle was exercised by
the combination fixture.

Both Control `pipeline.offlineRuntime.allowFailedStepRetryApproval` and Agent
`agent.sandbox.containerd.policy.allowFailedStepRetryApproval` remain **false by
default**, as do their respective parent `allowFailedStepRetry` permissions.
All four explicit operator permissions, the existing offline runtime policies,
and actual live-session readiness are required. Omitted settings mean false.
The [offline runtime examples](../offline-runtime-acceptance.md) keep these flags
off. No combination of the older whole-job approval and failed-step capabilities
implicitly grants this new capability. Opening the compiled gate does not edit
configuration, opt in existing jobs, approve a request or authorize deployment.

## Exact bounded contract

The exact new evidence tuple is kind `retry_failed_step_v1`, evidence version 1,
plan version 2, retry mode `failed_step`, `retryApprovalRequired=true`, and a trusted
1–3600-second approval timeout matching the frozen approval policy. The manual
`builtin:retry-approval` gate accepts no caller-supplied parameters. All ordered
logical steps, globally unique preallocated attempt IDs and definition digests
remain bound, along with runtime, source commit and registered input identities.
The budget is per logical step and includes its first attempt. Whole-job v1 and
ungated failed-step v2 canonical formats and digest domains remain unchanged.

The new Agent capability is `containerdFailedStepRetryApprovalV1` (protobuf field
21). The whole-job approval and live failed-step capabilities retain their original
meanings. A missing, mistyped or unknown capability fails closed. Older Control
JSON decoders reject unknown fields; no transparent mixed-version upgrade is
promised. Upgrade Control before enabling an accepted Agent capability; review the exact
gate-enabled candidate evidence before any operator opt-in.

This retains one live session and workspace, earlier successful steps, and partial
filesystem changes from failed commands. It does not provide rollback or recovery
across Agent restart. Background processes, zombies and `continueOnError` remain
unsupported. Approval is not a reusable permission to start later steps: admission
and execution must recheck current authority. Historical receipts preserve factual
ACKs but do not grant renewed execution. No migration 009 is required for this kind. Existing explicit prerequisites remain:
[005 immutable inputs](../migrations/005_immutable_job_inputs.md),
[006 generations](../migrations/006_container_job_generations.md),
[007 approval binding](../migrations/007_retry_approval_binding.md), and
[008 step admissions](../migrations/008_failed_step_admission_ledger.md).
Startup does not migrate, backfill authority or change operator permissions.

## Closed-gate implementation evidence (2026-10-15)

The repaired closed-gate backend head was
`f8cf9dc1296e0ade61372ae7b0ae386a22bab890`, tree
`7f33314c0e51a5d22fcc3da53de7b1d679b9e6af`; tested merge
`aa408e5e5ac1422ce55e74b71d43a84a5facfa85`.
`FailedStepRetryApprovalAccepted` was **false at that revision**. The bounded
acceptance adapter, not ordinary gate-enabled startup, exercised the new path.

- [Runtime run 38696214550](https://github.com/arcentrix/arcentra/actions/runs/38696214550)
  passed all five new approval/container scenarios without skips, together with
  the earlier runtime regressions. Its
  [cleanup artifact 11954830693](https://github.com/arcentrix/arcentra/actions/runs/38696214550/artifacts/11954830693)
  reports `passed: true`, `leaksBeforeFallback: []`, `cleanupErrors: []`.
  Artifact SHA-256:
  `b061f750737a86d0bcd0213d83c6404f44efdca3a6ad3229a11aee49c101293c`.
- [Ordinary CI 38696214599](https://github.com/arcentrix/arcentra/actions/runs/38696214599)
  passed quality checks, seven new real-MySQL repository test roots and two new
  service test roots without skips, retaining exact lock-wait witnesses and the
  13 earlier approval roots. Historical `admit`, `inspect`, `approval_observe`,
  `approval_admit` and `generation_admit` ACK paths passed.
- That ordinary run pinned frontend
  `ccf4519bbf2e09171fcb733ea19d8e3fa2ba606f`. Its actual-backend browser journey
  completed the existing installation/execution/logs/artifact chain with zero
  errors and four downloads. It did **not** establish end-to-end browser
  approval-gated failed-step execution.
  [Browser artifact 11955396527](https://github.com/arcentrix/arcentra/actions/runs/38696214599/artifacts/11955396527)
  SHA-256: `afb8404dc45d56873ccf3165ac92ccfd40e62970ea4bdb407dc4db0893586ac5`.
- [Frontend run 38691081433](https://github.com/arcentrix/arcentra-web/actions/runs/38691081433)
  passed approval-review/editor Chromium checks against simulated API transport
  at that frontend head. These fixtures cover review identity and interaction;
  they are separate from both real-MySQL authorization and real container effects.

These closed-gate results do not certify a different tree, a gate-enabled ordinary
entry, release OCI startup, production deployment, live Kubernetes rollout or
external model integration. V18's ungated failed-step evidence remains separately
recorded in [the original failed-step contract](failed-step-retry.md#acceptance-status).

## Gate-enabled ordinary-entry evidence (2026-10-15)

The accepted backend head is
`eba238c0e5fdfcfced1d12961c03aa70cc347d3b`, tree
`902210dab751d6e002e96f0a0f7e3f28cfb6252f`. Both workflows tested merge
`75b9454e8f336988cffa601f7a34f93d98fbf010`, whose tree matches that head,
and completed successfully:

- [Runtime run 38702766733](https://github.com/arcentrix/arcentra/actions/runs/38702766733)
  exercised ordinary `NewProcess`/`StartWorker` entry, the five approved-live-step
  scenarios and earlier runtime regressions. These cover a finite approved plan,
  cancellation while pending, revocation before a successor, revocation after
  committed admission before RUNNING, and lost admission ACK followed by revocation.
  Historical receipt replay preserves factual ACKs without authorizing fresh execution.
  The [cleanup artifact 11957845189](https://github.com/arcentrix/arcentra/actions/runs/38702766733/artifacts/11957845189)
  was independently inspected with `passed: true`, `leaksBeforeFallback: []`
  and `cleanupErrors: []`. Archive SHA-256:
  `62b6c5c36537b7cc38dba005a5e7b453d0725895f515193ab6e7a8aac30d11ee`.
- [Ordinary CI 38702766680](https://github.com/arcentrix/arcentra/actions/runs/38702766680)
  passed formatting, vet, race tests, command builds, real MySQL admission and
  authorization-contention checks, and the existing real broker/S3/Agent/browser
  journey. The frontend pin was
  `ccf4519bbf2e09171fcb733ea19d8e3fa2ba606f`.
  The actual browser evidence covers installation, execution, logs and artifact
  downloads; it does **not** establish browser approval-to-retry container execution.
  [Browser artifact 11956889283](https://github.com/arcentrix/arcentra/actions/runs/38702766680/artifacts/11956889283)
  archive SHA-256:
  `9954dae6c6f81128c0d08a8d934dd07066071a7ff86c3dde23cec0881e5df45f`.
  Approval-editor interaction remains separately covered by the simulated-API
  [frontend run 38691081433](https://github.com/arcentrix/arcentra-web/actions/runs/38691081433).

This evidence note was reconstructed on 2026-10-16 from the published candidate,
GitHub workflow/artifact metadata and the previously completed independent receipt
review. It is a new documentation change, not restoration of the unavailable
local final-document commit or its tree. At reconstruction, backend PR 62 and
frontend PR 12 remained draft and unmerged. Final-document CI, merge and exact
main verification must be tracked separately; these results do not certify any
later tree, packaged release startup, production rollout, live Kubernetes target
or external model integration. All operator permissions described above remain
off by default.
