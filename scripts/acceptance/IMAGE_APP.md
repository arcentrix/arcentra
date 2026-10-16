# Image-backed actual application journey

This is a separate opt-in disposable GitHub-hosted Linux x64 fixture. The host-binary
`run_actual_app.py` profile remains the default and retains its original public
validation contracts. `ImageFixture` inherits its entire exercise, failed-compiler
run/zero-artifact check, corrected source/new commit/new run, real Agent test/build,
independent read-only API observer, browser/API download byte comparisons, manifest
SHA/length/source checks, downloaded binary behavior checks, and final reread of
the original failed run. No database bypass, backend mock or host compiler fallback
is provided.

Local unit tests are pure command/response fixtures and do not run Docker:

```sh
PYTHONPATH=scripts/acceptance python -m unittest scripts/acceptance/test_image_runtime.py scripts/acceptance/test_run_actual_app.py
```

## Reviewed input contract

The mode-0600 JSON image manifest accepts exactly these fields (no duplicate keys):

- `schemaVersion`: integer `1`
- `runId`, `attempt`: nonzero decimal strings matching `GITHUB_RUN_ID` and `GITHUB_RUN_ATTEMPT`
- `sourceSha`: full lowercase 40-character SHA matching `GITHUB_SHA`
- `composeProject`: exactly `arcentra-image-<runId>-<attempt>`
- `networkId`: exact local 64-character network ID
- `images`: exactly `control`, `agent`, `agentBase`; each is exactly `{id, sourceSha}`.
  `id` must be a local `sha256:` plus 64 lowercase hexadecimal digits. Every image's
  source SHA must match the top-level SHA. `agent` must differ from `agentBase`.
- `toolchain`: exactly `goVersion=go1.25.6`, `pythonVersion=3.11.14`,
  `goSourceDigest=sha256:f4490d7b261d73af4543c46ac6597d7d101b6e1755bcdd8c5159fda7046b6b3e`,
  `pythonSourceDigest=sha256:65a93d69fa75478d554f4ad27c85c1e69fa184956261b4301ebaf6dbb0a3543d`.
  These are fixed reviewed official Go 1.25.6-bookworm and Python 3.11.14-slim-bookworm
  source index digests, not runtime-selectable tool sources.

Images and containers carry exact labels:
`io.arcentra.acceptance.image=v1`, `.owner=image-journey-v1`, `.run=<runId>`,
`.attempt=<attempt>`, `.sha=<sourceSha>`, `.role=<role>` (each abbreviated label suffix
extends `io.arcentra.acceptance.image`). Image roles are `control`, `agentBase`, `agent`;
container roles are `control`, `agent`, `cli`, `git`, `toolchain`.

The runtime inspects local image IDs, Linux/amd64, exact unmodified Dockerfile.ci
entrypoints, empty Cmd, working directory `/var/lib/arcentra`, no image volumes or
OnBuild commands, and all labels. The tested stock Agent layers must be a strict
prefix of the derivative's layers. This checks this run's build relationship; it is
not a supply-chain attestation. The standalone bounded toolchain probe runs inside
the exact derivative and verifies both versions. It explicitly overrides only this
probe's entrypoint. Control/Agent application launches use no entrypoint or command
overrides. CLI and Git reuse the exact Control image with fixed helper commands.

## Topology and permissions

The caller must create the existing disposable Compose dependency bridge first.
The fixture verifies its immutable ID, bridge/local status, exact project name and
Compose labels. It never creates or deletes a network and never accepts arbitrary
Docker arguments, image tags, remote URLs, mounts or endpoints. Remote/custom
Docker contexts, hosts and TLS settings are rejected, and Docker commands receive
a fresh minimal environment.

Internal endpoints are `control:19090`, `mysql:3306`, `redis:6379`, `kafka:9092`,
`minio:9000`, and `git://git:9418/acceptance.git`. HTTP Control/Agent binds inside
the container, publishing only `127.0.0.1:18080/18081`. The read-only Git daemon
publishes `127.0.0.1:19418` only for the host readiness probe. Browser/API request
validation remains strictly loopback. Browser profile is `image-build-v1`.

Each application receives only its exact private mode-0700 configuration directory
(readonly) and private writable state directory. Files are mode 0600. Control gets
its server certificate/key, Agent gets only the CA certificate, and CLI gets only
database configuration. The CA signing key, browser state/downloads, whole fixture
root and Docker socket are never mounted. Agent state persists across recreation;
the original authenticated identity digest is verified by the shared journey after
the enrollment token is removed from the remounted configuration directory.

All containers run as the non-root runner UID/GID with readonly rootfs, cap-drop
ALL, no-new-privileges, no restart policy, bounded local logs, and private bounded
/tmp. Control: 512 MiB/1 CPU/128 PIDs; Agent: 1536 MiB/2 CPU/256 PIDs; helpers:
256 MiB/0.5 CPU/64 PIDs. At least 3 GiB available runner memory is required.
Local logs explicitly use `max-size=8m`, `max-file=1`, and `compress=false`;
Docker's local driver cannot combine its default compression with one retained file.
Agent home/cache/tmp and build workspaces use its private state; Go module downloads
are disabled. This is trusted baremetal execution **inside an application Agent
container**, not containerd job isolation. The stock Agent image is not claimed to
include the test compiler/Python tools.

## Invocation and unconditional cleanup

Required environment includes the host profile's existing opt-in plus
`ARCENTRA_IMAGE_JOURNEY=1`, Linux/X64 runner markers, and exact GitHub run/attempt/SHA.
Use a fresh ownership filename outside the temporary fixture directory:

```sh
python scripts/acceptance/run_image_app.py \
  --image-manifest "$RUNNER_TEMP/image-manifest.json" \
  --ownership-file "$RUNNER_TEMP/image-ownership.json" \
  --browser-cwd /path/to/reviewed/frontend \
  --evidence-dir /path/to/new/evidence -- <reviewed-browser-command>
```

An independent workflow step with `if: always()` must run, even after cancellation:

```sh
python scripts/acceptance/run_image_app.py \
  --image-manifest "$RUNNER_TEMP/image-manifest.json" \
  --ownership-file "$RUNNER_TEMP/image-ownership.json" --cleanup-only
```

Ownership intent is atomically persisted before every create, then updated with the
immutable ID. Handles inspect actual daemon state; they never treat a Docker client
PID as application liveness. Timeouts, cancellation and unknown state block new
admission. Cleanup has an independent 180-second deadline, uses exact intended names
plus inspected immutable ID/image/owner/run/attempt/SHA/role, and reconciles confirmed removals with a post-removal absence check. Pending or
uncertain creates without an observed immutable ID remain unknown even when an
exact-name query is empty; cleanup fails closed and independent cleanup retries may
only resolve that uncertainty by observing and removing the owned container. It stops and inspects before removal and
alias reuse; foreign ownership is never removed. No global prune is used. The
independent cleanup step requires the same manifest, but does not require a browser,
application health, toolchain health, or the temporary mount paths still existing.

The journey has a 300-second hard signal deadline covering shared browser/HTTP/tool
operations. Every Docker call clamps to the remaining journey budget and drains
stdout/stderr under a combined 2 MiB ceiling; abnormal exit kills its owned CLI
process group even if the parent already exited. Cleanup gets 180 seconds, including
diagnostic capture, followed by bounded host-child termination. The workflow must
reserve this full cleanup margin.
Image provisioning has its own separate budget. Sanitized application logs and the
shared build-verification receipts remain the evidence. An entrypoint smoke or pure
unit-test pass is not proof of a healthy service or completed image-backed build.

The public `image-runtime-summary.json` is written beside the ownership file on
normal completion, cleanup failure, and independent cleanup. It includes only exact
run/attempt/SHA, stock-versus-test-derived image IDs, container roles/immutable IDs,
and confirmed/unknown removal status. First failures add validated static operation,
phase, role, return-code and reason fields. Only the credential-free toolchain
probe's start failure may include bounded fixed-vocabulary projections of daemon
errors plus its hardcoded create contract; unknown text is redacted, and keyword
features are diagnostic hints rather than a root-cause verdict. No raw daemon
errors, private paths, configuration or credentials are included. Cleanup/evidence failure removes the successful build
verification receipt and writes a failed overall result; browser success alone cannot
stand in for confirmed runtime cleanup.

Readiness GET probes tolerate transient connection resets/disconnects within the
existing deadline, while checking container liveness before each probe. Only HTTP
200 passes. Persistent transport failure or process exit fails; registration and
other mutation requests are never retried by this handling. Two real loopback TCP
regressions run only with the hosted workflow's explicit
`ARCENTRA_TEST_LOOPBACK_PROBES=1` opt-in; ordinary local tests skip them.

## Hosted workflow budget and evidence

The image journey workflow reserves cleanup time from the outset: ordinary shell
work shares a 14-minute absolute deadline, each official action is preceded by a
budget check and limited to two minutes, and the job has a 25-minute hard cap.
Owned application and image cleanup plus dependency teardown run in one always
step with a six-minute cap. A failed or interrupted journey or cleanup invalidates
the successful build receipt before evidence upload. The final upload is limited
to one minute and includes only allowlisted screenshots and sanitized JSON.

The dependency cleanup receipt verifies absence of this Compose project's
containers (including stopped), networks and volumes. Official base images,
dependency images and build cache remain scoped to the disposable VM until its
provider disposes of it; no global prune is performed. No image is pushed to a
registry. Stock image loader/entrypoint acceptance is a separate workflow; this
journey adds an explicitly provisioned test Agent toolchain, not an assertion that
the stock Agent includes a compiler.
