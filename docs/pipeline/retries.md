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

The same policy runner is used by the control-plane TaskFramework and whole-job
Agent worker. Missing executors, invalid configuration, source/condition errors,
permission/system errors, conventional shell 126/127 errors, signals,
cancellation, deadline expiry, artifact failures, and log or terminal-record
persistence failures are not retry triggers. Infrastructure persistence failures
also stop later commands despite `continueOnError`. An ordinary command failure
with `continueOnError` remains a recorded failure and proceeds without retry.

## Scope and workspace contract

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
