# Containerd sandbox backend

This package repairs the existing containerd backend contract. It is **not connected to Agent execution**: Agent sandbox mode still fails closed, and pipeline runtime images/resources are still rejected. Do not remove those guards based on unit tests.

Current verification: fake-backed lifecycle tests, real OCI spec tests and an SDK adapter fixture can run without a daemon. Actual containerd acceptance requires a separate capable Linux host and has **not been completed in the restricted implementation environment**.

## Supported boundary

- Explicit `Create → Start → Execute → Remove` lifecycle; the init command must remain alive while steps execute
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
    UnixSocket: "/run/containerd/containerd.sock",
    Namespace: "arcentra",
    DefaultImage: "your-approved-image@sha256:...",
    NetworkMode: "none",
}, logger)
// Check err, then arrange Close and report cleanup errors.

id, err := backend.Create(ctx, &sandbox.CreateOptions{
    Command: []string{"sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
    Resources: &sandbox.Resources{CPU: "500m", Memory: "512Mi"},
})
// Check err. If an ID accompanies an error, cleanup remains pending for that ID.
// After successful creation, arrange Remove with a cleanup context.
err = backend.Start(ctx, id)
result, err := backend.Execute(ctx, id, []string{"sh", "-ec", "printf hello"},
    &sandbox.ExecuteOptions{WorkingDir: "/tmp", User: "1000:1000"})
// Check err AND result.ExitCode; a nonzero exit is distinct from a transport error.
```

Only use an image approved for this host. It needs the commands used by the caller; the package does not install shell tools into images. Pulling/unpacking images is performed by the trusted host, independently of the container's offline network.

## Lifecycle and failure semantics

- Repeated Start succeeds only while the existing init task is actually running. Stopped/failed containers must be removed and recreated
- Execute subscribes to process exit before starting it. Commands run through containerd's exec API, never on the host
- Cancellation, timeout or uncertain exec RPC failure kills the entire container, including descendants. It cannot be reused for later steps
- Stop waits for exit after TERM, then escalates to KILL. Created init tasks with a nonzero PID are also killed before deletion
- Cleanup uses a bounded context detached from the canceled execution context, preserving the containerd namespace
- Uncertain container creation returns a recovery handle internally. Create attempts cleanup and, if that cleanup fails, returns the recoverable container ID with the error
- Uncertain init-task creation is reconciled by container ID during Stop/Remove. Failed cleanup remains tracked and can be retried with Remove, Cleanup or Close
- Close stops new creation and waits for in-flight creates. Cleanup failure is returned; the runtime client stays available so Close can be retried
- Per-container operations are serialized. Stop/Remove cancel active execution. Cleanup snapshots currently tracked containers; callers should stop new submissions first or use Close
- Output writers are caller-owned and must not block indefinitely. Writer errors are returned while remaining output continues to drain

## Remaining limitations

This is a process-local backend, not a complete untrusted-code isolation platform. Container bookkeeping and logs are memory-only; crash/restart adoption and persistent orphan reconciliation are not implemented. Image content is retained in containerd's cache. Container metadata, tasks and writable snapshots are cleaned up, with errors reported.

There is no Kubernetes backend, bridge networking, arbitrary plugin sandboxing, Agent runtime capability scheduling, DSL runtime propagation, rootless support commitment, custom seccomp/AppArmor policy, or VM isolation. Mount inputs and access to the runtime socket must stay with trusted operators. A read-only bind flag does not establish a complete security boundary for arbitrary submounts. Do not expose host credentials, host root mounts, or runtime sockets to jobs.

See [the acceptance guide](../../docs/sandbox-containerd.md) for prerequisites, exact commands and the distinction between unit/spec tests and real runtime acceptance.
