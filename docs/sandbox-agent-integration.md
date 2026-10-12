# Agent sandbox integration boundary

## Current status

Production remains fail-closed: `ValidateExecutionConfig` still rejects sandbox mode and the pipeline validator still rejects container runtimes, images and resource policies. No bootstrap registration or queue worker dispatch uses this adapter. `internal/agent/sandboxexec` is a standalone integration building block, not a shipped Agent execution mode or an isolation certification.

The implementation host has no usable containerd/runc/socket/cgroup-v2 environment. Fake-backend tests exercise the contract without running a container. The optional host test below is only runtime evidence when it actually runs successfully on an authorized host. The separate [backend acceptance](sandbox-containerd.md) is still required; the session test is not a substitute for namespace, cgroup and isolation assertions.

## Standalone session contract

- An operator supplies an existing absolute, canonical workspace root with no symlink components or group/other write permission. Keep this root and its ancestor path under trusted host control for the whole job. These checks are defense in depth, not protection against another privileged host actor.
- Each session creates a fresh, private `0700` subdirectory under that root using a root-scoped filesystem handle. Neither queue payloads nor step requests can choose a different host mount source. The container receives exactly that directory at `/workspace` and no Agent configuration, socket, credential file, cache root or sibling job directory.
- Image, CPU and memory limits are explicit. The selected image must contain `sh` and `sleep`. The init command runs at `/`; shell steps run at `/workspace` with a numeric UID/GID. By default it uses the Agent filesystem owner's numeric UID/GID. A configured override must already have workspace permissions; the adapter never changes host ownership or grants extra access.
- Only `uses: shell` or `builtin:shell`, with an empty action or `run`, can execute. Commands use `sh -ec`. Environment comes from the explicit request and the approved image configuration, without inheriting the Agent's process environment.
- All steps in a session are serialized. Runtime errors, timeout, cancellation or a replaced workspace stop further execution. A normal nonzero exit can be followed by another step if the job's policy permits it.
- Use `Manager.ExecuteWithExecutor(ctx, request, session)` so the existing declared-secret redaction (`secrets.*` environment values), durable logs and events remain active. Arbitrary environment values are not automatically treated as secrets. A failed log append cancels the session command. Do not register the adapter alongside host executors and use fallback selection for a sandbox job.
- `Close(ctx)` cancels an active command, waits for it, removes the container and only then deletes the workspace through its root-scoped handle. It uses an independent bounded cleanup context even after the job was canceled. Complete authorized artifact publication before Close.
- A failed container removal preserves the directory and returns an error. Repeated Close retries cleanup; successful Close is idempotent. A constructor failure can return a non-nil session when cleanup still needs retrying. Never discard that handle or report clean completion in that case.
- A backend with uncertain creation must return the resource ID. The session does not close a shared backend connection. Process crashes still require a durable ownership registry and reconciliation before production integration.

Filesystem deletion is synchronous. A huge file tree, blocked filesystem or a backend violating its cancellation contract can exceed normal expectations; no successful cleanup should be reported before the filesystem and runtime actually confirm it. An execution failure does not authorize deleting the shared root or unrelated runtime resources.

## Network and data boundaries requiring product decisions

The current backend accepts only `network: none`; it creates a private network namespace without outbound access. There is no bridge, host-network mode, CNI provisioning or DNS policy. Package managers, remote build tools and deployments requiring network access will fail inside the session. The bounded initial use case therefore requires prebuilt toolchain images plus vendored/pre-fetched dependencies and local build inputs. Image resolution itself happens through the trusted containerd daemon and can pull an approved image before the offline command starts; runtime image allowlists, pinning and registry authentication remain host policy work.

Source clone and object-storage artifact transfers currently run in the host Agent using its credentials and network access. Future wiring must explicitly keep these as trusted preparatory/transfer operations, with the session-owned workspace as their sole file boundary. Do not expose storage credentials or Git authentication helpers to commands by mounting the Agent's wider filesystem. Artifact file operations must retain their existing traversal/symlink defenses. Decide whether trusted host preparation is acceptable for the intended tenant model; moving it into a separate controlled networked stage is a different design, not a reason to silently enable container networking.

The existing worker accepts arbitrary nonempty `payload.Workspace` values and uses host execution; that path cannot be reused as a sandbox bind-mount policy. The session avoids that path entirely. Tightening existing baremetal workspace compatibility requires its own focused worker change and tests, including legacy standalone step dispatch.

## Remaining production integration

1. Obtain and retain actual backend and adapter acceptance output with the exact commit, host/runtime versions and approved image digest. Do not weaken assertions to fit an incapable host.
2. Define the supported offline product profile: image policy, numeric user, mandatory resources, source/artifact staging and unsupported step types. Add explicit immutable runtime/image/resources fields to job payloads. Do not infer a container request from a label or silently discard unsupported fields.
3. Advertise validated runtime capabilities from registered Agents, and dispatch only to compatible Agents with sufficient admission capacity. Current selection filters online status and labels, not container runtime or cgroup readiness.
4. Create one session for the whole job, feed the allocated workspace into source/artifact operations, and force shell execution through its request-level adapter. Builtin artifact transfer remains a separately approved host operation. Reject generic native/HTTP plugin execution for this profile.
5. Join cleanup failures into the job outcome, report leaked resource IDs without secret-bearing options, and reconcile owned resources across restart. Keep terminal-report retry separate from executing a job again.
6. Prove end-to-end claim, cancellation, durable log failure, artifact transfer and shutdown behavior before deliberately changing either fail-closed gate. A StepRun-only task must not be allowed to bypass a required job session.

## Runtime-independent checks

```sh
go test -race -count=1 ./internal/agent/sandboxexec ./pkg/sandbox
go test -tags=sandbox_acceptance -run '^$' ./internal/agent/sandboxexec ./pkg/sandbox
```

The second command compiles both acceptance tests only; it proves no actual runtime behavior.

## Optional capable-host adapter test

Follow all prerequisites and restrictions in [backend acceptance](sandbox-containerd.md), including a disposable authorized Linux host, working rootful containerd/runc, usable cgroup-v2 CPU/memory controllers and an approved Alpine-compatible image. Run the backend test first and keep its full results.

```sh
export ARCENTRA_SANDBOX_SESSION_ACCEPTANCE=1
export ARCENTRA_CONTAINERD_SOCKET=/run/containerd/containerd.sock
export ARCENTRA_SANDBOX_TEST_IMAGE='your-approved-alpine-image@sha256:YOUR_VERIFIED_DIGEST'
go test -tags=sandbox_acceptance -count=1 -timeout=5m -run '^TestSessionContainerdAcceptance$' -v ./internal/agent/sandboxexec
```

The explicit test fails rather than skips when opt-in or prerequisites are absent. It exercises real session creation, shell environment/output streaming, file visibility through the artifact workspace, sequential steps, nonzero exits, timeout poisoning, canceled-context cleanup, repeated Close, and container/snapshot/workspace removal. It does not run Kafka, gRPC, a production Agent, host source cloning or object-storage transfer. Images remain cached. Cleanup failures report the namespace and workspace for operator inspection and preserve the acceptance root.

## Adjacent audit: native plugins and concurrency

Native plugins are registered Go objects executed in-process. `Plugin.Execute` has no context argument, and `PluginExecutor.executePlugin` drops the incoming context; timeout options alone cannot force a plugin to stop. The Agent has no native plugin manager. Pipeline and queue validation now reject unknown native plugin names before workspace preparation, including names that carry `run` or `url` arguments. Baremetal HTTP execution is accepted only through explicit `uses: http` and dispatched directly to the existing HTTP executor; it retains trusted host network access. It must never serve as a sandbox fallback. A small next step is to define an opt-in context-aware plugin interface with cancellation tests for trusted builtins; loading arbitrary native plugins and isolating them requires a separate execution/process boundary and compatibility policy.

There are three different concurrency concepts:

- The production control process limits simultaneous pipeline runs with a local semaphore.
- The Kafka Agent queue currently executes and acknowledges one message before reading the next, intentionally protecting delivery/commit order. `agent.maxConcurrentJobs` now accepts only 1 or 0 (the default of 1); negative values and values above 1 fail startup/registration, and registration advertises the actual capacity of 1. Adding unbounded goroutines around the handler would make later offsets acknowledge unfinished earlier messages.
- Nonempty `job.concurrency` is now rejected by production pipeline validation. The old legacy `Runner` implementation is not a production cross-run or cross-control-instance lock.

The supported-capacity contract and native-plugin/named-concurrency rejections have regression coverage. Real parallel admission needs partition-aware contiguous-offset acknowledgment and cancellation/shutdown tests. Named concurrency requires an agreed key scope (for example project/environment), fairness and cancellation policy, durable lease ownership, expiry/renewal and restart reconciliation; a process-local mutex would not deliver the documented cross-run guarantee.
