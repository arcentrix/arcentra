# Explicit retry attempts

Retries are disabled unless a job has a complete, valid `retry` policy. Older
`maxAttempts` / `delay` alone are now rejected rather than silently selecting a
scope. The Build definition editor can apply a policy to its draft; normal
validation, repository save permission and concurrency checks still apply.

```yaml
jobs:
  - name: test
    retry:
      mode: failed_step
      maxAttempts: 3
      delay: 2s
      idempotent: true
      allowedExitCodes: [9, 75]
    steps:
      - name: tests
        uses: shell
        args:
          run: ./ci/test.sh
```

- `mode` is exactly `failed_step` or `whole_job`.
- `maxAttempts` includes the first execution and is bounded to 2–5. For
  `failed_step` the limit is per logical step; for `whole_job` it is per round.
- `delay` is optional and bounded to 0–1 hour. Cancellation and the job deadline
  interrupt the delay. The job timeout covers all attempts, not each round; a step timeout applies to each individual attempt.
- `idempotent: true` is an explicit operator assertion that repeated commands and
  their external side effects are safe. It does not make a command idempotent.
- `allowedExitCodes` is a nonempty, duplicate-free allowlist in 1–125. Only an
  actual shell process exit with one of these codes can trigger a retry. No
  string matching, model-generated classification or broad “retry every error”
  rule is used. Scripts must give the selected codes an unambiguous meaning;
  application-defined permission failures that share a generic exit code cannot
  be reliably inferred from output. Do not allowlist such a code.

The existing policy runner is used by baremetal control-plane and Agent work. The container generation loop below uses the same explicit policy bounds with a separate fresh-session lifecycle. Missing executors, invalid configuration, source/condition errors,
permission/system errors, conventional shell 126/127 errors, signals,
cancellation, deadline expiry, artifact failures, and log or terminal-record
persistence failures are not retry triggers. Infrastructure persistence failures
also stop later commands despite `continueOnError`. An ordinary command failure
with `continueOnError` remains a recorded failure and proceeds without retry.

## Baremetal scope and workspace contract

`failed_step` repeats just the failed step in the still-live job workspace.
Earlier successful commands are not repeated. The existing source snapshot,
files, environment and preceding side effects remain in place.

`whole_job` repeats all explicit steps from the beginning. Earlier successful
steps and their external side effects repeat. **It reuses the same workspace and
source snapshot; it does not reset files, perform a clean checkout or roll back
external systems.** Source checkout/download and approval run once before the
loop. A legacy job target and success/failure notifications run outside the loop.
An explicit deployment step is part of the loop and can repeat, so the operator
must only opt in when this is safe.

A retry never creates a fresh workspace and pretends it resumed prior state. If
the live workspace directory disappears, the retry stops. File contents remain
operator-controlled, so the runner cannot guarantee an application checkpoint.
Post-terminal manual resume and crash recovery into another attempt are not
implemented: use a new pipeline run, which has new run identities. The UI states
this limitation rather than providing a misleading “retry” action.

## Audit, logs, artifacts, and transport

Before execution the controller preallocates every bounded attempt as a distinct
`StepRun`. `attempt` starts at 1; `retryMode` and `previousStepRunId` expose the
scope and lineage in the scoped run-jobs API. The controller stores the complete immutable policy snapshot, and every attempt inherits the original pipeline trigger actor. Each attempt retains its own status,
exit code, log stream ID and step-scoped artifact namespace. No retry changes a
terminal attempt back to running or replaces an earlier failed outcome.
Unreached/reserved attempts become `skipped` with `attempt not used`. Run-job
summary counts use the latest logical execution rather than counting historical
failures as the final result. Full attempt history remains visible.

A failed command's report must be acknowledged before another command attempt
can start. A terminal transport failure never causes an execution retry.
Durably recorded successful reports may continue through the ordinary step
sequence; the job terminal waits behind every pending step/attempt report.
Persistence failures fence later commands. If the final command succeeded but
its final persistence/ACK is delayed, replay preserves that success rather than
inventing a command failure. Pending receipts are replayed with the original
claim and exact payload, not by rerunning commands.

Existing installations must run the explicit schema migration command before
starting the new controller, adding the StepRun attempt audit columns through
the model migration path. Production migration and deployment remain operator
steps; tests use disposable SQLite databases and local gRPC listeners.

Controller admission also fences attempts transactionally: the owning job must
still be running, the exact predecessor must have a committed terminal receipt,
and its scope, attempt number and policy must match. Failed-step retries require
an allowlisted failed predecessor; whole-job retries require a fully terminal
previous round with at least one allowlisted non-continued shell failure. Future
reserved slots do not hide current logical results in progress summaries. A
same-claim RUNNING re-ACK rechecks these conditions, while exact terminal replays
remain acknowledgeable after parent cancellation. Job-terminal commits are
rejected while any step/attempt remains open.

The Agent is trusted to report execution facts and enforce typed infrastructure
failure classification. An exit code alone is not independent proof that log
persistence succeeded, and the controller does not infer such proof from error
text. This admission protocol is not a defense against a compromised registered
Agent fabricating results.

## Offline container whole-job generations

For `containerd-offline-v1`, only `mode: whole_job` is supported, and only when both Control `pipeline.offlineRuntime.allowWholeJobRetry` and Agent `agent.sandbox.containerd.policy.allowWholeJobRetry` are explicitly true. Both default to false. Runtime readiness advertises a separate versioned retry capability; labels or an older Agent cannot substitute for it. `failed_step` remains rejected: a private owned-task quiescence experiment now proves a narrow process-free boundary, but production step-attempt CAS and continuation admission are not implemented. Existing legitimate background work makes that narrow boundary ineligible; it is never silently killed and called lossless continuation.

Each round has a fresh container, rootfs, tmpfs and workspace. All explicit steps repeat; external side effects are not rolled back. The first full source commit and registered input artifact set remain fixed across rounds. Checkout uses that full commit even when a branch moves; source/staging/hash/length failures stop the job. The reserved `ARCENTRA_JOB_GENERATION` environment value is the 1-based round number; job/step/runtime/Agent overrides are rejected. It is a diagnostic/testing value, not a permission or secret.

The controller captures the complete policy, executable-definition digest and every preallocated attempt before dispatch. The original JobRun claim is retained; each generation receives independent controller-issued generation/runtime identities. A first-generation preparation admission can precede checkout solely to allocate a journaled workspace. It cannot execute a command: the first and every subsequent Step RUNNING transaction require pinned inputs and the current active generation. Read-only inspection never authorizes allocation.

Only one acknowledged, non-continued, allowlisted pure shell exit can advance a round. Every error leaf must be the same eligible command exit; mixed runtime/log/receipt/cleanup errors do not qualify. The failed round and skipped remainder must have committed receipts. All owned processes/resources/workspace must be removed before a successor is admitted. Both first admission and successor handoffs persist intent before RPC and the exact response before allocation. Lost ACKs retry the identical transition; they do not mint another generation or rerun a command. Old-round terminal replays remain valid, but old rounds cannot claim work or publish winning outputs. Only a successful final generation registers outputs; partially uploaded objects from a failed finalization are not promoted and are not silently deleted.

Job/pipeline cancellation, disabled Agent, changed inputs, exhausted policy, cleanup failure and drain all fence successor admission. The overall deadline includes every round and delay. Draining finishes and cleans the current round, then starts no successor. A cancelled/expired request may have been admitted before its cancellation became visible; no user command may proceed without the subsequent live step claim, and owned unstarted resources are cleaned.

Approval-bearing retry requires the separate, default-off [durable exact-content gate](retry-approval.md). Only the exact manual `builtin:retry-approval` marker is supported, with trusted operator candidates and matching TLS Agent capability. The gate binds the pinned full source commit, all immutable inputs and complete finite retry plan before any container starts. Legacy first-run approvals cannot authorize retry. Deployment targets/steps remain outside this offline profile; removing a required gate is never an automatic workaround.

There is no automatic continuation after Agent/controller restart or an unresolved handoff. Preserve journals, receipts and generation rows. Read-only inspection does not resume execution. Use the existing explicitly offline reconciliation procedure only after all controllers/Agents and external work are stopped, then start a separately authorized fresh run. Never delete journals or rewrite claims to retry an ambiguous operation. Apply migration [006](../migrations/006_container_job_generations.md) explicitly after the immutable-input migration; no startup migration or historical retry-plan backfill occurs.

The registered Agent remains trusted to report command and cleanup facts. These bindings prevent accidental cross-round authority and replay mistakes; they are not attestation against a compromised Agent or rootful runtime operator. The container remains offline and no network, host socket, privilege or capacity expansion is implied.
