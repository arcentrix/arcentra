# Pipeline / Agent integration

This document describes the implemented execution path, its verification, and its remaining limits. Older inventory/decision documents describe earlier repository layouts; the active control-plane path is `Process → Coordinator → DAG Executor → TaskFramework`.

## Implemented workflow

- Create a project and pipeline, validate/save a definition, trigger a run, inspect jobs/steps/logs, cancel, and trigger a fresh rerun from the web application.
- Project/pipeline collection APIs filter by the authenticated user’s project, organization, and pipeline grants before pagination. Pipeline creation resolves its project from the request body; effective-action lookups include inherited organization grants.
- Definitions and API inputs use the protobuf JSON contract. Run inspection routes are scoped to both pipeline and run. Cross-pipeline run identifiers are rejected.
- DAG jobs are scheduled once, respecting dependencies. Local jobs and steps have persisted execution records; queued/running cancellation and timeouts write terminal state with independent cleanup contexts.
- Remote jobs are assigned to an enabled, available agent matching selectors. Work and cancellation use separate agent-specific Kafka topics and consumer groups.
- Agents register before consuming work, update authentication credentials after registration, execute real commands, report authentic execution outcomes, and use actual run identifiers for logs.
- Agent status reports authenticate the agent, validate ownership, and atomically reject stale claims/terminal-state reversals. Queue handler failures are retried without advancing offsets.
- Logs are persisted through a repository, de-duplicated on Kafka replay, and retained when writes fail. HTTP and WebSocket readers share the same aggregator.
- Local artifacts/reports are isolated by run and job, preserve nested paths, support globs, and reject traversal/symlinks. File replacement is atomic and cancellation-aware.
- Project SCM credentials are restricted to the project’s configured repository; credential-bearing overrides to other origins or repositories are rejected.
- Requested pipeline runtime images, resource constraints, or isolation configuration are rejected rather than ignored; runtime environment variables are applied explicitly.
- Project responses omit SCM credentials and webhook secrets. Missing authorization infrastructure fails closed.

## Build from a clean checkout

Requirements: Go 1.25.6 or compatible, a C compiler (SQLite/librdkafka use CGO), Git, and a POSIX shell. Code generation requires access to the Buf registry. Put `$(go env GOPATH)/bin` on PATH.

```sh
make codegen
make build TARGET=arcentra
make build TARGET=arcentra-agent
make build-target TARGET=cli
```

`make build` generates protobuf before Wire. The source Dockerfile now performs generation and builds both processes plus the CLI. Both runtime images include Git and a shell. Build toolchains required by your jobs (Node, Go, Java, etc.) must be added to your own agent image or installed on its dedicated host.

Container commands, for a machine with Docker:

```sh
docker build --target arcentra -t arcentra:local .
docker build --target arcentra-agent -t arcentra-agent:local .
```

These image builds were not run in the implementation environment because Docker/containerd were unavailable.

## Explicit schema initialization

Prepare a private configuration with the correct database DSN. Back up an existing database before applying a schema update.

```sh
./cli migrate --conf /private/path/config.toml
```

This is opt-in. Normal server startup does not migrate the database. The command creates/updates model tables and idempotently seeds built-in permissions, roles, bindings, and menus. It does not create users, grant a user administrator access, or overwrite existing role/menu customizations. For a new installation, register an account, then an operator with database access can explicitly select it with `./cli bootstrap-admin --conf /private/path/config.toml --username <registered-user>`. This command refuses to run once an enabled platform-super-admin grant exists. No administrator is granted automatically. After this explicit bootstrap, restart every control-plane instance to reload its authorization policy, then sign in again. Existing installations use their existing authorized role-management workflow. Schema tests use a disposable SQLite database; production MySQL migration compatibility still requires staging validation.

## Agent trust boundary

The implemented worker supports **explicit trusted-code baremetal execution**. A job's shell commands run with the Agent process's OS permissions and can access that host's resources. A container image alone is not a per-job isolation guarantee. Use dedicated disposable hosts/containers, minimal credentials, and trusted pipeline definitions. Do not run untrusted pull-request code on a privileged host or mount its Docker/containerd socket.

`conf.d/agent.baremetal.example.toml` is an explicit example. Copy it to a private file and configure the control plane, Kafka, and registration token. Never commit live tokens or passwords.

The older `conf.d/agent.toml` requests sandbox mode. That mode now fails closed instead of silently executing commands on the host. Requested sandbox, container/Docker, command-denial, or resource-limit enforcement is rejected when unsupported. Containerd/Kubernetes sandbox integration is a remaining feature, not a verified capability.

The tested worker processes one job at a time per agent. Do not interpret `maxConcurrentJobs` as verified multi-job scheduling capacity. Use multiple agents for parallel jobs.

## Verification commands

```sh
go test -race -count=1 ./...
go vet ./...
```

Important integration tests run real shell commands and real SQLite persistence. Agent integration uses authenticated loopback gRPC. Queue tests use librdkafka's mock Kafka cluster; this exercises Kafka wire/client routing and cancellation but is not validation of an external production broker, TLS/SASL, rebalance, or cluster failure behavior.

Frontend (sibling `arcentra-web`):

```sh
pnpm install --frozen-lockfile
pnpm test:run
pnpm build
pnpm lint
pnpm test:ui
```

The Playwright smoke script uses explicit local API fixtures. Chromium could not start in the implementation sandbox because Unix socket creation was denied, so no successful browser/screenshot QA is claimed.

## Remaining limits

- No production or staging deployment was performed. MySQL, external Kafka, real object storage, and container images need environment-specific integration validation.
- Sandbox execution/resource isolation is unsupported and rejected. Trusted baremetal is the supported path in this change.
- Agent-job automatic retry policies are rejected instead of ignored. Manual reruns create a fresh pipeline run. Local step retry follows the existing policy.
- Agent jobs with an implicit deployment target are rejected; use an explicit deployment step on the agent. Local target plugins use the existing `deploy` contract and fail when unavailable.
- Automatic takeover/resumption after agent/control-plane process loss still needs execution-attempt leases and fencing. Explicit offline reconciliation is available below; completed-replay protection is present, but exactly-once execution across crashes is not promised.
- Gateway CloudEvents now persist transactionally before acknowledgment, and WAL restart preserves committed sequence numbers. Legacy Gateway PushLogs returns an explicit unsupported error; live build logs use Kafka.
- Agent terminal-report retry receipts are in memory. Process loss before durable acknowledgment can leave a running record that needs operator reconciliation.
- Local artifacts are filesystem artifacts, not remote download URLs. Remote agent artifact steps support streaming transfers and downstream opaque-key references. Provider SDK behavior is tested with local fake transports; actual object-storage credentials/providers still require configured integration tests.
- Delay-queue scheduling outside the immediate pipeline path has not been comprehensively validated under broker/process failure.
- The frontend work covers the requested project/pipeline/build/agent integration. Other product areas that were already prototypes are not certified complete by this change.

## Interrupted execution recovery (explicit, offline only)

The safe baseline is to fail interrupted work explicitly and start a new manual run. It is not transparent resumption or an exactly-once guarantee. Persisted claim IDs make a lost RUNNING RPC response retryable within the same Agent process; they do not authorize another process to take over a running attempt.

Before applying recovery:

1. Stop **all** control-plane and Agent instances for this database.
2. Verify no shell/build/deployment work is still active, including external systems started by a job.
3. Back up the database and review the selected run IDs plus any external side effects.
4. Preview, then explicitly apply:

```sh
./cli recover-interrupted --conf /private/path/config.toml --run-id <run-id>
./cli recover-interrupted --conf /private/path/config.toml --run-id <run-id> --apply --executors-stopped
```

Use `--all-active` instead of `--run-id` only when deliberately recovering every active run. The default is dry-run. Applying requires the operator's explicit `--executors-stopped` assertion, preserves already-terminal records, and marks pending/running/paused runs plus active jobs/steps failed. It does not execute jobs, roll back deployments, or publish cancellation claims that external work actually stopped.

Restart services after reconciliation, inspect results, and trigger a fresh manual run if appropriate. Automatic orphan discovery across controllers requires controller/Agent leases and fencing that the current in-memory execution design does not provide; automatic startup mutation is intentionally absent.

## Durable build logs

Both the Agent and control plane accept already-redacted build output into an exclusive local spool before returning success to the executor. A successful append includes file and directory fsync. Kafka delivery uses `acks=all`; records remain until delivery confirmation. A stable event ID allows the database and active subscribers to deduplicate a replay after a lost ACK. Without Kafka, the control plane replays into its shared database-backed log aggregator instead.

Configure `[logSpool]` with `directory` and `maxDiskUsageMB` (default 512 MiB). The directory must be private, process-exclusive and on persistent local storage; each Agent/controller needs a separate directory. A process reopens and replays its own backlog at startup, without waiting for a new pipeline run. Files are 0600 and directories 0700; symbolic links, hard-linked records, foreign ownership, corrupt records and unsupported locking fail closed. Retain the same directory across restarts; never put it inside a disposable per-job workspace. The Agent default is `.build-log-spool` directly under its configured workspace root, outside individual Run/Job directories.

The queue additionally caps pending records at 10,000. It never evicts unacknowledged logs to make room. A full queue or filesystem failure rejects new output, stops the command, and is reported as an explicit failed execution rather than successful work with missing logs. Already accepted records are retained. An explicitly configured `continueOnError` still permits subsequent steps and can result in overall job success; the affected step remains failed with its log-persistence diagnostic. Temporary delivery failures produce safe generic operational warnings, with recovery notifications; raw payloads/transport errors are not logged by the spool. Known project secrets are masked before persistence. This is not encryption, and arbitrary secret strings unknown to the execution context cannot be automatically recognized.

Normal shutdown stops work before closing the spool. Confirmed records are deleted; unconfirmed records are replayed after restart. Terminal run state can become visible before queued output reaches the control plane. The frontend polls briefly after completion and keeps manual Refresh available for delayed output.

Kafka remains a trusted infrastructure boundary: restrict broker access and topic ACLs to authorized control/Agent services, use TLS/SASL in deployment, and do not expose the sample PLAINTEXT development broker publicly. A stable log ID provides retry deduplication, not cryptographic sender authentication.

Compose examples now bind separate control/Agent data directories. Kubernetes examples deliberately do not choose a storage class/PVC; they require an operator-provided per-instance persistent volume at `/var/lib/arcentra` before relying on recovery across Pod replacement. Container images/manifests were not deployed or runtime-tested here.

Tests exercise subprocess exit/reopen and injected fsync failures on temporary local storage. They do not verify power-loss persistence or the permanence of this assistant cloud filesystem. These guarantees require working fsync semantics and the same surviving disk. They do not protect against disk destruction, manually deleting the spool, misconfigured Kafka retention/replication, or sharing a spool between hosts. There is no transparent execution resumption: only log delivery is replayed. Spool corruption or a changed limit smaller than the retained backlog prevents startup and requires an operator to preserve and investigate the files, not silently delete them.
