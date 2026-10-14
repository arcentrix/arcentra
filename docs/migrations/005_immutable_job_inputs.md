# Immutable container input schema

This is an additive migration, applied only by the explicit `arcentra-cli migrate --conf <reviewed-config>` command through `repo.MigrateSchema`. Back up the selected database first. No migration runs at controller startup.

The JobRun model adds:

- `input_expectation` MEDIUMTEXT NOT NULL, default empty: canonical controller-selected artifact input set captured with runtime dispatch
- `input_manifest_state` VARCHAR(16) NOT NULL, default `unknown`
- `input_manifest` MEDIUMTEXT NOT NULL, default empty: canonical versioned source commit and artifact inputs
- `input_manifest_digest` VARCHAR(67) NOT NULL, default empty: independent original execution identity binding

There is deliberately no data backfill from terminal source evidence, produced artifact lists, mutable refs, or old task payloads. Existing jobs remain unknown. Existing container dispatches without an input expectation cannot use the new strict staging path; finish them with their original compatible Agent before upgrade or cancel them. Baremetal retains its existing source behavior. Deploy the migrated controller before upgraded container Agents; an unsupported RPC fails closed before artifact staging or user steps.

The manifest is fixed once before staging. Repeated reads and identical records require the same authenticated enabled Agent, original claim, source/runtime bindings, running job and pipeline, controller-selected artifacts, and nonexpired authoritative producer metadata. This migration does not enable container retries or generation dispatch.

Manifest encoding is capped at 3 MiB before capture and RPC parsing. The remaining 1 MiB of the default gRPC receive limit accommodates the execution identity and wire envelope. MEDIUMTEXT supports the full cap on MySQL; text defaults use explicit expressions (`DEFAULT ('')`) supported by MySQL 8.0.13+ and SQLite. The controller’s documented MySQL 8 requirement still applies.
