# Pipeline / Agent integration

This document describes the implemented execution path, its verification, and its remaining limits. Older inventory/decision documents describe earlier repository layouts; the active control-plane path is `Process → Coordinator → DAG Executor → TaskFramework`.

## Verified baseline and current work

V10 passed the designated hosted CI jobs on both merged main commits, verified on 2026-10-13:

- Backend `6c24bd389d76fe749ad07a8e23fc049d6cd46c93`: [run 38393872734](https://github.com/arcentrix/arcentra/actions/runs/38393872734), including Go quality, two fresh-schema real MySQL/Kafka/S3/Agent runs, Redis registration/login, and the actual-backend Chromium journey.
- Frontend `e7906be0e9909de26dea2467744420c197d27237`: [run 38393804004](https://github.com/arcentrix/arcentra-web/actions/runs/38393804004), including frontend quality and Chromium with simulated API transport. The backend job uses its separately pinned frontend acceptance client; these two browser checks are not interchangeable.

V10 covers the trusted baremetal baseline. Offline-containerd implementation-level acceptance separately passed [run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022), PR head `e7132be275c144a2eadc03b61e3c4f16dd302c4e`, tested merge `12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`. Independent gate-enabled [run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313) passed the ordinary `NewProcess`/`StartWorker` entries at PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c` (tested merge `1d2e29030741963897b7056c7d15249f2637d757`); operator policies default off. Neither result establishes release OCI image startup, production deployment, live Kubernetes release or external model calls. [Exact runtime evidence and opt-in](../offline-runtime-acceptance.md). See [real-stack acceptance](../real-stack-acceptance.md) and [the sandbox integration boundary](../sandbox-agent-integration.md).

V18 live failed-step retry passed independent gate-on acceptance at backend head
`3f7d81283afcabaf62af0e705a8d0b1252035024`, tree
`af529d00f50999cf4f5b9c1f9be9cbca7b6a6841`.
[Runtime run 38684719818](https://github.com/arcentrix/arcentra/actions/runs/38684719818)
used ordinary `NewProcess`/`StartWorker` and actual worker readiness; all five live
scenarios, seven restart phases and cleanup passed.
[Ordinary CI 38684719835](https://github.com/arcentrix/arcentra/actions/runs/38684719835)
passed real MySQL contention and the existing actual-backend browser journey at
frontend `c0b0fd280f80ed5bc8dcea6d638352d73bbeb289`. Failed-step editor interaction
has separate simulated-transport Chromium evidence; the browser journey did not
select and execute failed-step retry. [Backend PR 61](https://github.com/arcentrix/arcentra/pull/61)
and [frontend PR 11](https://github.com/arcentrix/arcentra-web/pull/11) were draft when this evidence was recorded on 2026-10-15. Both `allowFailedStepRetry` policies remain default off.
[Exact evidence, migration 008 and operator prerequisites](failed-step-retry.md).
V17 exact-content `whole_job` approval is supported separately. The new
[V19 failed-step approval contract](failed-step-retry-approval.md) has closed-gate
implementation evidence and passed exact gate-enabled ordinary-entry CI at
`eba238c0e5fdfcfced1d12961c03aa70cc347d3b` (see that contract's evidence). Its independent approval permissions and parent failed-step permissions
remain default false on both Control and Agent. The V18 evidence above is unchanged.

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
- The default production path rejects unsupported runtime images, resources and isolation configuration rather than ignoring them; runtime environment variables are applied explicitly. The `containerd-offline-v1` implementation has hosted runtime evidence; the independent gate-on ordinary production entries also passed in hosted test binaries, and operator policies default off.
- Project responses omit SCM credentials and webhook secrets. Missing authorization infrastructure fails closed.

## Build from a clean checkout

Requirements: Go 1.25.6 or compatible, a C compiler (SQLite/librdkafka use CGO), Git, and a POSIX shell. Buf, Wire and the official Go protobuf plugins are version-pinned in the Makefile. Generation uses local plugins from `api/buf.gen.yaml`; it does not send schemas to a remote Buf generation service. Installing the pinned tools and Go modules requires the corresponding dependency downloads unless already cached. Put `$(go env GOPATH)/bin` on PATH.

```sh
make codegen
make build TARGET=arcentra
make build TARGET=arcentra-agent
make build-target TARGET=cli
```

Generated `api/**/*.pb.go` files are committed so a clean checkout can resolve dependencies before generation; `make buf-check` checks them against the pinned generators. `make build` generates protobuf before Wire. The source Dockerfile now performs generation and builds both processes plus the CLI. Both runtime images include Git and a shell. Build toolchains required by your jobs (Node, Go, Java, etc.) must be added to your own agent image or installed on its dedicated host.

Container commands, for a machine with Docker:

```sh
docker build --target arcentra -t arcentra:local .
docker build --target arcentra-agent -t arcentra-agent:local .
```

The V10 hosted acceptance built and launched the application binaries with disposable containerized dependencies. It did not validate these Arcentra release images or their production deployment; a successful binary build is not release-image acceptance.

## Explicit schema initialization

Prepare a private configuration with the correct database DSN. Back up an existing database before applying a schema update.

```sh
./cli migrate --conf /private/path/config.toml
```

This is opt-in. Normal server startup does not migrate the database. The command creates/updates model tables and idempotently seeds built-in permissions, roles, bindings, and menus. It does not create users, grant a user administrator access, or overwrite existing role/menu customizations. For a new installation, register an account, then an operator with database access can explicitly select it with `./cli bootstrap-admin --conf /private/path/config.toml --username <registered-user>`. This command refuses to run once an enabled platform-super-admin grant exists. No administrator is granted automatically. After this explicit bootstrap, restart every control-plane instance to reload its authorization policy, then sign in again. Existing installations use their existing authorized role-management workflow. Fast schema tests use disposable SQLite, and V10 hosted acceptance successfully migrated and used fresh disposable MySQL schemas. Representative existing-data upgrades, production locking, backup/restore and deployment-specific compatibility still require staging validation.

## Agent trust boundary

The implemented worker supports **explicit trusted-code baremetal execution**. A job's shell commands run with the Agent process's OS permissions and can access that host's resources. A container image alone is not a per-job isolation guarantee. Use dedicated disposable hosts/containers, minimal credentials, and trusted pipeline definitions. Do not run untrusted pull-request code on a privileged host or mount its Docker/containerd socket.

`conf.d/agent.baremetal.example.toml` is an explicit example. Copy it to a private file and configure the control plane, Kafka, and registration token. Never commit live tokens or passwords.

The older `conf.d/agent.toml` requests sandbox mode and still fails closed instead of silently executing commands on the host. Requested Docker, Kubernetes runtime, command-denial or other unsupported enforcement is rejected.

The verified implementation run retained `OfflineRuntimeAccepted = false` and narrowly tagged assembly. The independent gate-enabled run passed ordinary production constructors; opening that compiled gate does not enable either operator policy by default. Its current bounded profile is trusted code, one Agent job or one explicit linear chain, an allowlisted digest-pinned image, explicit CPU/memory limits and an explicit non-root UID:GID matching the Agent. The container has no egress and receives neither a host runtime socket nor wider host paths. Trusted host source checkout/artifact-download staging precedes shell execution in one session; all container processes must be sealed before trusted host artifact uploads and cleanup. HTTP/native/deploy steps, standalone step tasks and host-path overrides are rejected for this profile. Whole-job retry uses the separate default-off generation permission and immutable input contract described below. Live failed-step retry has its own default-off permission and exact-candidate ordinary gate-on entry acceptance, recorded above; the backend and frontend PRs were draft when this evidence was recorded on 2026-10-15. Existing baremetal retries are unchanged.

Durable exact-owner runtime journaling and restart cleanup have the linked hosted implementation-level evidence. They do not resume or rerun interrupted commands. Startup requires authenticated, read-only confirmation of the original job/claim/runtime binding's terminal state before acknowledging a recovered journal record. Operators must precreate separate private persistent runtime journal/workspace roots; the runtime controller does not repair permissions or ownership. The independent gate-enabled run also verifies ordinary production constructors for this bounded profile. Packaged-service startup, production destinations and hostile-code isolation are not established by these hosted test binaries. See [sandbox integration](../sandbox-agent-integration.md).

The worker processes one job at a time per Agent. `maxConcurrentJobs` accepts 1 or 0 (the default of 1); negative values and values above 1 are rejected. Use multiple Agents for parallel jobs. Runtime-capability/readiness plumbing does not establish multi-job admission or HA fencing.

## Verification commands

```sh
go test -race -count=1 ./...
go vet ./...
```

Fast integration tests run real shell commands and SQLite persistence with authenticated loopback gRPC; mock-Kafka tests exercise wire/client routing and cancellation. Separately, V10 hosted acceptance ran the production Control → real Kafka → Agent shell → verified TLS gRPC → MySQL path twice in independent schemas with S3-compatible storage and Kafka logs. Each run lost committed step and job acknowledgments, required unchanged durable replay bytes/claims/digests, and verified commands were not repeated. This is disposable service acceptance, not validation of production broker TLS/SASL, rebalance, HA or infrastructure failure behavior.

Frontend (sibling `arcentra-web`):

```sh
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test:run
pnpm build
pnpm lint
pnpm format:check
pnpm test:ui
```

`pnpm test:ui` uses real Chromium with explicit simulated API fixtures. V10 also passed a separate actual-backend Chromium journey on a standard hosted runner: fresh install/migration, HTTP registration, explicit administrator bootstrap, login, TLS Agent enrollment/approval and same-identity restart, project/pipeline creation, Git definition review, shell execution, persisted logs and checkout evidence, byte-verified artifact download, refresh and re-login. Mobile controls and real downloads were exercised at 390/320/1440px, with screenshots reviewed. The local implementation sandbox's socket restriction was not bypassed; successful browser evidence comes from the linked hosted runs.

The commands above are instructions, not a passing result for the current offline-containerd changes. The separate hosted runtime evidence applies only to its exact tested revision; the independent gate-on result verifies its stated revision, and later runtime changes require fresh final-code regression and ordinary-entry acceptance.

## Remaining limits

- Disposable hosted MySQL/Kafka/Redis/S3-compatible service and browser acceptance passed for V10. Production/staging infrastructure, existing-data upgrades, private Git authentication, release images, load and HA still need environment-specific validation.
- Trusted baremetal remains supported. Offline-containerd implementation-level and independent gate-on ordinary-entry acceptance passed in hosted test binaries; operator policies default off. Its explicit linear-chain extension stages registered upstream artifacts into independent downstream workspaces; branches/joins and online package installation remain unsupported. Whole-job container retry requires the separate opt-in generation contract. Live failed-step retry retains the original session and partial filesystem state; its exact candidate passed implementation and ordinary gate-on entry acceptance, while merge/release and destination-specific activation remain separate. Kubernetes is not an Agent execution runtime.
- Ordinary controller-local and baremetal Agent jobs support the same explicit live-attempt retry policy: `failed_step` or `whole_job`, 2–5 attempts, idempotent opt-in, and an allowlist of real shell exit codes. Each attempt has an immutable StepRun identity. Manual reruns create a fresh pipeline run; post-terminal step resume is unavailable. See [retry semantics](retries.md). Offline whole-job generations use fresh isolated filesystems, immutable inputs and committed cleanup/admission evidence; V17 supports their separate exact-content approval gate. Live failed-step retries instead retain one session/workspace, require migration 008 and capable Agents, and reject `continueOnError`, background/zombie processes and restart continuation. Approval-bearing plans additionally require the separate [V19 exact contract and acceptance](failed-step-retry-approval.md), with independent default-off operator permissions; the exact gate-enabled ordinary entry passed the linked V19 acceptance. Both failed-step operator policies remain off unless explicitly enabled after reviewing the accepted build and destination-specific prerequisites. See [retry contracts](retries.md) and [failed-step evidence](failed-step-retry.md).
- Agent jobs with implicit deployment targets are rejected. The separate, default-disabled [controller-only Kubernetes release job](kubernetes-jobs.md) and [durable deployment approval](durable-deployment-approvals.md) flows have bounded implementation/test coverage, but no live cluster rollout was validated by V10. Offline-containerd jobs reject deployment steps and targets; there is no container-to-host deployment fallback.
- Automatic takeover/resumption after agent/control-plane process loss still needs execution-attempt leases and fencing. Explicit offline reconciliation is available below; completed-replay protection is present, but exactly-once execution across crashes is not promised.
- Gateway CloudEvents now persist transactionally before acknowledgment, and WAL restart preserves committed sequence numbers. Legacy Gateway PushLogs returns an explicit unsupported error; live build logs use Kafka.
- Agent terminal reports use a durable, fsynced, claim-bound outbox, with exact-byte replay and verified-conflict quarantine. Recovery requires the same surviving disk and original Agent/controller identity. A crash before a receipt is durably saved can still leave an unknown execution result requiring reconciliation; report replay never reruns the command. See [terminal reports](../agent-terminal-reports.md) and [identity persistence](../agent-identity-persistence.md).
- Remote Agent artifact steps support streaming transfers and downstream opaque-key references; authorized artifact APIs provide downloads. V10 verified upload/download bytes against a real disposable S3-compatible service, including the browser download path. Other provider SDKs, real credentials, retention and production storage need their own configured validation; controller-local filesystem artifacts are not automatically remote object URLs.
- Delay-queue scheduling outside the immediate pipeline path has not been comprehensively validated under broker/process failure.
- The frontend work covers the requested project/pipeline/build/agent integration. Other product areas that were already prototypes are not certified complete by this change.

See [automatic-trigger admission](automatic-trigger-admission.md) for cron/webhook rejection, durable acceptance and unresolved-outcome boundaries. [Explicit GitLab MR snapshot polling](gitlab-polling.md) adds a default-disabled, separately authorized baseline and selected-pipeline dispatch path. Other providers, historical event replay and MR-source validation remain unsupported; observed, definition and actual checkout revisions are distinct. Apply opt-in migration 009 before activation.

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

Restart services after reconciliation, inspect results, and trigger a fresh manual run if appropriate. Automatic execution takeover across controllers requires controller/Agent leases and fencing that the current design does not provide. The sandbox journal is narrower: it can clean only its exact recorded runtime resources on restart, then query authoritative terminal state. It does not automatically change interrupted run status, discover arbitrary orphan resources or execute work again.

## Durable build logs

Both the Agent and control plane accept already-redacted build output into an exclusive local spool before returning success to the executor. A successful append includes file and directory fsync. Kafka delivery uses `acks=all`; records remain until delivery confirmation. A stable event ID allows the database and active subscribers to deduplicate a replay after a lost ACK. Without Kafka, the control plane replays into its shared database-backed log aggregator instead.

Configure `[logSpool]` with `directory` and `maxDiskUsageMB` (default 512 MiB). The directory must be private, process-exclusive and on persistent local storage; each Agent/controller needs a separate directory. A process reopens and replays its own backlog at startup, without waiting for a new pipeline run. Files are 0600 and directories 0700; symbolic links, hard-linked records, foreign ownership, corrupt records and unsupported locking fail closed. Retain the same directory across restarts; never put it inside a disposable per-job workspace. The Agent default is `.build-log-spool` directly under its configured workspace root, outside individual Run/Job directories.

The queue additionally caps pending records at 10,000. It never evicts unacknowledged logs to make room. A full queue or filesystem failure rejects new output, stops the command, and is reported as an explicit failed execution rather than successful work with missing logs. Already accepted records are retained. Log-persistence failure fences later commands even with `continueOnError`. That option applies only to eligible ordinary command failures outside the offline failed-step profile; offline failed-step plans reject it. Temporary delivery failures produce safe generic operational warnings, with recovery notifications; raw payloads/transport errors are not logged by the spool. Known project secrets are masked before persistence. This is not encryption, and arbitrary secret strings unknown to the execution context cannot be automatically recognized.

Normal shutdown stops work before closing the spool. Confirmed records are deleted; unconfirmed records are replayed after restart. Terminal run state can become visible before queued output reaches the control plane. The frontend polls briefly after completion and keeps manual Refresh available for delayed output.

Kafka remains a trusted infrastructure boundary: restrict broker access and topic ACLs to authorized control/Agent services, use TLS/SASL in deployment, and do not expose the sample PLAINTEXT development broker publicly. A stable log ID provides retry deduplication, not cryptographic sender authentication.

Compose examples now bind separate control/Agent data directories. Kubernetes examples deliberately do not choose a storage class/PVC; they require an operator-provided per-instance persistent volume at `/var/lib/arcentra` before relying on recovery across Pod replacement. V10 tested disposable dependency containers, not these release images/manifests or production volume durability.

Tests exercise subprocess exit/reopen and injected fsync failures on temporary local storage. They do not verify power-loss persistence or the permanence of this assistant cloud filesystem. These guarantees require working fsync semantics and the same surviving disk. They do not protect against disk destruction, manually deleting the spool, misconfigured Kafka retention/replication, or sharing a spool between hosts. There is no transparent execution resumption: only log delivery is replayed. Spool corruption or a changed limit smaller than the retained backlog prevents startup and requires an operator to preserve and investigate the files, not silently delete them.
