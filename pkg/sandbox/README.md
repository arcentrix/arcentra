# Containerd sandbox backend

This package provides the containerd backend contract for a bounded offline-job integration. It does **not enable deployment or establish an untrusted multi-tenant security boundary**. Deployment remains an explicit operator opt-in; see the separate production-entry acceptance status.

Actual backend, session, durable/crash recovery and integrated single-job acceptance passed on an authorized GitHub-hosted VM in [run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022), with clean FIFO/runtime cleanup. PR head `e7132be275c144a2eadc03b61e3c4f16dd302c4e` was tested as merge `12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`. That run kept the compiled production gate false. Independent gate-enabled [run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313) also passed actual production `NewProcess`/`StartWorker` constructors at PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c` (tested merge `1d2e29030741963897b7056c7d15249f2637d757`), with a clean cleanup receipt. These hosted test binaries do not certify packaged-service or release-OCI startup. [Exact evidence and opt-in configuration](../../docs/offline-runtime-acceptance.md) distinguish these stages. Unit/spec tests remain useful runtime-independent checks.

## Supported boundary

- Explicit `Create → Start → Execute → SealOwned → artifact transfer → RemoveOwned` lifecycle for durable callers; the init command must remain alive while steps execute
- Optional explicit init `User` accepts nonzero numeric UID[:GID], clears supplementary groups and every capability set, and preserves no-new-privileges. Durable integration must require it and match trusted host workspace ownership
- Explicit-user image configuration reads the image content blob directly and inherits only environment variables (with a default PATH). It does not inherit image user/group, entrypoint, command or working directory, and does not mount the rootfs in the Agent process for name resolution. Unsupported OS/config media and oversized config fail closed; legacy image-user mode retains SDK behavior
- Offline containers only: `NetworkMode: "none"` or empty. Bridge and host networking return errors; no CNI is installed or configured
- The requested exec command, image/container environment plus step overrides, absolute container working directory, and numeric `UID[:GID]`
- Numeric UID without GID uses the same number for both; overriding the user clears inherited supplementary groups and capabilities
- CPU quota, memory limit, optional CPU shares and memory reservation, with strict validation. Empty resource fields impose no corresponding limit. Memory reservation is a soft hint, not guaranteed memory
- Trusted bind mounts and container labels; volumes, tmpfs, privileged mode and TTY are rejected
- Live stdout/stderr writers plus separate bounded captures. Each captured stream is limited to the first 4 MiB, with `OutputTruncated` reporting truncation
- Combined in-memory log snapshots, limited to the first 4 MiB. Follow/tail/time-filter options return errors

## Example

Use the actual module path:

```go
import "github.com/arcentrix/arcentra/pkg/sandbox"

backend, err := sandbox.NewContainerdSandbox(&sandbox.ContainerdConfig{
    UnixSocket: "/run/arcentra-runtime/containerd.sock",
    Namespace: "arcentra-offline-node-a",
    RuntimeRoot: "/var/lib/arcentra-runtime/runc",
    FIFODir: "/var/lib/arcentra-agent/offline-fifo",
    DefaultImage: "your-approved-image@sha256:...",
    NetworkMode: "none",
}, logger)
// Check err, then arrange Close and report cleanup errors.

id, err := backend.Create(ctx, &sandbox.CreateOptions{
    Command: []string{"sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
    User: "1001:1001", // Must match the actual non-root Agent UID:GID.
    Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"},
})
// Check err. If an ID accompanies an error, cleanup remains pending for that ID.
// After successful creation, arrange Remove with a cleanup context.
err = backend.Start(ctx, id)
result, err := backend.Execute(ctx, id, []string{"sh", "-ec", "printf hello"},
    &sandbox.ExecuteOptions{WorkingDir: "/tmp", User: "1001:1001"})
// Check err AND result.ExitCode; a nonzero exit is distinct from a transport error.
```

The paths above must already be provisioned by the operator; the FIFO directory is private, Agent-owned and mode 0700, distinct from job workspaces and journals. This example does not install a daemon or change host permissions. Access to the rootful daemon socket is effectively root-equivalent for the trusted caller, even when its process UID is non-root; this backend does not isolate the host from that caller. Jobs must not receive the socket or Agent configuration/state roots. Only use an image approved for this host. It needs the commands used by the caller; the package does not install shell tools into images. Pulling/unpacking images is performed by the trusted host, independently of the container's offline network.

## Lifecycle and failure semantics

- Repeated Start succeeds only while the existing init task is actually running. Stopped/failed containers must be removed and recreated
- Execute subscribes to process exit before starting it. Commands run through containerd's exec API, never on the host
- Cancellation, timeout or uncertain exec RPC failure kills the entire container, including descendants. It cannot be reused for later steps
- Stop waits for exit after TERM, then escalates to KILL. An exited init does not prove quiescence: Stop/Seal/Remove verify the complete runtime PID list and kill remaining descendants, including after a successful exec. Created init tasks with a nonzero PID are also killed before deletion
- SealOwned irreversibly stops execution and verifies zero runtime PIDs without deleting the task, container, snapshot, or mounted workspace. Transfer artifacts only after SealOwned succeeds
- Cleanup uses a bounded context detached from the canceled execution context, preserving the containerd namespace
- Uncertain container creation returns a recovery handle internally. Create attempts cleanup and, if that cleanup fails, returns the recoverable container ID with the error
- Uncertain init-task creation is reconciled by container ID during Stop/Remove. Failed cleanup remains tracked and can be retried with Remove, Cleanup or Close
- Close stops new creation and waits for in-flight creates. Cleanup failure is returned; the runtime client stays available so Close can be retried
- Per-container operations are serialized. Stop/Remove cancel active execution. Cleanup snapshots currently tracked containers; callers should stop new submissions first or use Close
- Output writers are caller-owned and must not block indefinitely. Writer errors are returned while remaining output continues to drain

## Exact restart recovery

1. Generate a canonical `arcentra-<UUID>` ID and stable noncredential owner identifier (for example a digest binding controller, Agent, job, claim and workspace).
2. Call `PrepareResource(ctx, id, owner)` and persist the complete `ResourceRef` before calling Create with `CreateOptions.Resource`.
3. Retain the same namespace and snapshotter in that reference. A later namespace default must not redirect recovery.
4. After restart, call `InspectOwned` and `RemoveOwned` with the journaled reference. Both inspect external resources independently of process-local bookkeeping. Resource IDs, owner, namespace, snapshotter and deterministic snapshot key are verified on both container metadata and snapshot labels.

`RemoveOwned` succeeds when the exact container, task and snapshot are confirmed absent, including an intent written before creation or a completed deletion whose journal update was interrupted. Mismatched ownership or unavailable inspection fails closed. Snapshot-only residue is removable after ownership verification; a task with missing ownership metadata fails closed for operator reconciliation. Cached container metadata disappearing does not bypass snapshot cleanup. Recovered containers are cleanup-only and cannot resume a prior job.

Legacy `Remove(id)` cannot prove absence after restart and returns NotFound for unknown IDs. It retains a bounded, process-local cache of the last 1,024 successful removals for short-term idempotence. `Cleanup`/`Close` operate only on tracked IDs and are not crash-recovery APIs. Runtime-socket and namespace access must remain exclusive to trusted operators; labels do not defend against a privileged actor rewriting runtime state concurrently.

## Remaining limitations

This is not a complete untrusted-code isolation platform. Logs remain memory-only. Durable callers must write their own journal before Create and use exact owned recovery; the backend never enumerates and deletes a namespace. Image content is retained in containerd's cache. Container metadata, tasks and writable snapshots are cleaned up, with errors reported.

There is no Kubernetes backend, bridge networking, arbitrary plugin sandboxing, rootless support commitment, custom seccomp/AppArmor policy, or VM isolation. Mount inputs and access to the runtime socket must stay with trusted operators. A read-only bind flag does not establish a complete security boundary for arbitrary submounts. Do not expose host credentials, host root mounts, or runtime sockets to jobs.

See [the acceptance guide](../../docs/sandbox-containerd.md) for prerequisites, exact commands and the distinction between unit/spec tests and real runtime acceptance.

Agent runtime capability scheduling and immutable DSL runtime propagation have hosted implementation-level evidence. The independent gate-enabled ordinary production entries also passed in hosted test binaries; both operator policies default off. See [the Agent integration boundary](../../docs/sandbox-agent-integration.md). Single-job/offline/no-automatic-retry limits remain; no upstream artifact-input DAG, online package installation, release OCI image startup or multi-tenant security guarantee is implied.

## Local failed-step candidate (default off)

`LiveSessionBackend` is an explicit, injectable candidate interface. Its independent
`ContainerdConfig.EnableLiveStepRetry` switch defaults to false and is not exposed
by the production configuration factory. Ordinary `Create` / `Start` / `Execute`
retain their existing behavior. An ordinary `Start` or `Execute` cannot operate a
live-session resource through the legacy API.

A caller must first persist an exact `ResourceRef`, then call `CreateLiveSession`
inside its normal creating phase, persist created/starting phases, and call the
returned `LiveSession.Start` separately. The session fixes a non-forking
`/bin/sleep` init and requires a nonzero numeric UID and GID. Starting pins the
actual kernel process using pidfd; there is no numeric-PID fallback. The random
session-incarnation UUID, original execution claim and exact resource are bound
to the process-local handle.

`ExecuteStep` requires an explicit first-attempt identity and admission digest.
Only a known failed exit with confirmed exec reaping and drained output can
produce an opaque `FailedStepBoundary`: the whole task is paused, its full PID
list must contain only the pinned init, and every init thread's kernel child list
must be empty, including zombies. `CheckBoundary` repeats those checks without
resuming. A background descendant is a visible failure and triggers whole-task
cleanup; it is never killed to pretend the original live continuation survived.

`ResumeStep` accepts the original pending pointer only once. It requires the exact
next attempt of that failed logical step, a distinct admission digest, unchanged
command/environment/user/directory/timeout, and a fresh frozen-task verification.
The bound is five total attempts. Successful logical steps can advance to a larger
index; pending failed boundaries cannot use ordinary execution to skip admission.
Stdin is unsupported because a mutable reader cannot prove equivalent replay;
output writers are not serialized or treated as authority. All nonzero exits are
frozen, so upper-layer candidate policy must reject `ContinueOnError` rather than
silently use an unadmitted thaw path.

The permit is consumed before Resume. Immediately before Resume, exec creation
and exec Start, the local gate checks cancellation, stopping state and the exact
admission's not-before/expiry. The cancellation mutex is never held across daemon
IO. Admission expiry does not become the running command's timeout. Uncertain
Pause/Resume acknowledgement, replaced identity, missing inspection, cancellation,
or reused admission fails closed. JSON cannot serialize or reconstruct a boundary;
restart remains cleanup-only. Runtime labels and pidfds do not fence a hostile
privileged client racing containerd RPCs, so the existing exclusive cooperating
runtime-owner limitation remains.

This interface and its fake-runtime tests are implementation evidence only. The
V16 private quiescence acceptance helper remains separate. No new real-container
acceptance or production failed-step enablement is claimed by this candidate.
