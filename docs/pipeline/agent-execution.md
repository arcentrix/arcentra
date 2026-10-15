# Agent execution contract

## Supported runtime and trust boundary

The trusted baremetal profile executes shell jobs on a dedicated host (or inside the already-isolated Agent container). Configure `agent.mode = "baremetal"`, `agent.sandbox.enable = false`, and leave `deniedCommands` and `resourceLimits` empty. `conf.d/agent.baremetal.example.toml` is the explicit development example.

Requested Docker/Kubernetes Agent execution and restrictions the worker cannot
enforce remain rejected. Selecting an image alone does not enable isolation.
Never point an unrestricted baremetal Agent at untrusted pull-request code, and
do not share its filesystem or service credentials with unrelated tenants.
Baremetal shell steps inherit the Agent's process environment, then apply Agent,
job and step environment overrides.

The separate, explicitly enabled `containerd-offline-v1` profile supports bounded
trusted-code isolation, an allowlisted digest-pinned image and explicit CPU/memory
limits. Both Control and Agent operator policies default off. Its no-egress,
linear-chain, staging/sealing and private-workspace rules are documented in
[sandbox integration](../sandbox-agent-integration.md) and [operator opt-in](../offline-runtime-acceptance.md).
The baremetal step types, environment inheritance and caller-selected workspaces
below do not expand that offline profile. Whole-job container retry uses a fresh
generation; live failed-step retry retains one session/workspace and partial
files. See [retry contracts](retries.md) and [V18 exact-candidate acceptance](failed-step-retry.md).
Backend PR 61 and frontend PR 11 were draft when this evidence was recorded on 2026-10-15; acceptance does not enable a
production deployment or either operator policy.

Install Go at the version required by `go.mod` for development. The runtime needs a POSIX `sh` and `git`; scripts can require additional project-specific tools. Steps run through `sh -ec` so failed commands stop the script. Unix process-group cancellation terminates descendant processes, and captured stdout/stderr are each bounded to 4 MiB while logs stream separately.

## Supported capacity and step types

The Kafka worker executes one job at a time to preserve message acknowledgment order. Set `agent.maxConcurrentJobs = 1`; an omitted/zero value means effective capacity 1. Negative values and values above 1 are rejected at startup and registration, and the Agent advertises only capacity 1. `job.concurrency` is rejected by production pipeline validation because named cross-job/cross-run locking is not implemented.

Baremetal Agent jobs accept explicit `shell` or `builtin:shell` steps (actions: omitted, `run`, `script`, `command`), `builtin:artifacts` (`upload` or `download` in a whole-job task), and `http` (action omitted or `request`). Shell steps require a nonempty `run`, `script`, `command` or `commands` argument. HTTP steps require an absolute HTTP(S) `url` without embedded credentials and run through the existing HTTP executor on the trusted Agent host. An HTTP step's `run` argument is never interpreted as a shell command.

Unknown native plugin names and version-qualified plugin names are rejected before host workspace/source/artifact preparation, even when their arguments contain a shell command or URL. Pipeline validation checks every step in an Agent-dispatched job, including steps without their own `runOnAgent` flag. Local control-plane plugin validation is unchanged. Old queue tasks with an empty `uses` and a shell command remain compatible; new pipeline definitions require an explicit `uses`. Unsupported queued work is claimed and reported as failed, rather than silently discarded or retried as a native plugin.

## Startup and delivery

1. Connect and register over gRPC. Dynamic registration updates the active client bearer token.
2. Initialize the registered agent's WAL outbox and Kafka build-log publisher.
3. Subscribe to the agent-specific work topics and a separate agent-specific cancellation topic. The cancellation consumer remains available while a shell job occupies the work consumer.
4. Claim a queued job and each step through authenticated status RPCs before execution. A process-local UUID is attached to every status report; if a committed claim response is lost, only the same attempt may confirm that claim and continue. Rejected, stale, and completed claims never execute commands.
5. Execute sequential steps in the same run-specific agent workspace. Job and step deadlines, environment overrides, `continueOnError`, and boolean `when` expressions are honored. Conditions can access `env`, `variables`, `job.name`, and `step.name`.
6. Report terminal step and job states. Steps following a failed step are skipped; unstarted steps following cancellation are cancelled.

Explicit `workspace` values are honored for compatibility. An omitted job workspace resolves to `<agent.workspaceDir>/<pipelineRunID>/<jobRunID>`. Standalone steps also receive a run-specific workspace. Working directories are retained for debugging; there is no automatic retention enforcement yet.

Git sources support branch selection, commit pinning, and HTTPS username/password or token authentication. Git prompts are disabled. Credentials are supplied through a temporary askpass helper and environment, rather than added to Git command arguments or repository config. Credential-bearing repository URLs are rejected; supply passwords and tokens in `source.auth`. Unknown source types and missing requested artifact storage fail the job; artifact download errors are not silently ignored.

## Agent artifacts

Whole-job agent tasks execute `uses: builtin:artifacts` in normal step order:

- `action: upload`, `args: {paths: ["dist/*", ".version"]}` expands relative file/directory globs, recurses into matched directories, and uploads each regular file
- `action: download`, `args: {job: "build", path: "dist"}` restores matching upstream files to their original workspace-relative paths; `paths` accepts multiple patterns, and omitting both downloads all files for that job

Object keys are scoped as `artifacts/<pipelineRunID>/<jobRunID>/<relativePath>` relative to the configured storage `BasePath`. The terminal job RPC reports relative paths mapped to these opaque keys. The control plane prefixes each path with the producing job name before passing it downstream. These values are storage keys, not public download URLs. Upstream files are also prefetched under `.artifacts/<jobName>/<relativePath>` without flattening filenames.

S3, MinIO, OSS, GCS, and COS providers expose an additive streaming interface. Transfers pass cancellation/deadline contexts, avoid loading entire artifacts into agent memory, reject traversal and symlinks, and use workspace-confined file operations. Downloads use temporary files plus atomic rename so failures do not truncate an existing file. Downloaded files have normal `0644` permissions; executability must be restored explicitly when needed. Empty matches, missing upstream jobs, unsupported storage capabilities, and transfer errors fail the step. Named bundles, expiry policies, public access, upload-policy arguments and untracked-file filtering are explicitly rejected by the agent adapter; permissions and retention are not inferred from those options.

## Reporting and recovery limits

CloudEvents are appended in execution order, with pipeline-run, job-run, step-run, and agent identifiers. Shell stdout/stderr also stream to `BUILD_LOGS` with the actual step-run ID, independent of whether an event publisher is enabled. Reporting uses authenticated RPC contexts even when the execution context has been cancelled.

Current terminal delivery uses the durable, claim-bound outbox described in
[terminal reports](../agent-terminal-reports.md). Step receipts, including
skipped/cancelled and preparation/condition outcomes, precede job completion.
Unacknowledged results replay their original bytes and claim without rerunning
shell commands or artifact transfers. Reporting delay does not turn a successful
command into a command failure; persistence failure fences later commands.
Completed or otherwise stale control-plane claims never grant new execution.

This is not an exactly-once execution guarantee. A crash before a result is durably saved can leave an unknown execution result requiring reconciliation. Saved receipts require the same surviving disk and original identity; they do not grant an automatic cross-restart execution lease or takeover. Explicit offline reconciliation is available. A restarted process cannot adopt a running claim from the old process. Build-log delivery now uses a separate bounded durable spool with fsync-before-acceptance and stable replay IDs; broker outages retain queued logs until acknowledged. Full or failed disks explicitly fail execution. See `implementation.md` for configuration, retention and surviving-disk assumptions. Generic native plugins and runtime profiles beyond the bounded offline containerd profile remain unsupported. Current durable terminal-report recovery and artifact lifecycle contracts are documented in [terminal reports](../agent-terminal-reports.md) and [artifact lifecycle](../artifact-lifecycle.md); neither permits execution takeover.

## Regression tests

Run focused verification with:

```sh
go test -race -count=1 ./internal/agent/... ./internal/shared/executor ./internal/shared/grpc ./internal/shared/storage
```

The suite covers:

- Actual shell execution through authenticated loopback gRPC into SQLite-backed control-plane repositories
- Success, nonzero exits, continued failures, conditional skipping, timeouts, cancellation, and rejected replay
- Live logs with execution identity, inherited environment, bounded output, and descendant-process cancellation
- Git commit pinning, safe workspace creation, failed storage setup, and fail-closed runtime settings
- Terminal-status retry without repeated shell side effects or artifact transfers, including skipped steps and temporary successful-step report failures
- Lost job/step claim responses after a real database commit, same-attempt retry, different-attempt rejection, and completed-run replay prevention
- Run/job-isolated artifact uploads, nested/glob downloads, streaming SDK transports for all five providers, symlink/traversal rejection, cancellation, and atomic replacement
- Two agents consuming isolated Kafka queues and cancellation delivered while a job is running, using librdkafka's local wire-protocol mock broker

The Kafka test is skipped only with `go test -short`. It fails on broker setup errors or timeouts. These tests do not replace a deployed Kafka/MySQL/container smoke test.
