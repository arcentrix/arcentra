# 015: lossless audit actions

This operator migration follows 014. Run the existing
`arcentra-cli migrate --conf <reviewed-config>` against the intended database
before deploying the new application. Control startup does not migrate.

## Reason and data contract

The permission dictionary already stores `Permission.Action` as LONGTEXT.
Canonical actions can therefore exceed the former `audit_log.action VARCHAR(32)`
column. MySQL rejected those audit inserts, although the authorization outcome was
unchanged. This migration preserves the complete action in LONGTEXT, NOT NULL,
with the fixed nonunique `idx_audit_log_action (action(191))` prefix index. The
prefix bounds only the index key; it never truncates the stored or returned
value. Audit List continues to compare the complete action using SQL equality,
with the existing column collation semantics. Values sharing the same 191-character
prefix remain distinct full values and are filtered by their complete action.

This does not change permission grants, authentication, action canonicalization,
or the existing handling of an audit write failure. No audit value is reconstructed
for previously rejected inserts. Existing rows are not rewritten by application
code, and there is no truncation, hashing, normalization or new length limit.

## Operator procedure

1. Back up the database and verify restoration. Record `SHOW CREATE TABLE
   audit_log`, the existing column charset/collation, row count and representative
   full values. Schedule a DDL lock window and sufficient disk space.
2. Stop Control writers and concurrent migration processes. Mixed-version writers
   and concurrent schema edits are not supported. Verify the selected database
   and reviewed configuration before invoking the CLI.
3. The migration runs every existing schema preflight before any DDL. For MySQL 8
   it accepts an absent audit table, or an InnoDB table with exactly either:
   - legacy `action VARCHAR(32) NOT NULL`, no default, and the visible ascending
     nonunique BTREE `idx_audit_log_action` on the full single action column;
   - completed `action LONGTEXT NOT NULL`, no default, with the same single-column
     index and an exact 191-character prefix.
   The action column must not have a generated expression, extra attributes or a
   custom comment. Additional action indexes, missing/renamed/composite/unique/
   invisible or functional indexes, other column types or widths, and other prefix lengths are
   unknown structures and fail before DDL. Existing LONGTEXT with a different
   index is deliberately not silently repaired.
4. For the known legacy shape, one MySQL atomic ALTER widens the column, drops
   the former full index and adds the prefix index together. It explicitly
   retains the column's actual charset and collation, even when those differ from
   table defaults. Only then may general AutoMigrate run. Fresh installation uses
   the same LONGTEXT and prefix index directly. Reapplying the completed migration
   does not alter the action column or index.
5. Inspect resulting column/index metadata, retained rows, and complete equality
   filtering. Run the required disposable MySQL acceptance under both configured
   collations, including the real denied and sensitive-success audit assertions.
   SQLite checks establish logical behavior only, not MySQL DDL or prefix indexes.

Unknown schemas require operator review of the actual definition and backup.
Do not drop indexes, shrink columns, change collations, or truncate values just
to make the preflight pass. Plan a separate reviewed, lossless reconciliation
of that schema, then rerun the explicit migration. If DDL reports an error or its
commit acknowledgment is uncertain, inspect the actual schema before retrying.
The action ALTER is atomic on supported MySQL 8/InnoDB; the complete multi-table
migration is not one transaction and later failures require inspection.

## Rollback

Application rollback retains the expanded action column and prefix index. Do not
run an old AutoMigrate/CLI that attempts to shrink LONGTEXT back to VARCHAR(32).
This migration provides no automatic database downgrade.

Any separately reviewed downgrade must stop writers and first prove
`SELECT COUNT(*) FROM audit_log WHERE CHAR_LENGTH(action) > 32` is zero, as well
as verify the original charset/collation and index requirements. A nonzero result
forbids shrinking: retain the expanded schema or use an explicitly approved
backup restoration plan that accounts for all subsequent audit records. Never
truncate audit data to satisfy a rollback. Checking length while writers remain
active is not a safe downgrade precondition.
