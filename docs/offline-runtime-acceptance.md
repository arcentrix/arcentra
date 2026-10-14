# Offline containerd acceptance and operator opt-in

## Verified result and the next gate

The first hosted implementation-level runtime acceptance passed on **2026-10-14**:

- [Run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022),
  [job 115672519129](https://github.com/arcentrix/arcentra/actions/runs/38508866022/job/115672519129)
- PR head `e7132be275c144a2eadc03b61e3c4f16dd302c4e`; GitHub tested merge commit
  `12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d` over main
  `e9dca0a9fef3e788842534811f2c015e17a005e8`
- Ubuntu 24.04 hosted VM; containerd 2.3.6, runc 1.5.2; the exact approved Alpine
  3.23.6 linux/amd64 digest and binary checksums in
  [the reproducible harness](../scripts/acceptance/CONTAINERD_RUNTIME.md)
- [Artifact 11868404908](https://github.com/arcentrix/arcentra/actions/runs/38508866022/artifacts/11868404908),
  843 bytes, independently downloaded and verified against SHA-256
  `2c391b491628347a0ca3b2f2a497f781e211f943af7659cc9f247bc4a5efa4b3`
- `cleanup.json`: `passed: true`, `leaksBeforeFallback: []`, `cleanupErrors: []`.
  This includes the private client FIFO check; dependency teardown also passed.
  `image.json` verifies manifest/config hashes and linux/amd64; `versions.txt`
  verifies the pinned containerd/runc binaries.

All five named tests passed: backend, standalone session, durable controller,
active-process crash/recovery, and whole-job acceptance. The whole-job path used
real Process scheduling, Kafka, container shell execution, authenticated TLS gRPC,
MySQL and S3-compatible object storage. It verified source checkout, sequential
shell steps, persisted logs, byte-checked artifacts, background-process sealing,
special-file/FIFO rejection, lost acknowledgments, same-journal restart, control-
queue cancellation, Agent step timeout, controller-first timeout with authenticated
late-report conflicts, retained quarantine audit, and subsequent healthy jobs.

At that exact revision `OfflineRuntimeAccepted` was **false**. A separately
build-tagged fixture configured the bounded Control policy and assembled the real
Agent worker components without opening the production startup gate. Those real
runtime results therefore establish the implemented code paths, not normal
production startup admission.

The next candidate changes the compiled `OfflineRuntimeAccepted` constant to
`true` and must rerun this hosted suite through the ordinary production Control
configuration/provider and Agent `StartWorker` entry points, with no test-only
policy/worker assembly. **Gate-enabled production-entry result: pending.** Record
its exact revision, successful run and clean receipt here only after verification.
A constant change, local unit test, or the earlier gate-off run is insufficient.
This remains distinct from launching packaged service binaries or release OCI
images in an intended deployment.

Operator policies remain **disabled by default**, including in a gate-enabled
build. No existing Agent, pipeline, label, configuration file or database row is
automatically opted in. Ordinary trusted baremetal execution and its existing
retry behavior are unchanged. The authorized CI privilege boundary is unchanged:
one disposable VM, one isolated rootful daemon, non-root Go tests, no host-security
changes or production secrets, a 25-minute limit, and mandatory cleanup evidence.

## Accepted first-profile limits

- Trusted code, exactly one Agent job, sequential shell steps and one job at a time
  per Agent. This is not a hostile-code or multi-tenant isolation guarantee.
- Runtime type `containerd-offline-v1`; immutable, explicitly allowlisted
  `image@sha256:...`; positive CPU/memory limits within both operator policies.
- Explicit nonzero numeric UID:GID matching the Agent; private PID/network
  namespaces, network `none`, no host runtime socket or arbitrary host mount.
- Trusted host Git checkout before the container runs; trusted host artifact
  publication only after every container process is sealed. Host Git, object-store
  and image operations may use network access. Container commands cannot.
- No automatic retries, execution resumption or takeover after a crash. Recovery
  removes only exact journaled resources and reconciles the original authenticated
  terminal outcome. A retry requires an explicitly requested fresh run.
- The one-job validator means upstream artifact-input DAG jobs remain unsupported.
  An artifact-download staging hook is not evidence that sequential container DAGs
  are supported. The hosted test does not claim that capability.
- Images must already contain required tools/dependencies; no online package
  installation, bridge/CNI/host networking, HTTP/native/deployment steps,
  Kubernetes execution, standalone StepRun runtime tasks, or custom mounts.
- No release OCI image startup certification, rootless-runtime commitment, custom
  seccomp/AppArmor claim, VM isolation, HA fencing, private-provider acceptance,
  production deployment, load guarantee, or protection from the trusted host
  administrator/same-UID processes.

## Review-only configuration template

These are complete **runtime-specific** blocks to merge into existing reviewed
Control and Agent configuration. They intentionally keep all opt-in flags false;
this is not a ready-to-start sandbox configuration. Preserve existing database,
broker/storage, TLS and Agent-identity settings separately. No live credential or
new authentication token is included. The `.invalid` endpoint and paths are
examples, not a request to contact or provision a service.

Control:

```toml
[pipeline.offlineRuntime]
enabled = false
allowedImages = ["docker.io/library/alpine@sha256:1f3591b8a02ea153f41c5bba878ad477f63ab3d19349762cb77504db02a23e15"]
maxCpu = "500m"
maxMemory = "64Mi"
```

Agent:

```toml
[grpc]
serverAddr = "control.example.invalid:9090"

[grpc.tls]
enabled = true
caFile = "/etc/arcentra-agent/tls/controller-ca.pem"
serverName = "control.example.invalid"

[agent]
mode = "sandbox"
maxConcurrentJobs = 1
workspaceDir = "/var/lib/arcentra-agent/offline-workspaces"
tempDir = "/var/lib/arcentra-agent/tmp"
cacheDir = "/var/lib/arcentra-agent/cache"
jobTimeout = 600
enableDocker = false
deniedCommands = []
# agent.resourceLimits must be absent/empty; it is not sandbox enforcement.

[agent.identityPersistence]
enabled = true
directory = "/var/lib/arcentra-agent/identity"

[agent.sandbox]
enable = false
runtime = "containerd"

[agent.sandbox.containerd]
namespace = "arcentra-offline-node-a"
unixSocket = "/run/arcentra-runtime/containerd.sock"
runtimeRoot = "/var/lib/arcentra-runtime/runc"
journalDir = "/var/lib/arcentra-agent/offline-journal"
fifoDir = "/var/lib/arcentra-agent/offline-fifo"
user = "1001:1001"
network = "none"
image = ""

[agent.sandbox.containerd.resources]
cpu = ""
memory = ""

[agent.sandbox.containerd.policy]
enabled = false
allowedImages = ["docker.io/library/alpine@sha256:1f3591b8a02ea153f41c5bba878ad477f63ab3d19349762cb77504db02a23e15"]
maxCpu = "500m"
maxMemory = "64Mi"

[taskQueue]
autoCommit = false
messageFormat = "sonic"
messageCodec = "sonic"
```

`1001:1001` is an example, not permission to create a user or change ownership.
Substitute the actual pre-existing non-root Agent UID:GID. Journal, workspace and
FIFO roots must be distinct, pre-existing, mode-0700, Agent-owned directories with
trusted non-symlink ancestors. Preserve them and the authenticated identity across
restart; do not delete/replace a bound FIFO root or create a new identity to bypass
recovery. Identity storage is separate from all job workspaces.

The daemon's socket, root/state, shim sockets and `runtimeRoot` are separately
operator-provisioned paths. Only the trusted Agent/operator may access that daemon.
Access to a rootful containerd API is a host-high-privilege, effectively root-
equivalent trust relationship. Running the Agent process as non-root does not
isolate the host from the Agent itself or anyone who can use its daemon socket.
The boundary tested here is the bounded job container: it receives neither that
socket nor Agent configuration, identity, journal, FIFO or sibling-workspace roots.
Use a dedicated host whose runtime access is securely provisioned by its operator;
this template does not create credentials or grant daemon access automatically.
`runtimeRoot` is daemon-side runc state; `fifoDir` is Agent-side task/exec IO.
Changing containerd `state` or process `TMPDIR` does not configure either one
implicitly. Do not chmod a system socket/FIFO path, alter host security settings,
mount sockets into job containers, or run the Agent as root to fix validation.

Image/resource legacy defaults stay empty. Each pipeline must explicitly request
its allowlisted digest and bounded hard limits; the immutable dispatched runtime
binding is authoritative. For a different toolchain image, review its exact digest
and resource needs at both Control and Agent before changing the allowlists. The
small Alpine image above demonstrates the accepted shell profile, not arbitrary
build-tool compatibility.

## Explicit migration and opt-in sequence

1. Preserve existing journals, identity, outbox and audit evidence. Reconcile
   unresolved execution effects; do not automatically rerun interrupted commands.
2. Rehearse backup/restore and the [runtime schema migration](migrations/004_offline_runtime_contract.md)
   on a representative staging database. Run `arcentra-cli migrate --conf
   /absolute/path/to/reviewed-config.toml` explicitly on the approved destination.
   Startup never migrates automatically. Do not backfill fabricated runtime bindings.
3. Verify the exact gate-enabled build's still-pending production-entry acceptance,
   then complete destination-specific release-image, storage, transport and durable-
   volume checks. The earlier implementation-level result is not a substitute.
4. With explicit operator intent, set Control `pipeline.offlineRuntime.enabled`,
   Agent `agent.sandbox.enable`, and Agent `agent.sandbox.containerd.policy.enabled`
   to `true`, preserving the reviewed matching allowlist/maxima and all private
   paths. Restart both services; configuration is a startup snapshot.
5. Verify authenticated runtime readiness, submit one trusted bounded job, confirm
   its actual result/artifact/logs and cleanup, and test same-identity restart on
   that authorized destination before broader use. Labels alone never admit work.

Keep policy off if any prerequisite or result is uncertain. No test, migration,
example, or successful cleanup authorizes a production rollout by itself.
