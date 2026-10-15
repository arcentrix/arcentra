# Offline failed-step retry: local implementation candidate

This branch connects the separately validated live-session quiescence capability
to durable step-attempt authorization. The complete path has not yet passed its
real dependency/container acceptance. `FailedStepRuntimeAccepted` remains false;
production startup refuses `allowFailedStepRetry`, even when the ordinary offline
runtime and whole-job retry are enabled. No capability is advertised for this
candidate. Existing baremetal retry and accepted offline whole-job retry retain
their contracts.

## Intended bounded contract

A failed-step retry keeps one continuously live generation, container, writable
root filesystem, tmpfs and workspace. Earlier successful commands are not run
again. The failed command's partial file changes also remain; this is not rollback
or an application checkpoint. Shell-local variables and process state are not
preserved between shell invocations.

Only explicit idempotent plans with 2–5 attempts and a distinct 1–125 shell exit
code allowlist are eligible. Input/source pinning and registered artifact
integrity remain required. Approval-bearing plans and `continueOnError` are
rejected in this initial profile. Download remains before shell execution;
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

## Acceptance required before enablement

Ordinary tests and SQLite fixtures cannot prove MySQL lock behavior or kernel
isolation. Before changing the acceptance gate, verify the precise source tree
with real MySQL CAS races and the existing disposable containerd harness: prior
commands execute once, the failed command alone retries, state and identities
remain exact, output provenance uses the admitted successful attempt, lost ACKs
do not duplicate commands, background/zombie and uncertain Resume cases refuse,
cancellation wins correctly, and every new restart phase cleans without Resume.
The current local branch does not trigger or publish new external workflows.
