# Durable deployment approval schema upgrade

This is an operator-run additive schema upgrade through the existing `arcentra-cli migrate --conf /path/to/config.toml` command. Starting the server does not migrate production databases. Do not run the historical destructive `002_authz.sql` as an upgrade for this feature.

1. Keep deployment and per-target pipeline opt-in disabled. Back up the database and test the migration against a representative copy. Preserve historical approval decisions and unresolved deployment reservations.
2. Inspect existing `approval_decision` data for duplicate `(approval_id, user_id)` pairs. The prior index was nonunique; the new `uk_approval_decision_user` unique index cannot be installed if duplicate history exists. Resolve such data through an explicitly reviewed operator procedure that preserves audit evidence. This release never silently drops duplicate votes or chooses a winner.
3. Run the existing explicit schema migration with this version's models. Required additions are:
   - `deployment_release`: nullable unique `approval_id`, `approval_evidence_hash`, JSON `approval_target`
   - `approval_request`: nullable unique `deployment_release_id`, `evidence_hash`, `version` default 1; `environment` widened to 64 characters
   - `approval_decision`: unique `(approval_id, user_id)` constraint
4. Verify the nullable uniqueness constraints allow multiple legacy records with NULL links and prevent duplicate non-NULL release/approval linkage. Verify timestamp precision preserves milliseconds for newly created `expires_at` values, as used by the approval evidence digest. Do not backfill fake linkage or evidence on legacy rows.
5. Confirm candidate/quorum validation fails closed for legacy rows without trustworthy candidate snapshots. Generic approval policy records and old in-memory plugin gates do not authorize deployment.
6. Review an exact target's trusted `pipelineApproval` configuration and candidate permissions, then enable deployment and per-target pipeline opt-in only if intended. Policy changes intentionally invalidate earlier reviewed target fingerprints and gate evidence.

The migration is included in the repository model/schema registration and exercised with fresh disposable SQLite databases. Production MySQL index creation, row locks, timestamp/JSON storage, database size and migration duration remain operator validation responsibilities. Keep backups; do not downgrade by dropping approval evidence or releasing unresolved reservations.
