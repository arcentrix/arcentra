# Failed-step container continuation: capability proof only

The production `containerd-offline-v1` profile still rejects `failed_step`.
This work is a private runtime capability probe, not a new retry mode or an
execution permission. It does not change the ordinary session init, operator
policies, network isolation, capacity, or restart behavior.

## Proposed narrow boundary

A future failed-step retry must preserve the original session, workspace,
writable root filesystem and tmpfs. Starting a fresh session with an old
workspace is not equivalent. Killing earlier background work and calling that
lossless continuation is also incorrect.

The probe instead uses a controlled single-process init. Before user commands,
its kernel identity is pinned with a Linux pidfd; unsupported kernels fail
closed. After the failed command's exit and successful exec deletion have both
been observed, the exact owned task is paused. Runtime ownership and task
identity are checked again, and the complete process list must contain only
that pinned init. The cgroup process list alone does not prove zombie absence. The frozen, pinned init must also have no descendants in its scoped process metadata. Missing visibility is a refusal, not an empty result. There is no process-name whitelist or numeric-PID fallback.
Any additional process, including earlier legitimate background work, makes this
narrow continuation ineligible. Normal failure cleanup may then terminate the
job, but it must not report that the failed step resumed successfully.

The task remains frozen until guarded continuation. A cancellation admitted
before Resume prevents that call. If a Resume RPC was already admitted, its
remote effect can be uncertain; no user command may begin and exact owned
cleanup is required. A successful response alone is insufficient: task status
and identity must be verified before execution. Unknown ownership, failed
pause/resume, unreaped exec, output/log error or inconsistent status permanently
invalidates the probe. A restarted process cannot reconstruct a continuation
permit and may only use existing ownership-checked cleanup/reconciliation.

## Runtime ownership limitation

This private experiment requires an exclusively controlled/cooperating daemon
and namespace, consistent with the existing trusted rootful-runtime profile.
The containerd API has no atomic task-generation precondition for Pause, Kill
or Delete. A client-side lock and pidfd cannot fence another privileged daemon
client replacing resources between RPCs. This is not hostile-client or HA
fencing.

An asynchronous Task.Wait channel is not evidence that its ID-based RPC was
registered while the original init was alive. The probe must not use such a
channel to turn a stopped same-numbered PID into original-task proof. Cleanup
is restricted to a single operation that begins with verified live identity;
if attribution is later lost, evidence remains and cleanup reports failure.
It must not target an unknown replacement or claim that resources are gone.
Existing ordinary production cleanup behavior is unchanged by this experiment.

## Evidence required before product integration

- Deterministic ordinary/race cases for fork/background children, init/task
  replacement or PID reuse, failed exec deletion, output errors, Pause/Resume
  mismatches, cancellation and bounded cleanup.
- A real named subtest within the already-approved disposable GitHub VM and
  existing runtime-test budget, using the same official runtime/image pins and
  non-root user. It must fail, not skip, if the required capability is missing.
- Actual preservation of workspace/rootfs/tmpfs bytes across a clean boundary,
  resumed command execution, refusal when a background writer survives, and
  verified exact cleanup including a paused task.
- Independent review and exact-tree CI evidence. Local injected tests alone do
  not establish real task freezing or isolation.

The probe remains unreachable through production retry selection. Even a
successful capability test does not authorize a second StepRun: a later change
must add a separate immutable step-attempt permission and CAS, retain exact
historical ACK replay, bind the failed and successor attempt to the same live
session, and prevent cancellation/revocation or restart from granting execution.
Whole-job generation numbers must not be reused as step-attempt numbers.

Current whole-job retry and its limitations are documented in [retries.md](retries.md).
Approval-bearing container retry remains rejected until approval durably binds
the pinned full source commit, cumulative artifact inputs and entire finite
retry plan. No production cluster, credential, or model connection is configured
by this proof.
