# Offline containerd acceptance and operator opt-in

## Verified implementation and ordinary production-entry results

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

The independent **gate-enabled ordinary production-entry acceptance also passed
on 2026-10-14**:

- [Run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313),
  [job 115684232550](https://github.com/arcentrix/arcentra/actions/runs/38512588313/job/115684232550)
- PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c`; GitHub tested merge commit
  `1d2e29030741963897b7056c7d15249f2637d757` over the same main
  `e9dca0a9fef3e788842534811f2c015e17a005e8`
- All five named runtime tests and every workflow step passed. The whole-job log
  explicitly confirms: `production gate is open; actual NewProcess and StartWorker
  constructors are in use`. The configured production Control provider and Agent
  worker startup were exercised without the gate-off test-only assembly.
- [Artifact 11870328620](https://github.com/arcentrix/arcentra/actions/runs/38512588313/artifacts/11870328620),
  843 bytes, independently downloaded and verified against SHA-256
  `feb0707c16e9f0e0bf57ee7cd752dd0c8216937c98116252eacfc4965cde395f`
- `cleanup.json`: `passed: true`, `leaksBeforeFallback: []`, `cleanupErrors: []`;
  the private FIFO check and dependency teardown passed. Runtime versions, binary
  checksums and the verified immutable image match the earlier approved pins.

This second result verifies the compiled `OfflineRuntimeAccepted = true` path at
its exact tested revision, rather than inferring it from the earlier gate-off run.
Both results use race-enabled test binaries and disposable real dependencies.
They do not establish startup of packaged service binaries or release OCI images
in an intended deployment. Changed runtime code requires fresh final-code checks.

Operator policies remain **disabled by default**, including in a gate-enabled
build. No existing Agent, pipeline, label, configuration file or database row is
automatically opted in. Ordinary trusted baremetal execution and its existing
retry behavior are unchanged. The authorized CI privilege boundary is unchanged:
one disposable VM, one isolated rootful daemon, non-root Go tests, no host-security
changes or production secrets, a 25-minute limit, and mandatory cleanup evidence.

## Current offline profile and sequential-chain limits

- Trusted code, either one Agent job or one explicit linear chain of Agent jobs.
  Each job runs sequential shell steps in its own session/workspace; an Agent
  still executes one job at a time. This is not a hostile-code or multi-tenant
  isolation guarantee.
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
- Multiple jobs must form one explicit dependency chain: one root, at most one
  predecessor and successor per job, and all jobs reachable from that root.
  Disconnected roots, branches, joins, cycles and mixed host/deployment execution
  are rejected. Declaration order does not replace `dependsOn`.
- Upstream inputs travel only through registered artifact storage. Host staging
  precedes each new container; an upstream workspace, root filesystem or claim is
  never reused. See the chain contract below. Historical single-job acceptance
  does not establish this extension; check the exact new revision's hosted chain
  scenario and post-merge checks before relying on it.
- Images must already contain required tools/dependencies; no online package
  installation, bridge/CNI/host networking, HTTP/native/deployment steps,
  Kubernetes execution, standalone StepRun runtime tasks, or custom mounts.
- No release OCI image startup certification, rootless-runtime commitment, custom
  seccomp/AppArmor claim, VM isolation, HA fencing, private-provider acceptance,
  production deployment, load guarantee, or protection from the trusted host
  administrator/same-UID processes.

## Sequential-chain contract

A container pipeline can use one job or a single explicit dependency chain. Every
job is validated before dispatch; a bad later job must not allow the root to run.
The pipeline-level runtime, image and limits apply to every job. Operator policy
comes from the immutable Control startup configuration for API load, validation,
save and trigger, as well as Process submission. Pipeline JSON cannot enable or
replace that policy. Editing policy files requires a restart; project permissions
and approval checks continue to use their existing live authorization rules.

Each job gets its own Agent selection, JobRunID, claim, runtime binding digest,
container session and workspace. The next job is scheduled after successful
upstream completion and registered artifact backflow. The Agent seals all upstream
container processes before upload and completes exact cleanup before its terminal
report. Cleanup failure remains job failure and does not unlock the next job.
Lost terminal acknowledgments replay the original receipt rather than repeating
commands or uploads. A fresh downstream session never mounts the upstream
workspace or receives its unregistered files.

The existing condition semantics remain: a skipped predecessor can unlock its
successor, but missing required artifact input fails closed before container
startup. Explicit shell `continueOnError` applies only to a verified command nonzero exit.
Artifact steps cannot set it. Runtime/staging, log-persistence, report and cleanup
errors cannot be ignored to continue commands; only the existing receipt replay
and exact cleanup retries remain. A command-exit error joined with any additional
failure is also blocking.
No automatic command resumption or takeover is introduced by adding a chain.

The following definition uses only tools present in the pinned image and uses
registered storage to pass `dist/result.txt`. It requires reviewed, explicitly
enabled matching Control/Agent policies and an already configured artifact store.
It grants no credentials or network access to the containers.

```yaml
namespace: offline-chain
version: 1.0.0
runtime:
  type: containerd-offline-v1
  image: docker.io/library/alpine@sha256:1f3591b8a02ea153f41c5bba878ad477f63ab3d19349762cb77504db02a23e15
  resources:
    cpuLimit: 500m
    memoryLimit: 64Mi
jobs:
  - name: produce
    steps:
      - name: build
        uses: shell
        runOnAgent: true
        args:
          script: mkdir -p dist; printf 'offline artifact' > dist/result.txt
      - name: publish
        uses: builtin:artifacts
        action: upload
        runOnAgent: true
        args:
          paths: [dist/result.txt]
  - name: consume
    dependsOn: [produce]
    steps:
      - name: fetch
        uses: builtin:artifacts
        action: download
        runOnAgent: true
        args:
          job: produce
          paths: [dist/result.txt]
      - name: verify
        uses: shell
        runOnAgent: true
        args:
          script: test "$(cat dist/result.txt)" = "offline artifact"
```

Container `failed_step` and `whole_job` retry policies are still rejected when
saving or triggering a definition, before work is dispatched. Their session,
workspace and staging semantics require a separate implementation; existing
trusted-baremetal retry modes are unchanged. Branches, joins and disconnected
roots are intentionally outside this linear-chain extension.

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
3. Verify the reviewed gate-enabled build against the ordinary production-entry
   evidence above; changed runtime code requires its own final-code acceptance.
   Complete destination-specific release-image, storage, transport and durable-
   volume checks. Hosted test binaries do not certify the deployment artifact.
4. With explicit operator intent, set Control `pipeline.offlineRuntime.enabled`,
   Agent `agent.sandbox.enable`, and Agent `agent.sandbox.containerd.policy.enabled`
   to `true`, preserving the reviewed matching allowlist/maxima and all private
   paths. Restart both services; configuration is a startup snapshot.
5. Verify authenticated runtime readiness, submit one trusted bounded job, confirm
   its actual result/artifact/logs and cleanup, and test same-identity restart on
   that authorized destination before broader use. Labels alone never admit work.

Keep policy off if any prerequisite or result is uncertain. No test, migration,
example, or successful cleanup authorizes a production rollout by itself.
