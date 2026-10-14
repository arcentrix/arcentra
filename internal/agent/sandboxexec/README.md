# Durable offline job sessions

This package does not enable a production execution mode by itself. The caller must
validate the operator's disabled-by-default runtime policy, authenticated execution
claim, controller binding, runtime-binding digest, approved image/resources, and
supported shell-only pipeline before allocating a session. A non-root caller with
access to the rootful containerd socket still holds effectively root-equivalent
host privilege; this package does not isolate the host from that trusted caller.
Job containers must not receive the socket or Agent configuration/state roots.

## Lifecycle

1. Open a `Controller` with an exact `DurableBackend` and separate, existing, private
   journal/workspace roots. Both roots must be absolute, symlink-free, owned by the
   Agent UID, and mode `0700`; their ancestors must be trusted. A journal is bound
   permanently to the controller identity, Agent identity, workspace root, and
   containerd namespace, and held with an exclusive process lock.
2. Inspect `Recoveries()`. Opening validates all records and resource ownership
   before the first removal, then removes only the exact recorded owned resources.
   Every interrupted claim becomes a cleanup-complete tombstone. `Ready()` stays
   false and allocation remains blocked until the caller reconciles authoritative
   business outcomes and calls `AcknowledgeCompletion` for each original claim.
   Cleanup completion never means a job succeeded, and recovery never starts or
   reruns a shell command.
3. `Allocate` records controller/Agent/pipeline/job/claim/runtime-binding identities,
   the namespace/container/snapshotter identity, and workspace identity. The runtime
   owner digest binds all of these fields. The complete runtime intent is durably
   written before `Create`; allocation creates only a private workspace. Journal
   records do not contain image credentials, commands, environment, input data,
   output, or execution results.
4. Stage trusted source and input artifacts in `Session.Workspace()` from the host.
   Then call `Start`. Durable sessions require an explicit canonical nonzero
   `UID:GID`; the production adapter must additionally require it to match the
   Agent's actual unprivileged UID/GID. No ownership changes are performed here.
5. Execute explicit shell/run steps through the normal executor manager so its
   logging, redaction, cancellation, and event behavior remains in effect. Commands
   run only in `/workspace`; there is no host-shell fallback. Steps are serialized.
6. Call `Seal` before any host-side output artifact traversal. It cancels and waits
   for an active step, then asks the runtime to kill and verify all container
   processes have stopped. Success preserves the workspace and disallows further
   execution. If sealing fails, do not read/upload output artifacts.
7. Perform the trusted host-side artifact operation, then call `Close`. Close removes
   the exact owned runtime resources before deleting the recorded workspace. Cleanup
   uses a bounded context independent of caller cancellation. Failures preserve the
   journal and workspace, block new allocation, and can be retried when ownership
   remains unambiguous.
8. After durable authoritative terminal evidence exists, call
   `AcknowledgeCompletion(originalClaimID)` to delete the cleanup tombstone. Never
   acknowledge solely because Close succeeded. This protects the interval between
   cleanup, terminal delivery, and acknowledgement. The control plane still owns
   deduplication after a tombstone has been acknowledged.

`Controller.Close` releases journal ownership only after every live session has
been closed. It can retain terminal tombstones for subsequent startup recovery.
It does not silently abandon a session whose cleanup failed.

## Fail-closed storage

Journal documents have a bounded canonical encoding and SHA-256 corruption
checksum. Writes use a new exclusive `0600` staging file, file fsync, atomic rename,
and directory fsync. Workspace directory creation/removal is also directory-synced.
Record count and size are bounded. Records, lock files, directory ownership,
permissions, hardlinks, symlinks, and workspace device/inode identity are checked.
The checksum is a corruption detector, not authentication against the trusted UID.

Unknown or interrupted staging files, invalid/corrupt records, foreign ownership,
namespace/binding mismatch, quota overflow, changed workspace identity, runtime
identity conflicts, or unresolved cleanup prevent readiness and retain evidence.
A live session can still stop its exactly owned processes using its immutable
in-memory identity when journal persistence or workspace validation fails. After
startup has validated every journal/runtime identity, later recovery failures also
attempt stop-only cleanup. Neither path deletes uncertain files, acknowledges an
outcome, or admits artifact reads. Corrupt startup identity records or foreign
runtime ownership cannot authorize even a stop operation.
There is no namespace-wide cleanup, resource-name guessing, auto-delete of unknown
files, or automatic execution retry. If a process crashes between workspace mkdir
and persisting its device/inode, startup deliberately requires operator review
rather than assuming that the unbound directory is safe to delete. Interrupted
journal staging is likewise retained for operator review.

This is a trusted-host execution/recovery foundation. It is not a claim of safe
hostile multi-tenancy or protection against a malicious local administrator or a
host process running with the same UID. Shell/tool writes may alter the contents of
the mounted workspace; callers must retain their existing artifact path validation.

## Compatibility and verification

`NewSession` remains the legacy standalone, auto-start, non-durable helper used by
existing tests. New production wiring must use `OpenController` and `Allocate`.
Legacy `Seal` removes the runtime via the original backend contract while retaining
the workspace. A legacy session cannot be migrated into a durable controller.

The ordinary tests use an injected runtime; they cover phase-boundary recovery,
exclusive ownership, tombstones, exact cleanup, no rerun, sealing/cancellation,
quotas, corrupt and linked files, workspace replacement, and journal write/sync/
rename failures. They do not constitute real-containerd acceptance. The separately authorized hosted runtime stage passed; it is distinct from these
injected tests and from the still-pending gate-enabled production-entry candidate.

### Durable runtime acceptance harness (hosted implementation pass)

[Run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022)
passed both durable tests, including `TestDurableControllerActiveRecoveryContainerdAcceptance`:
a separate Agent test process exits while its container/background writer is still
live, then a new client recovers the exact journaled resources. Foreign ownership
and workspaces remain untouched. The same hosted suite passed the complete
Process/Kafka/Agent/MySQL/S3 job and clean FIFO/runtime cleanup. Exact PR head:
`e7132be275c144a2eadc03b61e3c4f16dd302c4e`; tested merge:
`12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`.

The compiled production gate was false in that run. The gate-on candidate's normal
production configuration and `StartWorker` acceptance is **pending**. Operator
policies remain disabled by default. See [the evidence and operator guide](../../../docs/offline-runtime-acceptance.md)
for the exact verified scope and the independent release/deployment limitations.

`TestDurableControllerContainerdAcceptance` is separately build-tagged and requires
`ARCENTRA_DURABLE_SESSION_ACCEPTANCE=1` in addition to explicit authorization. It
requires Linux, cgroup v2, a nonzero Agent UID/GID, an already provisioned daemon,
and these explicit operator settings:

- `ARCENTRA_CONTAINERD_SOCKET`: the isolated authorized Unix socket
- `ARCENTRA_CONTAINERD_RUNC_ROOT`: explicit daemon-side runc state root
- `ARCENTRA_CONTAINERD_FIFO_DIR`: separate pre-existing private Agent-owned mode-0700 IO root
- `ARCENTRA_SANDBOX_TEST_IMAGE`: approved Alpine-compatible `image@sha256:...`
- `ARCENTRA_DURABLE_SESSION_NAMESPACE`: dedicated
  `arcentra-durable-acceptance-*` namespace
- `ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT`: existing private journal directory
- `ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT`: existing separate private workspace root

It exercises staged input, start, shell execution, a background writer that survives
the foreground step, Seal stopping that writer, host artifact reads, Close, and a
new client/controller reopening the exact pending terminal tombstone. A separately
owned bounded foreign resource must remain running through this recovery. Cleanup
uses exact refs only; failure evidence and the successful terminal tombstone are
retained. The harness has no authoritative control-plane outcome, so it deliberately
does not acknowledge that tombstone or make the journal reusable by inventing one.
A fresh explicitly approved root pair is required for another clean acceptance run
unless an operator reconciles the retained evidence.

Compile-only verification does not connect to containerd or run either acceptance
suite:

```
go test -tags sandbox_acceptance -run '^$' ./internal/agent/sandboxexec
```

Do not run the tagged test, provision its daemon, change host permissions, use sudo,
or install runtime software merely because the test compiles. Those remain a
separate authorized acceptance stage. Follow the existing hosted harness; a passing
implementation test does not automatically opt in a deployment or certify the
gate-enabled production-entry candidate.
