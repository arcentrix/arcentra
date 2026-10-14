# Safe deployment startup and acceptance

These are operator-reviewed examples, not a one-command production installation. No command in the normal server entrypoint creates credentials, changes schema, or deploys workloads. V10 passed disposable hosted real-service and browser acceptance; production infrastructure and release-image validation remain required. Offline-containerd implementation-level and independent gate-enabled ordinary production-entry acceptance have now passed separately in hosted test binaries; operator policies remain off by default. See [exact evidence and configuration](offline-runtime-acceptance.md).

## Public listeners and probes

The Control and Agent HTTP applications do not expose `/shutdown` or `/debug/pprof`. These reserved paths return 404, including unauthenticated requests and preflights. Stop a supervised process through the operating system/service manager (SIGTERM). There is no replacement remote shutdown token or new management listener.

`/live` indicates that the HTTP process can answer a probe. `/ready` indicates runtime readiness; `/health` remains its compatibility alias. Pending Agent enrollment, approval, failed authentication, worker startup and draining make readiness fail without causing liveness restart loops. Controller readiness includes its configured gRPC startup state; listener failure must not leave a healthy-looking API process. These checks do not certify end-to-end MySQL/Kafka/S3 or cluster availability.

Use `/live` for Kubernetes liveness and `/ready` for readiness. Examples include a 150-second startup-probe budget before liveness begins; tune it after measuring the authorized dependency environment. The example Service selector matches the actual Pod label. The Agent is a gRPC client and has no inbound gRPC server port. Metrics are separate internal endpoints, not authenticated public APIs.

## Controller gRPC transport

Direct server TLS is opt-in; an operator supplies existing certificate/key files:

```toml
[grpc.tls]
enabled = true
certFile = "/etc/arcentra/tls/server-chain.pem"
keyFile = "/etc/arcentra/tls/server-key.pem"
```

Mount only the required files read-only, with private-key access restricted to the service user. The product does not issue or save new credentials, modify file permissions, install a CA, or change host networking. Controller configuration is parsed into a private startup snapshot. The old file watcher is removed: all configuration changes, including TLS and project/provider allowlists, require explicit restart; changing a file does not partially reconfigure running services. Live database authorization, approval, revocation and source checks remain live. Restart explicitly after an approved certificate change. Invalid/incomplete files fail startup rather than falling back to plaintext. Server authentication does not replace application bearer-token authorization. No client certificate enrollment or automatic certificate rotation is provided.

Agent configuration continues to use `grpc.tls.enabled`, `caFile` and `serverName`; the hostname must match a certificate SAN. Never disable validation. Existing persisted identities bind the exact loaded CA bytes, endpoint and effective hostname: do not change those as an incidental rollout, regenerate CAs, delete journals or blindly re-enroll.

Legacy plaintext is retained only for a trusted internal transport boundary. It does not enable verified-conflict quarantine or durable enrollment. An existing verified TLS proxy is also supported; its backend path must be private/trusted and cannot be exposed as an alternate unauthenticated public transport. Browser/API HTTP needs its own approved HTTPS termination; gRPC TLS does not encrypt HTTP. Reflection is disabled by default.

## Task queue transport and existing configuration upgrades

The outer Kafka task envelope is the existing Nova JSON `TaskMessage`, including its unchanged task payload bytes. Control and Agent support `taskQueue.messageFormat` and `taskQueue.messageCodec` values `json` or `sonic` only; Sonic is a JSON implementation and both values are wire-compatible. Omitted values default to Sonic. An explicit nonempty `messageCodec` selects the implementation, but both configured fields must be supported. Case and surrounding whitespace are normalized. This does not alter gRPC protobuf APIs or introduce a protobuf task protocol.

Before upgrading an existing installation, replace historical `protobuf` or `blob` values in both Control and Agent configuration with `sonic` (or `json`). Both application startup paths now reject incompatible/unknown values with the exact configuration field and supported alternatives, before connecting to application dependencies. Update both processes' reviewed files and restart explicitly. Existing valid JSON messages require no payload rewrite or broker purge; retain queued work and journals. Do not delete pending messages as an upgrade step.

When Kafka is configured, producer/worker initialization errors are propagated; Control never silently falls back to local execution. Only absent `messageQueue.kafka.bootstrapServers` retains the existing no-Kafka mode. Successful initialization is not a broker reachability/readiness guarantee: Kafka connects asynchronously, so actual enqueue/consume acceptance is still required.

## Packaging and configuration checklist

- Pin reviewed image versions/digests. The existing release workflow publishes under GHCR, whereas historical Compose/Kubernetes examples name Docker Hub images. Verify the actual image destination and architecture before use; no image was published by this change.
- Both source-build and release-image layouts include the operator CLI and use `/var/lib/arcentra` as working directory, so relative spools land under the documented persistent mount. Do not share process-exclusive spool/identity directories across replicas.
- `Dockerfile.ci` consumes release-built binaries. Actual container startup and dynamic-library compatibility have not been validated in this cloud environment; do not infer that a successful Go build certifies a release image.
- Compose's configuration bind is repository `conf.d`, read-only. The file is still an incomplete dependency example: Kafka, object storage, dependency readiness, database creation and container-address configuration are prerequisites. Default loopback addresses refer to each container itself. Do not paste placeholder passwords into a real deployment.
- Kubernetes examples intentionally omit PVC and credential resources. Provide reviewed per-instance storage and configuration before relying on restart durability. Single-replica examples do not provide HA leases/fencing.
- Systemd templates use the actual `-plugin-conf` flag and explicit restart semantics; SIGHUP is shutdown, not reload. StateDirectory and working/writable directory are `/var/lib/arcentra` for Control and `/var/lib/arcentra-agent` for Agent. Set Agent workspace/temp paths accordingly in its reviewed configuration. These are template edits only, not host configuration changes.
- Historical `agent.toml` requests unsupported bridge/legacy defaults and still fails closed; it is not the explicit offline profile. Use the [review-only offline configuration](offline-runtime-acceptance.md) only after reviewing the exact hosted ordinary-entry evidence and completing destination-specific checks. `agent.baremetal.example.toml` is only for explicitly trusted code and provides no same-UID isolation. Do not enable it silently to make a healthcheck green.

## Explicit migration and upgrade

1. Back up the target database and test restoring it. Confirm the exact configuration file/database destination; use an isolated staging copy first.
2. Stop controllers and Agents for a maintenance-window upgrade. Preserve journals, identity directories, artifacts and audit evidence. Reconcile active/unknown effects through the existing documented recovery process; do not automatically rerun work.
3. Inspect existing approval uniqueness conflicts as described in `migrations/003_durable_deployment_approvals.md`. Do not delete historical votes or invent source/approval lineage to force an index to succeed.
4. Run the supplied `arcentra-cli migrate --conf /absolute/path/to/reviewed-config.toml` explicitly on the intended database. This uses GORM schema migration plus idempotent built-in authorization seeds; it does not create an administrator or grant user privileges. It is not an automatic rollback system or a guarantee of zero-lock/destructive-free changes on every legacy schema.
5. Verify required tables/columns/indexes and existing data before starting one controller and one Agent. Restore the backup and compatible binaries if the staging rehearsal demonstrates that rollback is needed; do not assume an older binary is compatible with every migration.

Do not run historical `docs/migrations/002_authz.sql` as an upgrade script: it drops legacy tables/columns. Normal startup, probes and image entrypoints must never execute migration or seed actions implicitly. The offline-runtime schema adds persisted Agent capability and immutable job-runtime binding fields. Review and explicitly migrate the final schema before any future authorized rollout; a migration does not open the runtime acceptance gate.

## Hosted evidence and remaining acceptance

V10 passed backend main `6c24bd389d76fe749ad07a8e23fc049d6cd46c93` in [run 38393872734](https://github.com/arcentrix/arcentra/actions/runs/38393872734) and frontend main `e7906be0e9909de26dea2467744420c197d27237` in [run 38393804004](https://github.com/arcentrix/arcentra-web/actions/runs/38393804004), verified on 2026-10-13. Backend CI exercised real disposable MySQL, Kafka, Redis and S3-compatible storage, verified TLS Agent execution and lost-ACK report replay, plus a fresh-install actual-backend Chromium journey. The separately named frontend Chromium job uses simulated API transport. See [the exact scope](real-stack-acceptance.md).

Still required on the intended authorized deployment: build and start both release images; run their `--help` and CLI `migrate --help`; rehearse an existing-data MySQL upgrade and restore; verify production Kafka TLS/SASL, object storage, TLS trust and credential handling, durable volumes, cancellation/restart behavior, load and HA limits. No live Kubernetes deployment, external model request, private Git authentication or production infrastructure validation is inferred from disposable CI. Any such destination needs its own explicit configuration and acceptance.

The assistant cloud workspace's missing runtime and Chromium socket restrictions were not bypassed. Successful real-service/browser results came from standard GitHub-hosted runners, not a local mock or skipped test. Containerd namespace/cgroup/process behavior and the staged worker/recovery path were not included in V10; they passed separately in [runtime run 38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022), at PR head `e7132be275c144a2eadc03b61e3c4f16dd302c4e` (tested merge `12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`). That implementation-level run kept the production gate false. Independent gate-enabled [run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313) also passed the ordinary `NewProcess`/`StartWorker` constructors at PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c` (tested merge `1d2e29030741963897b7056c7d15249f2637d757`), with clean cleanup receipts. These hosted test-binary results do not establish packaged-service or release-OCI startup; destination-specific deployment checks remain required. See [containerd acceptance](sandbox-containerd.md); do not broaden the authorized temporary-VM privilege boundary.

References: [gRPC authentication](https://grpc.io/docs/guides/auth/) and [Kubernetes probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/).
