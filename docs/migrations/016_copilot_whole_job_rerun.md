# 016: durable whole-job new-execution records

This additive, explicit migration follows 015 (lossless audit actions). Run
`arcentra-cli migrate --conf <reviewed-config>` against the intended database.
Normal Control startup does not migrate. Application wiring now connects
prospective capture, the authenticated Prepare/Confirm API, dispatcher, Agent
claim/source observation, fresh approval, terminal receipts and protected
cancellation. Migration alone activates none of them. The release gate
`TerminalRerunAccepted` remains `false`; the independent Control and Agent
operator settings also default off. Enabling a setting does not bypass the
release gate. Real integrated MySQL, containerd/Agent and browser acceptance
for this new-execution path remains pending. See the
[operator and user guide](../copilot-terminal-whole-job-execution.md).

## Before upgrading

Back up and verify restoration; stop all Control writers before DDL. Do not run
mixed old/new writers. MySQL DDL implicitly commits, so a failed command is not a
transactional rollback. Keep writers stopped until the schema and matching binary
have been checked. Preserve existing key configuration; no key is created or
rotated by this migration.

Every existing preflight and the new rerun preflight runs before any new DDL.
Unknown/partial marker groups, partial ledger sets, changed column/default/index
shapes, or orphan protected identities require operator review. Such an
interrupted partial migration is **not** an idempotent successful migration:
inspect and repair from the reviewed schema or restore the backup, then rerun.
One narrow intermediate state is recoverable automatically: the exact nonunique
191-character Step JobRunID prefix index exists, the original LONGTEXT values and
collation are unchanged, and no partial marker groups or authority tables exist.
This access-path-only state passes preflight and migration continues; success is
reported only after the complete schema and seeding succeed.
Never synthesize snapshots, votes, claims or input manifests to make checks pass.

## Changes

- `pipeline_run`: `copilot_rerun_intent_id`, `copilot_rerun_gate_id`.
- `job_run`: the same links plus `expected_source_commit`.
- These marker columns are VARCHAR(64), NOT NULL, DEFAULT ''. Historical rows
  therefore retain ordinary semantics and gain no rerun authority.
- `step_run.job_run_id` stays LONGTEXT, with its existing nullability, charset,
  collation and full values. A nonunique BTREE prefix index
  `idx_step_run_job_run(job_run_id(191))` supports full-equality target-child
  checks; the prefix is an access path, not a shortened identity. Known existing
  LONGTEXT without this index is upgraded by index-only ALTER. The exact complete
  shape is idempotent; unknown types/indexes are rejected rather than truncated.
- Register `copilot_definition_snapshot`, `copilot_rerun_intent`,
  `copilot_rerun_admission_gate`, and `copilot_rerun_schedule`, with their declared
  unique identity/request keys and bounded lookup indexes. Ciphertext stays in
  MEDIUMBLOB; definition/evidence/context content and full digest widths are
  preserved. Existing table charset/collation is not rewritten.

No historical run receives a definition snapshot. With the validated opt-in
startup bundle, a new verified manual trigger can atomically save its Run,
encrypted resolved-definition snapshot and Inbox event. Ordinary parsing or a
successful legacy run is not evidence of capture; an existing run is never
backfilled from current HEAD. Definitions with managed secret references or
nonempty Source.Auth are unsupported by this bounded path.

Confirm creates fixed new Run/Job/Step-attempt identities plus admission and
schedule records, leaving the original failed run unchanged. It neither runs
commands nor records an approval vote. The configured dispatcher can subsequently
seal and notify that exact new execution. Expected source SHA stays separate
from the Agent's observed checkout: Confirm and claim never pre-pin inputs or
reuse old approval votes. A fresh exact-input approval is required before user
commands may start.

## Required verification

Ordinary tests cover fresh and legacy migration, replay, safe historical defaults,
long multibyte JobRunID preservation, ciphertext/receipt preservation and failure
before DDL. They do not establish MySQL lock or upgrade behavior.

Provision a fresh, disposable loopback MySQL 8 schema named
`arcentra_copilot_rerun_test` or `arcentra_copilot_rerun_test_<suffix>`. The fixture
refuses remote hosts and nonmatching schemas and requires the schema initially
empty. It drops its fixture tables. Set the existing test-only account DSN in
`ARCENTRA_COPILOT_RERUN_MYSQL_DSN`, then run:

```bash
set -o pipefail
ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL=1 \
  go test -race ./internal/control/repo \
  -run '^TestCopilotRerun(Confirm|Schema|Seal|Claim|Reports|Expiry|Dispatch|CancelActor)MySQLRequiredGate$' \
  -count=1 -timeout=8m -v | tee /tmp/copilot-rerun-mysql.log
python3 scripts/acceptance/verify_copilot_rerun_mysql_log.py /tmp/copilot-rerun-mysql.log
```

Run all eight roots: Confirm, Schema, Seal, Claim, Reports, Expiry, Dispatch and
CancelActor. The log verifier requires every named root, collation and scenario
exactly once, plus the actual-work/terminal-output witnesses; partial, duplicate,
failed or skipped coverage is rejected. This is a required test command, not a
claim that the hosted suite has passed.

All suites run `utf8mb4_0900_ai_ci` and `utf8mb4_bin`. The schema suite invokes the
production explicit migration for fresh/upgrade/replay, validates exact index and
column metadata, preserves long values/ciphertext, and rejects partial shapes
before writes. The transaction suite uses independent connections whose default
is READ COMMITTED, proving the new scoped REPEATABLE READ boundary through exact
server-observed holder/waiter edges. Its read-only lock observer needs the same
performance_schema access as the existing approved contention fixtures; this
migration does not grant it. A skipped suite is not acceptance evidence.

## Rollback

Disable the feature, stop writers and return only to compatible application code.
Keep the additive tables, markers and lossless LONGTEXT index structure. Do not
purge durable request deduplication, ciphertext, fixed IDs or confirmation receipts.
Do not shrink JobRunID or discard its suffix to restore an old shape. Restoring an
older backup loses later receipts; never automatically replay confirmations from
an uncertain outcome. The application may inspect fixed durable receipts and
renotify still-unclaimed work; migration provides no dispatch or claimed-session
recovery. A lease change, lost response or Agent restart cannot authorize a new
claim, a replacement Agent or command replay for already-claimed work. Preserve
runtime journals/outbox records for the existing bounded recovery procedure.

## Admission incarnation digest extension

The gate includes `admission_nonce_digest VARCHAR(67) NOT NULL DEFAULT ''`.
Only its versioned domain-separated digest is persisted; the authenticated
32-byte process nonce is transient and is never backfilled, logged or returned.
The exact known predecessor without this one column may be upgraded only after
all ordinary schema preflights and read-only evidence checks succeed. Every gate
and schedule must remain unclaimed (or preclaim-cancelled), and protected
Run/Job/Step rows must have no running/start/claim, observed input, approval,
generation or terminal-report execution evidence. A claimed timestamp, running
Job, claimed Schedule or unknown/partial shape fails closed before any DDL.
The extension cannot reconstruct an incarnation for previously claimed work.
Stop all writers for migration, retain expanded schema on application rollback,
and inspect/repair any interrupted non-atomic DDL before retrying.

## Artifact orphan-evidence access paths

The same read-only preflight now recognizes two additional nonunique access
indexes: `step_run_artifact.idx_step_run_artifact_job_run(job_run_id(191))`
and `project_artifact.idx_project_artifact_job_run(job_run_id)`. The former
retains its historical nullable LONGTEXT column and complete values; the latter
retains its nullable VARCHAR(64). Charset and collation are never rewritten.
Every probe retains full JobRunID equality; prefix bytes do not establish
identity. Unknown column shapes, alternate/composite/unique/hidden/expression
JobRunID indexes, or an incorrect prefix fail before any planned DDL.

Any subset of these exact additive access indexes and the StepRun prefix index
is a recognized restart point, provided all remaining preflights pass. This
covers interruption between index-only ALTER statements, not partial authority
ledgers or marker groups. Success still requires the entire migration. Keep the
indexes on application rollback; never truncate stored identifiers.

The existing `TestCopilotRerunSchemaMySQLRequiredGate` command additionally checks
both artifact column collations and full historical values, exact prefix/full
index metadata, index-only recovery, and refusal before unrelated DDL. Its
`artifact-indexes` case seeds 2,000 unrelated rows per artifact table, validates
EXPLAIN ANALYZE actual rows/loops, proves an exact missing-JobRunID insertion
waits on an independent server-observed lock edge, and verifies an unrelated
primary-key update remains possible while that range is held. Both configured
collations must pass; an unexecuted or skipped fixture is not lock evidence.
