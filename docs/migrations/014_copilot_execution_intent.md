# 014: detached Copilot retry-approval execution intent

This is an explicit, additive operator migration from migration 013. Use the
existing `arcentra-cli migrate --conf <reviewed-config>` command against the
intended database. Control startup does not migrate. Migration does not contact
providers, fetch source, create credentials, grant users access, submit votes,
start jobs, or enable execution flags.

## Before migration

1. Back up the database and verify restoration. Preserve the prior binary/schema.
2. Stop all old Control writers before DDL and keep them stopped until compatible
   controllers are deployed. Mixed-version writers are not supported. MySQL DDL
   implicitly commits: do not assume transactional schema rollback.
3. Leave `copilot.execution.retryApprovalEnabled = false`. The migration does not
   enable Copilot, history, retry approval, Agent runtime, or any other operator
   gate. Existing managed credentials and encryption-key procedures are unchanged.
4. Verify database identity, space and privileges. Inspect any partial previous
   migration before retrying. All execution schema preflight runs before any DDL.

## Added table

`copilot_execution_intent` contains detached identifiers and opaque digests,
versions, bounded typed state, and lifecycle timestamps. It has no prompt, source,
command, environment, provider-response, plan-body, credential, or ciphertext
column. It does not introduce a foreign-key cascade from private history.

Required unique indexes bind `intent_id`, `decision_id`, and the composite
`(owner_user_id, project_id, request_id)`. Bounded indexes cover
`(owner_user_id, project_id, id)` and `(expires_at, id)`. Identity strings use
VARCHAR(64), digests VARCHAR(128), state VARCHAR(16); indexed strings remain within
MySQL utf8mb4 key limits. IDs, states, versions and authority have no schema defaults
that could manufacture an approved/prepared record. Existing tables and historic
review plans are not backfilled into executable intents.

An absent table is added. An intact schema is idempotently reapplied. Existing
partial columns, missing/mis-shaped indexes, nonunique replacements, duplicate
identities under the database collation, or blank/padded identity columns fail
closed for operator review. No automatic repair invents binding or authority.
Applications additionally reject case-aliased identities instead of treating a
case-insensitive database match as proof of exact identity.

`expires_at` limits submission eligibility, not audit retention. Identifier-only
intent and vote linkage survives history deletion and expiry. There is no automatic
ledger purge in this slice: deleting deduplication keys could admit reused request
IDs. Admission is capacity-bounded; archival or retention-policy changes require a
separately reviewed design preserving deduplication. History content retention and
backup retention remain governed by migration 013 and operator policy.

## Verification and rollback

Run the Copilot execution schema/repository/service tests and existing history and
retry-approval regression suites. Verify upgrade from 013 creates zero intents,
votes, jobs, or grants and preserves old review-plan ciphertext. Required schema
checks include columns, defaults, all unique indexes and pre-DDL partial-schema
rejection. SQLite success does not establish production MySQL locking, races, or
actual Agent retry execution. Do not enable the feature before the disposable
MySQL tests and real fail-first-then-retry acceptance pass.

For rollback, disable the feature and stop writers before returning to compatible
code. Preserve the new table and immutable approval votes; never drop it as a
routine rollback or synthesize missing records from history. A restored older
backup loses post-backup request receipts: do not automatically replay prior
confirmations. Inspect and explicitly repair or restore partial MySQL DDL.

See [bound retry execution](../copilot-bound-retry-execution.md) for the user-visible
contract and acceptance boundary.
