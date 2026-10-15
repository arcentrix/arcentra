# Offline failed-step retry: gate-on candidate accepted

The live-session/durable step-attempt implementation and its ordinary production
`NewProcess`/`StartWorker` gate-on path passed independent hosted acceptance on
2026-10-15. The accepted backend candidate is
`3f7d81283afcabaf62af0e705a8d0b1252035024`, tree
`af529d00f50999cf4f5b9c1f9be9cbca7b6a6841`. See [exact evidence](#acceptance-status).

[Backend PR 61](https://github.com/arcentrix/arcentra/pull/61) and
[frontend PR 11](https://github.com/arcentrix/arcentra-web/pull/11) were draft when this evidence was recorded on 2026-10-15; this is candidate acceptance, not a main release or deployment.
Current merge/release status is available from those PRs and the delivered revision manifest.
Control `pipeline.offlineRuntime.allowFailedStepRetry` and Agent
`agent.sandbox.containerd.policy.allowFailedStepRetry` both default to false.
Neither the accepted gate nor a successful test implicitly enables either policy.
Existing baremetal retry and accepted offline whole-job retry retain their
contracts. The failed-step editor has fixture-based Chromium evidence; the
actual-backend journey and runtime execution evidence have separate scopes below.

## Operator prerequisites

The exact candidate above has passed ordinary production-entry acceptance. Keep
failed-step policy disabled until the intended build and destination-specific
checks are reviewed. Before explicitly opting in on an authorized destination:

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
integrity remain required. `continueOnError` remains rejected. The V18 profile
recorded below did not support approval-bearing failed-step plans. The separate
[V19 exact-content failed-step approval candidate](failed-step-retry-approval.md)
adds only its exact kind/plan tuple and independent default-off permissions;
its gate-enabled ordinary entry is pending this candidate's CI. V17
[whole-job approval](retry-approval.md) does not authorize that combination. Download remains before shell execution;
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

### Gate-on candidate and distinct verification scopes

The accepted backend head is `3f7d81283afcabaf62af0e705a8d0b1252035024`,
source tree `af529d00f50999cf4f5b9c1f9be9cbca7b6a6841`; the tested merge is
`5f04d1f9c0c4ae0d50c147a9d993d010d2c5686a`.

- [Runtime run 38684719818](https://github.com/arcentrix/arcentra/actions/runs/38684719818)
  passed with `FailedStepRuntimeAccepted = true`, ordinary `NewProcess` and
  `StartWorker`, and capability derived from actual worker readiness. Closed-gate
  adapters were unused. All five live failed-step scenarios, seven restart phases
  and prior runtime regressions passed without skipped tests. Earlier steps ran
  once, the selected failed command twice, with one generation/session/init,
  one Resume and one final publication. Exact receipt/CAS/job ACK replay and
  independently observed Kafka duplication preserved bytes, claims and digests.
  Background/zombie/cancel refusals had no successor effects, Resume or artifact;
  uncertain Resume had one Resume call but no successor effects or artifact.
- [Cleanup artifact 11949518772](https://github.com/arcentrix/arcentra/actions/runs/38684719818/artifacts/11949518772)
  reports `passed: true`, `leaksBeforeFallback: []`, `cleanupErrors: []`.
  SHA-256: `21a1b526e3404d6ee2d08cb3b02aa4de9d1783491f5293fc6bc1a9ee75a79628`.
- [Ordinary CI run 38684719835](https://github.com/arcentrix/arcentra/actions/runs/38684719835)
  passed quality checks. The five real MySQL step-attempt scenarios with four
  exact server row-lock wait witnesses and the existing nine repository plus four
  service approval tests all ran without skips in their dedicated integration lane.
  Its actual-backend browser
  journey pinned frontend `c0b0fd280f80ed5bc8dcea6d638352d73bbeb289` and completed
  the existing real installation, execution, logs and artifact journey with zero
  errors and four downloads.
  [Browser artifact 11950580259](https://github.com/arcentrix/arcentra/actions/runs/38684719835/artifacts/11950580259)
  SHA-256: `8fc0720ece34cb17bc55a6612156d73006466bf7975d4c28247a6fec94c95a7c`.
- [Frontend run 38683724510](https://github.com/arcentrix/arcentra-web/actions/runs/38683724510)
  passed quality and Chromium checks at that frontend head. Failed-step editor
  draft guards and no-autosave behavior were verified with simulated API transport.
  The actual-backend browser journey did not select and execute `failed_step`;
  actual failed-step execution is established by the separate runtime run above.

Both PRs were draft when this evidence was recorded on 2026-10-15. These exact-candidate results do not certify another
revision, release OCI startup, a production deployment, Kubernetes rollout or
external model integration. No production cluster or model is configured by this
feature. Ordinary tests and SQLite fixtures alone cannot prove MySQL lock behavior
or kernel isolation; changed runtime code requires fresh exact-code evidence.

### Earlier closed-gate implementation evidence

The earlier implementation passed at remote `ffc843a3`, source tree
`d3512066883a3df4d93c50559e94d415fd850867`, with
`FailedStepRuntimeAccepted = false`:

- [Runtime run 38682739091](https://github.com/arcentrix/arcentra/actions/runs/38682739091)
  passed all five live scenarios and seven restart phases, including strict
  source/input checks, exact lost-ACK replay, once-only effects and cleanup.
- [Cleanup artifact 11949115418](https://github.com/arcentrix/arcentra/actions/runs/38682739091/artifacts/11949115418)
  SHA-256: `c1ac15a9b03893c3f8cbb7d95473fa3e56617c5e9ef1a37327d9173e41358405`.
- [Ordinary CI run 38682739082](https://github.com/arcentrix/arcentra/actions/runs/38682739082)
  passed MySQL contention and the existing backend/browser journey using the
  prior frontend pin. It does not substitute for the later gate-on evidence.
