# Offline runtime contract schema upgrade

Offline containerd execution remains unavailable in production pending authorized, dedicated-host runtime acceptance. `pipeline.offlineRuntime.enabled = true` is rejected by this build. Unit tests inject a bounded policy without connecting to containerd; this is not runtime acceptance.

This change uses the existing explicit `arcentra-cli migrate --conf /path/to/config.toml` operator migration. Do not run a migration automatically on service startup. Back up the database and validate a representative copy before using the migration.

Additions:
- `agent.runtime_capabilities`: nullable JSON, supplied only by credential-validated Agent registration or authenticated heartbeat. Missing old reports permit legacy baremetal selection only. Explicit execution mode is recorded independently of readiness: an unready containerd Agent is never selected for baremetal work. Unknown or malformed modes are excluded from all execution. An old heartbeat clears stale readiness; user labels do not grant runtime capability.
- `job_run.runtime_type`, `runtime_image`, `runtime_cpu`, `runtime_memory`, `runtime_binding_digest`: non-null strings with empty defaults. Historical rows retain baremetal semantics; do not backfill fabricated runtime evidence.

The controller atomically stores the image digest, resource limits, original Agent identity and source expectation before dispatch. Generic updates cannot change the runtime columns. Runtime jobs require the exact recomputed binding digest on the initial execution claim and subsequent reports. An older Agent that ignores the payload field cannot claim a runtime job. Legacy single-step fetch/stream interfaces exclude runtime jobs entirely, and runtime step claims are transactionally bound to the original running parent claim. The recovery inspection RPC is authenticated, read-only and restricted to the exact original Agent/job/claim/binding identity.

The supported injected profile is `containerd-offline-v1`: exactly one complete Agent job, an explicitly allowlisted `image@sha256` reference, mandatory positive CPU and memory hard limits bounded by operator maxima, and no runtime config flags. Shell steps accept only an empty action or `run`. Artifact downloads form a prefix; shell steps form the middle; artifact uploads form a suffix. HTTP/native/deployment steps, host paths, standalone tasks and automatic retry are rejected. A retry requires a new run and fresh isolated workspace.

Fresh disposable SQLite tests cover additive defaults, capture/claim ownership and read-only recovery confirmation. Production MySQL row locks, collation, JSON storage and migration execution remain operator validation responsibilities. Never downgrade by dropping evidence while runtime jobs or recovery records remain unresolved.
