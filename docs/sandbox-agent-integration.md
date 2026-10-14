# Agent sandbox integration boundary

## Current status: wired, hard-disabled, not runtime-accepted

The current branch contains `containerd-offline-v1` plumbing across pipeline validation, immutable dispatch payloads and persisted job bindings, Agent capability/readiness reporting, runtime-aware selection, the queue worker, whole-job sessions and durable runtime recovery. This is under-development implementation, not a released or validated isolation capability.

Production remains fail-closed. The default policy is disabled and `pkg/taskqueue.OfflineRuntimeAccepted` is `false`; production configuration validation rejects opt-in while that hard gate is closed. Setting a policy flag, editing an Agent label, or requesting sandbox mode cannot enable execution. A runtime job must never fall back to host shell, HTTP or native plugin execution. The existing trusted baremetal path remains available with its existing retry semantics.

V10's successful [backend main CI](https://github.com/arcentrix/arcentra/actions/runs/38393872734) at `6c24bd389d76fe749ad07a8e23fc049d6cd46c93` establishes the earlier trusted-baremetal real-service/browser baseline. It does not validate the new sandbox branch. The local final-code uncached full-repository race suite passed for this implementation stage. No successful real containerd run is claimed here. Fake-backend tests and compile-only acceptance checks cannot prove namespace, cgroup, process termination or filesystem isolation behavior.

The implementation environment has no usable containerd/runc/socket/cgroup-v2 setup. The separately authorized [disposable GitHub runtime workflow](../scripts/acceptance/CONTAINERD_RUNTIME.md) provisions only an isolated temporary daemon on a dedicated hosted VM. It does not reconfigure the system daemon or host security settings. The production acceptance gate remains closed until exact-revision runtime checks and independent review succeed; merely adding the workflow is not a passing result. Keep backend, adapter and end-to-end worker acceptance distinct.

## Bounded offline job profile

The request and validation plumbing restricts the initial profile to:

- Trusted pipeline code and exactly one job, with every step routed to the selected Agent. The serial worker supports one job at a time; this is not a multi-tenant or hostile-code security guarantee.
- Explicit runtime type `containerd-offline-v1`, an exact operator-allowlisted `image@sha256:...` reference, and mandatory CPU/memory limits within operator maxima. Image and resource values are frozen at dispatch and bound to the pipeline/run/job/Agent identity by a runtime digest. Queue payloads and reports must match that binding.
- An explicit numeric non-root `UID:GID` matching the Agent's effective UID and GID. There is no automatic ownership change to make an image or workspace work. The image must contain the required shell/init tools and all toolchains/dependencies needed offline.
- A private network namespace with `network: none`. No container egress, host networking, bridge/CNI configuration or DNS exception is supported. The container sees its allocated job workspace at `/workspace`, not the Agent's configuration, runtime socket, journal root, cache root or sibling jobs.
- Only a prefix of explicit `builtin:artifacts` downloads, one or more `shell`/`builtin:shell` steps, and a suffix of explicit artifact uploads. Shell action must be empty or `run`. Source checkout is an explicit trusted host preparation operation using a remote Git URL.
- No automatic retry policy, alternate attempt IDs, HTTP steps, arbitrary native plugins, Kubernetes/deployment steps or targets, standalone StepRun runtime tasks, caller-chosen host workspace, local source transport, custom mounts or extra isolation flags. An interrupted or failed job needs an explicitly requested fresh run; baremetal retry policies are unchanged.

Operator policy belongs to Control and Agent configuration, not the pipeline or queue payload. Runtime capabilities are separate from labels and are checked with Agent availability/readiness. A reported capability is not acceptance evidence or HA admission/fencing. Production gate checks still apply before this profile can be enabled.

## Staging, execution, sealing and output

The whole-job worker plumbing has this ordered lifecycle:

1. Validate the complete job and immutable runtime binding before host staging or command execution. Claim the original job, allocate one session-owned workspace, and durably record the exact runtime ownership identity.
2. Stage source checkout and artifact downloads using trusted host Agent code, credentials and network access into that workspace. No container process runs during this prefix. The payload cannot select a host bind-mount source.
3. Start one container session after inputs are ready. Every shell step uses the same session through `Manager.ExecuteWithExecutor`, retaining the existing declared-secret redaction, durable logs and events. Commands use `sh -ec` at `/workspace`; environment comes from the explicit request and approved image, not the Agent process environment. There is no host-executor fallback.
4. Call `Seal` before reading output for any host artifact upload. Sealing cancels active exec work and must establish that all container processes, including background descendants, have stopped. A seal error blocks publication; it cannot be treated as a normal shell exit or bypassed by `continueOnError`. The sealed session cannot run another command or accept more downloads.
5. Upload the allowed artifact suffix through the trusted host transfer path. Artifact paths remain workspace-scoped and retain traversal, symlink and opened-file identity checks; special files are not valid artifacts. Commands never receive storage credentials by mounting the Agent's wider filesystem.
6. Close the session, remove only the recorded container/task/snapshot and job workspace, and include cleanup errors in the job outcome. Cleanup uses independent bounded contexts after cancellation. Runtime records remain until cleanup and terminal delivery/reconciliation satisfy their acknowledgment requirements.

Image resolution or a permitted image pull occurs through the trusted host containerd daemon. “Offline” describes container command networking, not host Git/object-storage/image operations. This design assumes that trusted host preparation is acceptable for the deployment's tenant model. Networked builds, package installation during commands and releases to external systems require a different explicitly designed profile.

Shell steps are serialized within the session. A normal nonzero shell exit can follow the job's explicit `continueOnError` behavior; runtime errors, canceled/timed-out execution, log persistence failure or a replaced workspace must not cause host fallback. Host artifact reads happen only after successful sealing. A slow/blocked filesystem or runtime can still delay cleanup; no successful removal should be reported until actual runtime/filesystem checks confirm it.

## Private persistent roots and restart recovery

The durable runtime controller uses separate, pre-existing journal and workspace roots. Both must be clean absolute paths, owned by the Agent user with mode `0700`, under trusted ancestor paths without symlink components. The roots cannot overlap and must survive process restart. An operator creates and provisions them before startup. The runtime controller does not create missing roots, `chmod` them, `chown` them or silently adopt another user's state. Private job subdirectories and journal records are created within those roots.

The journal is process-exclusive and bound to the stable Agent identity, authenticated controller trust binding, containerd namespace and workspace root. Records retain the exact job/run/original claim/runtime digest, lifecycle phase, resource ownership and workspace filesystem identity. Writes use file/directory synchronization; bounded records, canonical checksums, private modes, link rejection and an exclusive lock fail closed on corruption, identity conflicts or uncertain persistence. A checksum is not a signature against a host administrator or malicious process with the same UID. Trusted code and trusted host operators remain part of the boundary.

Recovery is deliberately narrower than execution resumption:

- Before becoming ready, the controller inspects recorded ownership and removes only the exact recorded resources. It does not sweep a namespace, infer ownership from a job name, delete unrelated directories or recreate a missing container to finish work. Unknown/corrupt state, a conflicting owner or incomplete cleanup preserves evidence and blocks readiness.
- The existing durable terminal outbox handles saved report delivery independently; replay uses the original result and claim and never repeats a command. Normal successful terminal delivery is already authenticated and checked against the immutable job/runtime binding. Runtime journal acknowledgment also requires exact local cleanup evidence.
- On restart, after cleanup, `InspectJobExecution` makes a read-only authenticated query for that Agent's exact original job/claim/runtime digest. Only authoritative terminal state permits acknowledgment of the recovered runtime record. The query neither changes job state nor authorizes a rerun. Unavailable, mismatched or still-active state blocks readiness and requires operator review and, where appropriate, the existing explicit offline interrupted-run reconciliation.
- A surviving runtime journal does not reconstruct a lost terminal result, claim successful execution, roll back external effects or grant another Agent execution authority. Durable identity and the same surviving disk are prerequisites; automatic re-enrollment cannot replace the original owner.

Failed removal retains the affected workspace and journal evidence. Retrying cleanup must preserve the same exact resource identity. Never delete the shared root, rewrite the journal, change an Agent identity, or broaden daemon permissions to make readiness pass.

## Standalone adapter compatibility

`NewSession` remains the smaller adapter entry point used by its standalone acceptance test. It starts the runtime during construction and can use the Agent filesystem owner's numeric UID/GID by default; that does not relax the integrated offline profile's explicit matching non-root user requirement. The durable worker uses `Controller.Allocate` followed by host staging, `Start`, `Seal` and `Close` instead.

A constructor failure can return a non-nil session when cleanup still needs retrying; keep that handle. Repeated `Close` retries failed cleanup and successful `Close` is idempotent. An uncertain-create backend must retain the exact resource identity. A session does not close a shared backend connection. Standalone session success alone does not prove durable controller recovery or the production queue path.

## Runtime-independent verification commands

Run against the final code revision and retain the actual results separately:

```sh
go test -race -count=1 ./pkg/taskqueue ./pkg/sandbox ./internal/agent/sandboxexec ./internal/agent/taskqueue ./internal/shared/pipeline/validation ./internal/control/process ./internal/control/repo ./internal/control/service
go test -tags=sandbox_acceptance -run '^$' ./internal/agent/sandboxexec ./pkg/sandbox
```

The second command only compiles acceptance tests. Neither command establishes real container behavior. Retain the current revision’s full repository quality results after code generation; previously passed V10 tests are not evidence for changed source.

Required regression evidence includes runtime schema/claim/digest rejection, readiness-aware selection, no fallback or standalone-task bypass, staging order, seal-before-read, cancellation/shutdown, log/terminal persistence failure, exact-owner restart cleanup and unavailable/mismatched/nonterminal authority. Keep passing checks, failures, blocked checks and never-run checks separate.

## Optional capable-host adapter test

After explicit authorization, follow the prerequisites in [backend acceptance](sandbox-containerd.md): a dedicated disposable Linux host, a working rootful containerd/runc setup, usable cgroup-v2 CPU/memory controllers and an approved Alpine-compatible digest image. Run the backend test first and retain its complete output. These commands do not grant permission to install components or change host security settings.

```sh
export ARCENTRA_SANDBOX_SESSION_ACCEPTANCE=1
export ARCENTRA_CONTAINERD_SOCKET=/run/containerd/containerd.sock
export ARCENTRA_SANDBOX_TEST_IMAGE='your-approved-alpine-image@sha256:YOUR_VERIFIED_DIGEST'
go test -tags=sandbox_acceptance -count=1 -timeout=5m -run '^TestSessionContainerdAcceptance$' -v ./internal/agent/sandboxexec
```

The explicit test fails rather than skips when opt-in or prerequisites are absent. It exercises real standalone session creation, shell environment/output streaming, workspace visibility, sequential steps, nonzero exits, timeout poisoning, canceled-context cleanup, repeated `Close`, and container/snapshot/workspace removal. It does not run Kafka, gRPC, a production Agent, host source cloning, object-storage transfer or durable-controller restart recovery. Images remain cached; cleanup failures retain evidence for operator inspection.

Record the exact code revision, host/runtime versions, approved image digest, command and complete output. Before considering activation, additionally prove the integrated staging/session/sealing/publication/recovery path on the same bounded profile. Passing a standalone backend or adapter test must not silently open the production gate. Release-image startup, production infrastructure and hostile multi-tenant isolation remain separate validation work.

## Adjacent limits: native plugins and concurrency

Native plugins are registered Go objects executed in-process. The existing `Plugin.Execute` interface has no context argument and cannot enforce process isolation; the Agent has no native plugin manager. Pipeline and queue validation reject unknown native plugin names. Baremetal HTTP steps retain trusted host network access only through explicit `uses: http`; offline-containerd jobs reject them.

The controller's local pipeline-run semaphore, the Agent's single-message-at-a-time Kafka worker and named cross-run concurrency are different mechanisms. `agent.maxConcurrentJobs` accepts only 1 or 0 (default 1); nonempty `job.concurrency` is rejected. Runtime readiness/selection does not add cross-instance leases. Real parallel admission requires partition-aware contiguous-offset acknowledgment and shutdown/cancellation tests; named concurrency needs defined scope and durable ownership/expiry/recovery.

## Durable-controller acceptance harness

`TestDurableControllerContainerdAcceptance` is a separate `sandbox_acceptance` test with its own `ARCENTRA_DURABLE_SESSION_ACCEPTANCE=1` opt-in. Its prerequisites and retained-evidence behavior are described in [the session package README](../internal/agent/sandboxexec/README.md). It targets the new persistent Controller, source/artifact staging boundary, background-process sealing, exact cleanup and reopening tombstones. Merely compiling it with `-run '^$'` does not execute a daemon or validate isolation. The separate authorized containerd workflow enables this test and the active-process restart test on its disposable VM. The ordinary quality and real-service/browser workflow does not imply container isolation validation.

The Agent advertises an explicit execution mode separately from readiness. A containerd-mode Agent is excluded from baremetal dispatch even while it is not ready; a legacy Agent with no mode report remains eligible only for the old baremetal path. Unknown modes fail selection. If an already-bound structurally valid task reaches the same Agent after its image/resource policy is tightened or its configured execution mode changes, the Agent claims that exact task and reports a failed, unexecuted result. It does not allocate a session, clone source or transfer artifacts. Unresolved runtime recovery instead blocks consumption without claiming or acknowledging new work.

The explicit one-job restriction is a first-profile validator constraint, not a consequence of serial Agent concurrency. Current user-configurable container runs can clone source, execute shell steps and publish artifacts, but cannot create a same-run upstream job for artifact-download inputs. A synthetic upstream download fixture would not establish supported sequential container DAG execution. That capability and fresh-session retry semantics remain follow-up implementation work.
