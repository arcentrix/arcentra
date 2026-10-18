# Project secret management and run binding

Apply with the existing operator-run `arcentra-cli migrate --conf /private/path/config.toml`
against a backed-up representative copy before deploying the API/UI. Ordinary
server startup does not migrate. Pause administrative writes during migration;
MySQL DDL is not atomic. Inspect a failed migration before retrying.

## Preflight and additive state

Before any schema write, secret IDs and (scope, scope_id, name) combinations are
checked for duplicates under the database's native collation and lower-case
normalization. Empty/null/oversized/whitespace-padded identities and ambiguous
project-scope spellings fail closed. Legacy project names must match the same
ASCII identifier syntax as new project secrets; historical names such as
`deploy-token` require explicit operator remediation before migration. Unicode
whitespace padding is rejected as well. Values/ciphertext are never inspected or
rewritten by migration. No rename, merge, deletion, key generation, credential
rotation, organization creation, membership or authorization grant is automatic.
Review ambiguous legacy rows separately. Global scope uses the existing empty
scope_id convention; null legacy scope IDs require explicit operator remediation.

- secret.secret_id: bounded unique business ID (64 bytes)
- secret.(scope,scope_id,name): bounded unique scope/name key (16/64/128 bytes)
- secret.version: monotonic uint64, initial default 1
- secret.is_enabled: soft lifecycle state, existing records default 1
- pipeline_run.project_id: private immutable admission-time original project ID;
  legacy runs retain empty values. Historical assignments are never inferred or
  backfilled from a mutable current pipeline definition.

The original project binding is captured under the existing admission locks for
new runs. Legacy unpinned runs cannot resolve secrets: start a newly admitted run
rather than guessing historical ownership. Rollback must retain the new columns,
unique indexes, versions, tombstones and audits.

## HTTP contract

Every route is authenticated and checks fresh primary-database authority inside
the resource transaction. Existing secret:read/write/delete permissions apply to
the exact project and its organization; no default user grants are added.

- GET /api/v1/project/:projectID/secrets: complete metadata inventory, max 1000
- POST same path: {name,secretType,description?,value}
- PATCH /:secretID: {version,description}; metadata only
- POST /:secretID/rotate: {version,value}; explicit replacement
- POST /:secretID/disable: {version}; irreversible soft disable in this milestone

All mutations return metadata only. IDs, name, type, project assignment and
creator are immutable. Names are case-preserving ASCII environment identifiers
matching [A-Za-z_][A-Za-z0-9_]{0,127}; case/collation name aliases are conflicts.
Types: password, token, ssh_key, env. Description limit: 4096 characters. Values:
1–65536 UTF-8 bytes, no NUL bytes or blank/mask/redaction placeholders. Unknown, duplicate,
case-alias and null fields fail validation. Metadata edits omit value and preserve
it; supplying value requires the rotate route. Versions are opaque decimal
strings and require exact CAS. An unchanged metadata edit still increments its
version, so old requests cannot regain validity. No hard deletion or re-enable
route is provided.

Inventory: {projectId,list,total,canCreate,canWrite,canDisable,projectActive,
encryptionConfigured}. Each row: {secretId,projectId,name,secretType,description,
version,isEnabled,configured,createdAt,updatedAt,canUpdate,canRotate,canDisable}.
Permission flags are advisory snapshots; mutations recheck. Metadata reads and
metadata/disable edits do not require the key. Create and rotate require the
operator's existing strict base64-encoded 32-byte ARCENTRA_SECRET_ENCRYPTION_KEY.
There is no generated or fallback key, automatic key rotation or key recovery.
HTTP 400 input, 401 unauthenticated, 403 current access denied, 404 exact scope/ID
missing, 409 stale CAS/lifecycle/name conflict, 503 unavailable source/crypto.

No route returns plaintext, ciphertext or a masked value placeholder. Legacy
/secrets endpoints retain non-project compatibility but cannot create, list,
read, move, update, delete or retrieve a project secret value, including guessed
IDs. The internal metadata collection used for existing Copilot redaction remains
available; this milestone adds no secret values/version history to Copilot input.
Success writes and minimal success audits commit atomically. Audit payloads are
empty objects; descriptions, names and credentials are not copied into them.
Credential writes use silent database logging and errors are value-independent.

## Runtime lifecycle and limits

Execution reads one complete primary project-bound snapshot. Decryption is
all-or-nothing: missing keys, corrupt ciphertext, ambiguous names, inactive
projects, more than 1000 rows or over 1 MiB of active plaintext fail closed before
any secret reaches execution. Before loading ciphertext, lengths from the locked current metadata read reject
more than 2 MiB total active ciphertext or 128 KiB for one record; disabled
ciphertext is not materialized. Empty active collections require no encryption key.
Disabled values are not decrypted or injected. Values and the exact metadata
version snapshot are retained in memory and never added to run metadata.

Rotation affects future resolutions. A changed complete snapshot fails a new
undelivered job/handoff; cached values are never silently replaced in an approved
plan. Checks occur before new DAG dispatch, after approval waits, before Agent
enqueue and local execution/retry handoff. These checks are not a distributed
atomic recall mechanism. Queue acceptance is delivery; ambiguous enqueue outcomes
remain unknown and cannot be assumed withdrawn. Already queued/running Agent jobs
and their previously approved immutable retry plans may retain old values.
Disabling/rotating does not stop Agent-local attempts or recall values already
delivered. Existing log redaction and immutable retry inputs remain unchanged.

Run SQLite suites and opt-in MySQL suites on disposable loopback infrastructure
using the existing ARCENTRA_RETRY_APPROVAL_MYSQL_DSN contract. Skipped MySQL tests are not
concurrency evidence. Acceptance credentials must be synthetic and ephemeral.
