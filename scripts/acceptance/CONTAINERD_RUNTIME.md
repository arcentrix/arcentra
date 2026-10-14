# Disposable containerd runtime acceptance

`.github/workflows/containerd-acceptance.yml` exercises the actual runtime on a
fresh GitHub-hosted `ubuntu-24.04` VM, with a 25-minute job limit. It runs for PRs
and pushes to `main`; it does not use `pull_request_target`, self-hosted runners,
repository secrets, OIDC, write permissions, or retained checkout credentials.
The first implementation-level run passed with
`pkg/taskqueue.OfflineRuntimeAccepted = false`. The independent gate-on run also
passed normal production configuration and actual `NewProcess`/`StartWorker` entry
points. Both use hosted test binaries, not packaged services or release OCI images.
Operator policies remain disabled by default. This job alone does
not opt in deployments or establish production readiness. See [the exact acceptance
record and configuration](../../docs/offline-runtime-acceptance.md).

## Scope and execution

The authorized entry point is `scripts/acceptance/run_containerd_runtime.sh` in
that workflow. It refuses a root test process, non-GitHub-hosted environment,
non-Ubuntu-24.04 system, unsupported architecture, or absent cgroup v2. Do not run
the privileged helper on a workstation, a shared host, the development container,
or a production machine. A missing capability is a failed check, never a reason
to modify host security settings.

The workflow compiles race-enabled test binaries as the unprivileged runner,
reuses the existing Compose fixtures for synthetic MySQL/Redis/Kafka/MinIO, and
creates the fresh `arcentra_acceptance_containerd` schema. Dependency services
publish only loopback ports and use a unique Compose project. The MinIO binary is
built from the existing pinned official release source; it is never published.

The harness downloads containerd and runc into a fresh private
`/tmp/arc-cd.<run-id>.<attempt>.<random>` directory, verifies fixed SHA-256 pins,
and starts one root supervisor through `sudo -n env -i`. That supervisor starts
one rootful containerd with its own root, state, temporary files, gRPC socket,
TTRPC socket, shim socket directory, and runc state root. The daemon's API socket
is accessible to this VM's runner only. The Go tests run with nonzero UID/GID and
a clean environment, private HOME/TMPDIR, and explicit synthetic fixture values.
They never run through sudo. The client-side FIFO root is a precreated,
runner-owned mode-0700 `work/fifo` directory inside this same private run root;
it is separate from the daemon's privileged state. The rootful API socket grants
the trusted runner/Agent effectively root-equivalent host access; a non-root Go
process does not isolate the VM from that caller. The tested boundary is the job
container, which never receives that socket or the Agent configuration/state roots.
This privilege exists only in the authorized disposable VM.

Version-4 containerd configuration explicitly names both server sockets and the
shim socket directory; overriding just `state` is insufficient. CRI, NRI, the
shared `/opt/containerd` plugin, restart manager, TCP gRPC, debug, and metrics
listeners are disabled. No system containerd or Docker service is restarted or
reconfigured. There are no sysctl, AppArmor, firewall, user-group, or host network
changes. Tested job containers have private PID/network namespaces and offline
network policy; the host Docker socket and host network are never mounted or
shared with them.

`ARCENTRA_CONTAINERD_RUNC_ROOT` is the daemon-side absolute owned state directory
passed to every fixture's optional `ContainerdConfig.RuntimeRoot`. It is created
inside this one runtime's private root and rejects symlink/host default paths.
The Agent's optional operator configuration `containerd.runtimeRoot` is distinct
from pipeline input; pipelines cannot choose it. An empty production setting
retains the existing daemon behavior. This fixture always supplies it.

`ARCENTRA_CONTAINERD_FIFO_DIR` supplies every fixture's `ContainerdConfig.FIFODir`
and the Agent's operator-only `containerd.fifoDir` setting. Containerd's Go client
creates task/exec stdio FIFOs on the client host, so daemon `state` and `TMPDIR`
alone do not move its default `/run/containerd/fifo` path. This fixture explicitly
uses the private runner-owned directory. It never changes ownership or mode on
the system FIFO directory, and never grants the Go tests root privileges. The
supervisor validates exact ownership, mode 0700, no symlink components and an empty
FIFO root before starting. It verifies that the root is still valid and empty
before final cleanup; any remaining FIFO entry is a failed acceptance receipt.

## Exact tests and environment contract

All runtime tests require the `sandbox_acceptance` build tag. Missing explicit
opt-in or required data fails instead of skipping. The harness runs:

1. `TestContainerdAcceptance` in `./pkg/sandbox`: actual execution, streams,
   workspace isolation, UID/GID, resource limits, offline network, cancellation,
   and removal.
2. `TestSessionContainerdAcceptance`,
   `TestDurableControllerContainerdAcceptance`, and
   `TestDurableControllerActiveRecoveryContainerdAcceptance` in
   `./internal/agent/sandboxexec`: adapter execution, durable ownership, active
   child-process exit/restart recovery, and exact owned-resource cleanup.
3. `TestRealContainerJobAcceptance` in `./internal/agent/taskqueue`: actual
   Process submission through Kafka, authenticated Agent status/log delivery,
   container execution, MySQL run state and S3 artifact content.

The shell sets the three existing sandbox/session/durable opt-ins, the new
`ARCENTRA_CONTAINERD_ACCEPTANCE` and `ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE`
flags, `ARCENTRA_CONTAINERD_SOCKET`, `ARCENTRA_SANDBOX_TEST_IMAGE`,
`ARCENTRA_CONTAINERD_RUNC_ROOT`, and `ARCENTRA_CONTAINERD_FIFO_DIR`. It supplies a
dedicated
`arcentra-durable-acceptance-<run>-<attempt>` namespace plus separate private
journal/workspace roots, and an
`arcentra-containerjob-acceptance-<run>-<attempt>` namespace/private root for the
whole-job test. Dependency variables use only the fixed synthetic loopback
services. Raw environment data is never uploaded.

## Cleanup is part of the result

The supervisor tracks namespace names from its isolated daemon. On normal exit,
error, or signal, it inventories tasks, container records, writable snapshots,
mounts, namespace-scoped cgroups, and client FIFO entries before fallback cleanup.
Expected immutable image cache snapshots and journal tombstones are not leaks. A leaked resource
fails the receipt even when fallback successfully removes it.

Fallback signals only exact task IDs on the owned socket, exact namespace cgroup
PIDs, and shims whose executable is this run's verified binary. Process signals
use pidfds plus executable/start-time checks to avoid PID reuse. Mount cleanup
matches complete owned path components, unmounts deepest-first without lazy
unmount, and removes cgroup directories only when empty. No system-wide kill,
Docker prune, wildcard cgroup removal, or recursive host mount cleanup is used.
Data removal occurs only after remaining mounts/resources have been checked.

The root supervisor has an independent 18-minute lifetime. CI termination also
hits the shell trap/supervisor signal handler, and the hosted VM is discarded
when its job ends. Missing cleanup receipt, cleanup error, missing named test,
wrong test filter, skip, or test failure cannot produce a green result. If a leak was found or cleanup
cannot finish, owned paths are retained for diagnosis until VM disposal; the
receipt reports that failure instead of claiming cleanup succeeded.

Uploaded artifacts contain only the image verification, runtime version pins,
and cleanup receipt. A failed receipt includes at most 20 bounded daemon warning
or error lines with URL credentials and token/password/secret fields redacted.
Application logs, TLS keys, authentication state, workspaces, database data,
container root filesystems, and complete daemon logs are not uploaded.

## Reproducible pins and provenance

Pins live in `containerd-pins.env`. They were verified on **2026-10-13** by
fetching the official release binaries and hashing their bytes without launching
a daemon. All release dates precede that verification date:

- containerd **2.3.6**, released 2026-09-24, the supported 2.3 LTS line:
  [official release](https://github.com/containerd/containerd/releases/tag/v2.3.6),
  [support lifecycle](https://containerd.io/releases/),
  [asset checksums](https://github.com/containerd/containerd/releases/expanded_assets/v2.3.6),
  [upstream checksum file](https://github.com/containerd/containerd/releases/download/v2.3.6/containerd-2.3.6-linux-amd64.tar.gz.sha256sum).
  The linux-amd64 archive is
  `df806bb9b86653ebc94822a7076642ff3c3614324dbc1b511bdf75ac1669b762`.
- runc **1.5.2**, released 2026-09-25:
  [official release](https://github.com/opencontainers/runc/releases/tag/v1.5.2),
  [asset checksums](https://github.com/opencontainers/runc/releases/expanded_assets/v1.5.2),
  [upstream checksum file](https://github.com/opencontainers/runc/releases/download/v1.5.2/runc.sha256sum).
  `runc.amd64` is
  `599f6f94ff8c5057241eff0d54c3c74f95c34935b6457b33fe545defc61e9488`.
- Docker Official Alpine **3.23.6 linux/amd64**, created 2026-09-17:
  `docker.io/library/alpine@sha256:1f3591b8a02ea153f41c5bba878ad477f63ab3d19349762cb77504db02a23e15`.
  [Immutable Docker Official Image metadata](https://github.com/docker-library/repo-info/blob/c667a56ce08d1768d5d003dfe7b7b290065cec90/repos/alpine/remote/3.23.md),
  [official image source revision](https://github.com/alpinelinux/docker-alpine/tree/00d3407329e4c65f237cc1e325a4ab0cb4974df4/x86_64).
  Its manifest bytes and the registry `Docker-Content-Digest` header matched that
  SHA-256; the image config bytes also matched their declared digest and proved
  `linux/amd64`. `verify_containerd_image.py` repeats these read-only registry
  checks before starting the daemon. Pulling by digest cannot silently follow a
  moved tag. Public anonymous pull tokens are kept only in memory.

The fixed binary hashes are checked locally; downloading an adjacent checksum
file is not treated as sufficient verification. Updates require reviewing the
new official release, support window, digest, and all runtime tests.

Configuration references:
[containerd 2.3.6 config](https://github.com/containerd/containerd/blob/v2.3.6/docs/man/containerd-config.toml.5.md),
[shim socket configuration](https://github.com/containerd/containerd/blob/v2.3.6/core/runtime/v2/shim_manager.go),
[runc state root](https://github.com/containerd/containerd/blob/v2.3.6/cmd/containerd-shim-runc-v2/process/init.go).

## Safe offline verification

These commands perform no privileged operation and launch no daemon/container:

```sh
bash -n scripts/acceptance/run_containerd_runtime.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/acceptance -p 'test_containerd_runtime.py' -v
go test -tags=sandbox_acceptance -run '^$' \
  ./pkg/sandbox ./internal/agent/sandboxexec ./internal/agent/taskqueue
```

Offline checks and a committed workflow are not proof of runtime acceptance.
Only the exact commit's completed hosted job, all named tests, and a clean cleanup
receipt establish that result. [Run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022)
now provides the gate-off implementation result at PR head
`e7132be275c144a2eadc03b61e3c4f16dd302c4e` (tested merge
`12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`). Its artifact
`11868404908` SHA-256 is
`2c391b491628347a0ca3b2f2a497f781e211f943af7659cc9f247bc4a5efa4b3`;
cleanup reported no leaks or errors. Independent gate-enabled [run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313) also passed
all five tests, with the whole-job path using actual `NewProcess` and `StartWorker`
constructors, at
PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c` (tested merge
`1d2e29030741963897b7056c7d15249f2637d757`). Its artifact `11870328620` SHA-256 is
`feb0707c16e9f0e0bf57ee7cd752dd0c8216937c98116252eacfc4965cde395f`;
independent download verification confirmed `passed: true`, empty
`leaksBeforeFallback` and empty `cleanupErrors`, matching runtime/image pins and
successful dependency teardown. This second result establishes the ordinary-entry
path at that revision; packaged-service, release-OCI and destination-specific
validation remain separate.
