# Explicit organization and scoped-member upgrade

Apply only with the existing operator-run command, after taking a backup and
validating a representative copy:

```sh
arcentra-cli migrate --conf /private/path/config.toml
```

Ordinary server startup never synchronizes this schema. Pause administrative
writes during migration; MySQL DDL is not an all-or-nothing transaction. A failed
migration needs operator inspection before retrying, not automatic repair.

## Preflight and additive columns

Before any schema mutation, the command rejects duplicate organization IDs,
organization names, project IDs, or organization/project user associations under
the target database's own collation. Empty, null and over-64-byte keys are also
rejected. No rows are merged, renamed, deleted or silently adopted. Review and
resolve ambiguous legacy data separately before running migration again.

- organization: unique bounded org_id/name indexes; version defaults to 1
- organization_member: unique (org_id,user_id); version defaults to 1 and
  managed_grant_id/managed_role_id default to empty
- project: unique bounded project_id; settings_version defaults to 1
- project_member: version defaults to 1; managed_grant_id/managed_role_id default to empty;
  is_enabled defaults to 1 for existing associations

The empty managed_grant_id is intentional. Old associations do not acquire new
roles, and old role grants are not claimed by the new membership workflow.
Existing scope-ID-only projects stay scope-ID-only: migration creates no parent
organization, member association, grant, invitation or subscription. Existing
registration remains unprivileged. The command retains its existing builtin
role/permission seeding and does not create per-user grants.

## Product and authorization boundaries

Organization creation requires both org:update and org:manage_member at platform
scope. This explicit creation transaction associates the authenticated creator
with exactly the new organization and its builtin org_owner grant. Names are
immutable after creation. Owners and admins have different owner-assignment
capabilities even where their seeded ordinary permissions overlap.

All new metadata/member writes reconstruct the actor, enabled team membership,
roles, bindings and nonexpired grants from locking primary-database reads inside
the transaction. CAS versions, success audit and writes commit together. Builtin
role assignment verifies the exact seeded permission definitions; edited or
custom roles are rejected rather than being used as an escalation shortcut.
Every assigned role action must also be allowed to the actor in that exact scope;
manage_member alone does not authorize granting a more powerful builtin role.
The protected original organization owner and project creator cannot be removed
or demoted by these routes. There is no ownership-transfer operation.

A member removal tombstones its association and revokes only the role_grant
identified by that association's managed_grant_id. Other direct, team and
inherited access remains independent. Re-adding increments the existing version,
so an old request cannot modify a new incarnation of the same association.

Organization archival is soft and irreversible through this milestone's API.
It refuses organizations with active enabled projects; archive those projects
first. Historical projects, runs, members and grants are retained. New scoped
member writes and project creation reject inactive real organizations, while
legacy no-row scopes remain supported. Archival is not a broad grant-revocation
or runtime-cancellation operation.

Ordinary permission/collection middleware refreshes current primary authority per
request so member grants and revocations work across control-plane instances.
That request check alone is not a claim that every historical endpoint has
transactional write authorization. New organization/member, project-creation and
project-settings operations provide their own transactional checks. Organization
list metadata and capability flags are an advisory primary snapshot; writes
recheck authority and versions. Listing does not hold domain range locks while
loading the permission catalog. MySQL deadlocks in other concurrent transaction
interleavings fail closed and roll back; no automatic retry or deadlock-free
guarantee is provided.

## Rollout and verification

Deploy the schema before the updated API and UI. Exercise SQLite tests and the
opt-in MySQL concurrent admission/CAS/revocation tests on disposable local
infrastructure. A skipped MySQL suite is not concurrency proof. On rollback retain
all new columns, tombstones, grant provenance and audits; do not reset versions
or drop indexes to make old requests succeed.
