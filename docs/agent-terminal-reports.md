# Durable Agent terminal reports

## Rollout and requirements

The Agent persists final job/step status reports in `<agent.workspaceDir>/.terminal-report-outbox` (or `./.terminal-report-outbox` when the workspace directory is empty). Use a persistent local filesystem supporting file and directory `fsync` and advisory file locking. This implementation requires a Unix platform; unsupported platforms fail closed. It is not a shared network queue or a substitute for backups.

The control plane stores `terminal_report_digest` and `cancellation_claim_id` on job and step runs. Back up the database, test the additive migration on a staging copy, and explicitly run the existing CLI migration against the intended database before rollout:

```sh
arcentra-cli migrate --conf /private/arcentra/config.toml
```

Ordinary control-plane startup does not migrate the schema. This migration also retains the existing CLI's authorization seeding behavior; it does not grant an administrator. Production MySQL migration and production disk failure behavior still need operator validation.

Set `taskQueue.autoCommit = false`. The Agent rejects automatic Kafka commits because they can advance an offset before the handler's report has been persisted and acknowledged. The existing supported execution mode remains explicit trusted `baremetal`, with sandbox disabled. This change does not enable sandbox execution or isolation. Files owned by the Agent are not a security boundary against commands run as the same OS user; use a dedicated trusted host.

## Delivery contract

1. The worker snapshots the terminal protobuf request, original execution-claim ID, and any step dependencies. Timestamps, exit codes, status, and artifact metadata are not recomputed on replay.
2. Declared `secrets.*` environment values, source passwords/tokens and Agent transport credentials are removed from error text before serialization. Credential-bearing URL userinfo and common token/signature query parameters are also masked. If a declared secret remains in identifiers or artifact metadata, the report is rejected rather than changing those immutable fields. Commands, task payloads, environment maps and connection credentials are never journal fields. Arbitrary undeclared secrets cannot be inferred reliably; do not put them into names or output.
3. A private temporary file is written and synced, atomically renamed, and followed by directory sync before the first terminal RPC. A pending job receipt is persisted only after all outstanding step receipts are durable. The job record names its step dependencies, so replay cannot acknowledge the job before those steps.
4. Delivery uses current transport authentication and the original claim ID. The same bytes are sent on retry. A record is removed only after an explicit successful RPC response, and deletion is directory-synced before the handler can acknowledge the Kafka task.
5. The control plane acknowledges a duplicate only when the original claim and full terminal request digest match the committed receipt. Duplicate replay does not rewrite terminal state or recreate pruned artifact rows.

Reports are replayed during a bounded 10-second startup pass and again by a background loop. Every work-queue delivery is gated behind pending report replay, so an unresolved historical report prevents new commands. Independent cancellation consumption remains available. RPC contexts are bounded; stopping the worker cancels background replay and releases the journal lock after in-flight task handling has stopped. A cancelled command still has a chance to persist its final report with a separate bounded reporting context.

## Controller-requested cancellation

Controller cancellation atomically leaves the remote job and its unfinished step attempts canceled, with a cancellation-origin field bound to the original job claim. It does not overwrite already successful or failed steps. A later cancellation report may attach its exact digest once; it cannot change status, timing, error text, execution claims, or artifacts. Exact duplicate cancellation receipts can then be acknowledged after a restart. Legacy canceled rows without this explicit provenance remain fail-closed.

For job-bound execution, step claims are derived from the original job claim and the exact preallocated step-attempt ID. This also identifies never-started canceled attempts without inventing a successful execution claim. A RUNNING claim retry that reaches an already-canceled matching step receives a scoped, typed cancellation rejection, allowing the Agent to report cancellation even before the Kafka control message arrives. This rejection never confirms execution or a terminal result. Standalone step claims remain unchanged. Journal replay always uses the saved original claim, including older claimed steps. Upgrade controller and Agents together: older Agents' random claims for never-started canceled steps cannot satisfy the new exact binding.

A materially different late result, including SUCCESS or FAILED that races with controller cancellation, is **not** acknowledged as cancellation and cannot replace the canceled result or register artifacts. It remains durably pending and currently blocks new work on that Agent. Timeout/recovery conflicts have the same fail-closed limitation. There is no automatic quarantine or administrative receipt-discard protocol in this version. Stop the affected Agent, preserve its journal and the controller database, inspect actual command effects and the conflicting records, and escalate for explicit operator reconciliation. Do not delete or rewrite the journal, alter terminal rows to forge acceptance, re-enroll the Agent, or claim that restarting alone resolves a genuine conflict. If work must continue, provision a separate authorized Agent and keep the affected identity and journal intact.

## Identity and storage limits

The directory is permanently bound to a SHA-256 hash of the exact Agent ID and configured controller logical address. Authentication tokens are not part of this binding and are not saved. A changed Agent ID or controller address, including a differently spelled alias, fails closed even if the directory is empty. The Agent never adopts another identity's receipts. To move an installation, stop it, verify outstanding delivery using the original identity and controller, and have an operator plan the state migration explicitly; do not delete or rewrite receipts to suppress errors.

Process-restart recovery requires the operator to configure the original `agent.id` and per-Agent `grpc.token`, the same `grpc.serverAddr`, and the surviving workspace. When both identity fields are configured, they take precedence over a remaining `grpc.registrationToken`; failed per-Agent authentication never falls back to dynamic enrollment. The journal hash is an identity binding, not a credential.

Pure dynamic enrollment is **not** restart recovery: every successful enrollment creates a new Agent ID, and the returned ID/token are kept only in memory. Without complete configured credentials, dynamic registration is blocked before contacting the controller whenever `.terminal-report-outbox` already exists, including empty or incomplete journal state. This read-only preflight does not delete, rebind, recover, or change journal permissions. The operator must supply the original credentials to recover; this change does not save credentials, enable persistence settings, or introduce a new enrollment protocol.

Directories use mode `0700` and files `0600`. Startup validates ownership, rejects symlinks and hard-linked files, and requires an exclusive writer lock. Records have checksums and hashed filenames. Defaults are fixed bounds: 64 MiB of serialized pending records, 10,000 records, and 4 MiB per record. Quota, malformed records, unexpected directory entries, unsupported filesystem operations, or uncertain sync outcomes do not discard committed records or acknowledge tasks. A partial `.pending` write was never accepted for delivery and is discarded on restart.

A storage failure fences later commands even when a step requests `continueOnError`. Ordinary RPC loss after durable persistence does not turn a successful command into a failed command or grant command retry. Explicit retry policies are separate from report replay and must use a fresh step-attempt ID.

## Crash windows and operator handling

- Before a terminal receipt is persisted: an already-claimed command is never automatically resumed or rerun. Its exact result may be unavailable; normal interrupted-run recovery and operator inspection are required.
- After persistence but before RPC acknowledgement: a restart with the original configured identity replays the saved report, never the command.
- After the control plane commits but its acknowledgement is lost: replay receives the exact-match acknowledgement for the original request and claim.
- After report acknowledgement but before Kafka offset commit: a redelivered task cannot acquire a new claim for the completed run, so its commands do not run again.
- After an uncertain directory sync: the current journal instance stops writes. Restart validates whatever committed state the filesystem retained; a surviving report can be replayed safely.

Terminal rows from older versions without a digest, and rows marked failed by offline/interrupted-run recovery, do not prove that a late Agent payload was accepted. The controller rejects those receipts even when their status happens to match. Rejected reports stay on disk and block new commands for operator reconciliation. Likewise, changing a payload, claim, storage identity, or pipeline/job scope never converts a rejection into successful delivery.

Recovery covers process restarts with the same intact disk and identity. It does not guarantee exactly-once commands, resume partially completed jobs, retry side effects, survive disk loss, or transfer receipts to a newly registered Agent.

## Verification

Automated tests cover real-filesystem reopen, ownership locking, private modes, identity changes, symlink/hardlink/corruption rejection, dependency ordering, quota and file/directory sync faults, cancellation, secret-safe persistence, manual-commit enforcement, and real gRPC plus SQLite terminal-commit/ack-loss and queue-ack crash windows. The tests clear all Agent in-memory receipt/claim state, reopen the journal, verify unchanged request bytes/claims, and assert the shell side effect happened only once. Kafka integration tests use the real Kafka wire protocol against a local mock cluster; they are not production-cluster validation.

Separate synthetic two-lifecycle registration regressions prove configured identity precedence, blocked enrollment when a journal survives but original credentials are missing, no dynamic fallback after failed authentication, and unchanged journal bytes and permissions. These tests do not claim that dynamic enrollment credentials are persisted or automatically recovered.

Controller-to-Agent round-trip tests start with `Process.Submit` and no pre-created job/step records, use authenticated loopback gRPC and real SQLite repositories, and verify downstream artifact consumption, exact producer attempt/storage/SHA identity, committed-ACK replay, cancellation of all retry slots, successful subsequent work on the same Agent, and journal drain.
