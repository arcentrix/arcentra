# Organization, members, and project settings

This milestone adds an authenticated Organizations directory and actual scoped
member operations. It does not interpret a historical project scope ID as a real
organization or automatically enroll existing users.

## Operator rollout

1. Back up and test the existing database copy, then explicitly run migration 010
   with `arcentra-cli migrate --conf /private/path/config.toml`. See
   [the migration guide](migrations/010_organization_scoped_members.md). Duplicate
   or collation-ambiguous historical IDs require operator review; migration does
   not repair or delete them.
2. Upgrade Control and the paired frontend together. Stop old controllers before
   rollout so they cannot keep accepting unversioned project writes or cached
   authorization decisions.
3. Log in with an existing authorized account. Organization creation requires
   platform-level `org:update` and `org:manage_member`. Creating an organization
   explicitly creates its owner association and scoped owner grant for that
   authenticated creator.
4. Add an **existing user ID** to the organization or project and choose an offered
   built-in scoped role. This is not an email invitation or account-creation flow.
   Role options are restricted by the actor's current permissions. The original
   owner/creator is protected; ownership transfer is not exposed.

A non-platform organization owner/admin can have `org:create_project` without
`project:manage_member`. Project creation does not automatically grant a project
role. Such a creator needs an independently authorized project administrator to
set up project members. A project-create permission alone is not complete build
or membership-management readiness.

## Project settings

The Settings page reads a current version token. Explicit edits can change the
display name, description, default branch, tags, language, framework and visibility
metadata. Repository identity, organization, credential fields, arbitrary JSON
configuration and enabled state are not editable through this form. Visibility
metadata does not grant access.

A version conflict requires a fresh read and an explicit new submission. The UI
must not silently replay a stale edit. Empty editable metadata can be deliberately
cleared where allowed; credentials are never populated, replaced or cleared by
these operations.

Archive blocks **new run admission** through the durable project/run lock order.
Already admitted jobs and their normal execution can finish; archive is neither
cancellation nor rollback. Explicit new manual release effects recheck current
active project authorization. Restore does not resume old unresolved runs, undo
external effects, or automatically reactivate polling. Existing durable terminal
receipts retain their original acknowledgment semantics.

Organization archive first requires all enabled active projects to be archived.
It retains historical data and grants. This first operation is soft archival but
has no organization restore UI/API; archived organizations leave the active
directory. No permanent data deletion occurs.

## Authorization and compatibility

- Each new scoped mutation reads primary, transaction-bound current authority and
  commits its version change, grant provenance and success audit together.
- Ordinary request permission checks refresh current primary authority so scoped
  additions/removals take effect across controllers. This does not claim every
  legacy endpoint now has atomic transaction-bound authorization.
- Removal disables only the grant owned by that membership operation. Other
  direct, inherited or team grants may remain. Legacy associations are not
  silently adopted as ownership of unrelated grants.
- Legacy unversioned project PUT, DELETE, enable/disable and statistics mutations
  return a conflict directing clients to versioned settings. They cannot bypass
  archive or restore restrictions.
- Project metadata responses omit raw environment/settings/build JSON and
  credentials. Display URLs remove userinfo, query and fragment; opaque or unsafe
  URLs are withheld. The stored runtime source configuration is unchanged.
- Organization project listings still require effective project read authority;
  `org:read` alone is not a blanket project-data grant.

## Acceptance

The new CI fixtures use existing disposable MySQL services, synthetic accounts and
scope IDs. They exercise role/membership CAS, revocation, protected owners,
case-sensitive and case-insensitive identities, migration, project archive versus
admission and audit rollback. The actual browser journey uses the real API to
create/edit an organization, add/change/remove scoped members, create/edit a
project, archive/restore it, reject organization archive while a project remains
active, and finally archive both. An independent API read checks final lifecycle
state after the browser ends.

Local unit tests and simulated transport tests are not substitutes for the
exact-commit MySQL/browser CI result. Production account grants, external
notification sends and real credentials are never configured by these fixtures.
