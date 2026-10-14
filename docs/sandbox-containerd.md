# Containerd backend verification

## Status and scope

The backend, adapter, durable recovery and integrated single-job runtime paths passed [hosted run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022) on 2026-10-14. The exact PR head was `e7132be275c144a2eadc03b61e3c4f16dd302c4e`, tested as merge `12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`. Cleanup reported no FIFO/resource leaks or errors. [The acceptance record](offline-runtime-acceptance.md) preserves hashes, supported scope and operator configuration.

That successful implementation-level run kept the compiled production gate false. The candidate that sets it true must separately pass ordinary Control configuration/provider and Agent `StartWorker` entry points; that result is pending. Operator policies stay off by default. This evidence does not certify production deployment, release OCI images or hostile multi-tenant isolation. No privileged operation was performed in the restricted development workspace.

## Runtime-independent checks

From the repository root:

```sh
go test -race -count=1 ./pkg/sandbox
go test -tags=sandbox_acceptance -run '^$' ./pkg/sandbox
```

The second command **only compiles** the acceptance test. It does not run any acceptance checks. The normal test suite does not include the acceptance test, so it cannot quietly skip an unavailable daemon and appear to have validated real containers.

Coverage includes command/env/cwd/user forwarding, output capture/streaming, strict resources, one private network namespace, mount options, fast/nonzero exits, cancellation, stop escalation, cleanup error retention, concurrent lifecycle operations, and uncertain creation recovery. An adapter fixture exercises real containerd image/spec options against local test data; it is still not a real container run.

## Host prerequisites

Use a dedicated, disposable Linux development host or CI runner authorized to execute test containers:

- Supported rootful containerd/runc versions and an explicitly isolated Unix socket; the verified baseline is containerd 2.3.6/runc 1.5.2, using the existing 1.7 client API
- A working rootful OCI runtime, an available snapshotter, and an accessible cgroup-v2 hierarchy with usable CPU/memory controllers
- Ability to inspect the daemon's task PIDs through this host's `/proc` and corresponding cgroup paths. The test expects the daemon and test process to share this host view
- An operator-approved Alpine-compatible image containing `sh`, `sleep`, `id`, `cat`, `readlink`, and `ip`, selected by verified immutable digest
- Image already available/unpacked or permission and host connectivity to pull it. Container commands run without external networking
- A non-root test user, separate pre-existing mode-0700 user-owned workspace/journal/FIFO roots, an explicit daemon-side runc root, and enough disk/memory for the image and runtime mounts

This test does not install components, enable privileges or change host security settings. A rootless or differently arranged daemon may need a separately designed acceptance test; this command must not be reported as passed in that situation.

## Explicit acceptance command

Use the [reproducible hosted harness](../scripts/acceptance/CONTAINERD_RUNTIME.md). It supplies all opt-ins and private paths and runs the compiled tests as the unprivileged runner. Only its isolated daemon supervisor uses the already authorized disposable-VM privilege. This guide does not authorize a different host or permission changes.

For a separately approved preprovisioned environment, the backend test additionally requires explicit `ARCENTRA_CONTAINERD_SOCKET`, `ARCENTRA_CONTAINERD_RUNC_ROOT`, `ARCENTRA_CONTAINERD_FIFO_DIR`, `ARCENTRA_SANDBOX_TEST_IMAGE` and `ARCENTRA_SANDBOX_ACCEPTANCE=1`. Do not substitute default system paths or run `go test` as root.

The build-tagged test fails, rather than skips, when opt-in or a prerequisite is absent. Once requested, a missing runtime, denied mount, unavailable image, wrong effective limit, or failed cleanup is a failed/blocked acceptance run. Never describe an explicit compile-only check or prerequisite failure as a successful runtime run.

The test creates a uniquely named namespace/container and a temporary bind-mounted workspace. It checks:

1. Actual requested command, environment precedence, numeric UID/GID, working directory, stdout/stderr and nonzero exit code
2. Workspace persistence across exec steps and inability to see a separate host-only marker through the container's filesystem
3. Distinct PID/network namespace identifiers and no default network route
4. Effective cgroup-v2 CPU quota (`50000 100000`) and memory limit (`67108864`) from the host hierarchy
5. Timeout termination, cleanup despite a canceled caller context, repeated Remove, and absence of container metadata/writable snapshot afterward

The test reports cleanup errors. It does not remove the downloaded image cache. In-process cleanup is not crash recovery; after an interrupted test, an operator must inspect the test's `arcentra-acceptance-*` namespace and generated IDs before removing leftovers.

## Required evidence before runtime activation

Record the successful command, exact code revision, host/runtime versions, approved image digest and complete test output. Investigate any leak or missing controller rather than weakening assertions. Even a successful run validates only these bounded backend behaviors; it does not establish suitability for hostile multi-tenant code or certify the new Agent wiring. The linked run supplies adapter and integrated whole-job evidence for trusted staging, all-process sealing before artifact reads, cancellation, exact-owner restart cleanup and authenticated terminal-state reconciliation. Ordinary gate-enabled startup admission remains pending. Treat the gate-on candidate as unverified until its own ordinary-entry run and activation review complete; no passing compile-only, mock or standalone check opens it automatically.
