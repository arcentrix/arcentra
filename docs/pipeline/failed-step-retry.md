# Offline failed-step retry: accepted implementation, gate-on validation pending

The complete live-session/durable step-attempt implementation passed the
closed-gate hosted runtime acceptance on 2026-10-15. This candidate opens the
compiled acceptance gate, but its ordinary production `StartWorker` gate-on
verification is **pending**. The earlier result must not be treated as evidence
for this candidate's changed startup path. See [exact evidence](#acceptance-status).

Control `pipeline.offlineRuntime.allowFailedStepRetry` and Agent
`agent.sandbox.containerd.policy.allowFailedStepRetry` both default to false.
Neither this candidate nor a successful test implicitly enables either policy.
Existing baremetal retry and accepted offline whole-job retry retain their
contracts. The failed-step UI is not released ([draft PR 11](https://github.com/arcentrix/arcentra-web/pull/11); fixture CI passed, combined acceptance pending).

## Operator prerequisites

Keep failed-step policy disabled until the exact candidate has passed ordinary
production-entry acceptance and destination-specific checks. Then, before
explicitly opting in:

1. Back up and explicitly apply [migration 008](../migrations/008_failed_step_admission_ledger.md)
   with the earlier runtime, immutable-input and generation schema migrations.
   Startup never migrates or backfills execution authority.
2. Upgrade both Control and Agent to compatible builds. The selected ready Agent
   must advertise `containerd_failed_step_retry_v1`; a label, an old Agent or the
   whole-job retry capability is insufficient.
3. Review and explicitly enable `allowFailedStepRetry` in both policies, in
   addition to the existing offline runtime prerequisites. Configuration is a
   startup snapshot; restart the affected services after an authorized change.
4. Use the bounded policy below. Migration, capability advertisement and opening
   the compiled gate alone never authorize a retry or a production deployment.

## Bounded live-session contract

A failed-step retry keeps one continuously live generation, container, writable
root filesystem, tmpfs and workspace. Earlier successful commands are not run
again. The failed command's partial file changes also remain; this is not rollback
or an application checkpoint. Shell-local variables and process state are not
preserved between shell invocations.

Only explicit idempotent plans with 2–5 attempts and a distinct 1–125 shell exit
code allowlist are eligible. Input/source pinning and registered artifact
integrity remain required. `failed_step` plans with any approval or `continueOnError` are
rejected in this profile. V17 supports the separately default-off
[exact-content approval for `whole_job`](retry-approval.md); that approval does
not authorize live failed-step retry. Download remains before shell execution;
output upload remains after final sealing. Networking, OCI builds, branching,
parallel jobs and host-executor fallback are not added.

The controller authorizes every logical step's first attempt and each immediate
retry successor. Immutable admissions form a contiguous bounded chain over the
preallocated StepRun identities. Future unused SKIPPED rows do not become the
current attempt or a successful artifact producer. The immutable request binds
the original Agent/job claim, complete plan, fixed generation, exact inputs,
session incarnation, predecessor receipt and paused-boundary audit. A stored
receipt alone is never permission to execute again.

The Agent persists intent before requesting a CAS, persists the exact admission
before execution, and persists consumption before Resume/Exec. It obtains the
failure's durable terminal acknowledgement before requesting a successor. Lost
terminal or CAS acknowledgements replay only the same saved bytes. A lost Resume
acknowledgement is an uncertain effect and cannot be retried.

Admission has a fixed start-authorization window, checked after durable
consumption and immediately before a new effect. It does not replace or shorten
the declared timeout of work already started; the original job and per-step
deadlines continue to apply. Replaying an admission never renews its window.

## Process and recovery boundary

A retry boundary requires the failed exec to have exited and been reaped, the
entire original task to be paused, the kernel-pinned controlled init to remain
alive, and no other live processes or zombie children. This includes background
processes legitimately started by earlier successful steps. Such a process
causes a visible refusal and terminal cleanup; it is never killed and then
presented as a successful non-destructive continuation.

The runtime proof is opaque, live-only and one-use. It is checked again after
controller waits and delay, immediately before resuming the exact original task.
Cancellation, deadline, draining, identity change, uncertainty or persistence
failure blocks continuation. Commands already admitted before cancellation
arrives may have effects; cancellation is not rollback.

An Agent restart loses the proof. Journal recovery only verifies/cleans the exact
owned resources and replays original facts. It never reconstructs a proof from
a PID, JSON, admission or prior success, and never resumes an interrupted step.
Controller restart can serve a previously committed admission to an otherwise
still-live Agent only under the original current authority and deadlines.

## Acceptance status

The closed-gate implementation evidence is tied to remote commit `ffc843a3`,
source tree `d3512066883a3df4d93c50559e94d415fd850867`:

- [Runtime run 38682739091](https://github.com/arcentrix/arcentra/actions/runs/38682739091)
  passed all five live failed-step scenarios and seven restart phases, including
  strict source/input checks, exact lost-ACK replay, once-only effects and cleanup.
- [Cleanup artifact 11949115418](https://github.com/arcentrix/arcentra/actions/runs/38682739091/artifacts/11949115418)
  has SHA-256 `c1ac15a9b03893c3f8cbb7d95473fa3e56617c5e9ef1a37327d9173e41358405`.
- Ordinary [CI run 38682739082](https://github.com/arcentrix/arcentra/actions/runs/38682739082)
  passed, including actual MySQL contention and the existing backend/browser journey using the prior frontend pin. Its result is separate from
  the runtime result.

`FailedStepRuntimeAccepted` was false for that accepted implementation run. This
candidate changes the compiled gate to true; its ordinary `StartWorker` entry
must be independently exercised on the exact final candidate before gate-on
acceptance can be claimed. No gate-on passing result is recorded here yet.
Ordinary tests and SQLite fixtures alone cannot prove MySQL lock behavior or
kernel isolation. Retain exact commit/tree, final-code quality results and real
MySQL/containerd evidence separately; a prior tree's pass does not cover changes.

These results do not establish release OCI startup, a production deployment,
Kubernetes rollout or external model integration. No production cluster or model
is configured by this feature.
