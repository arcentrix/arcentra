# Explicit SCM polling activation schema upgrade

Apply this additive upgrade through the existing operator-run command before
rolling out the updated control plane:

```sh
arcentra-cli migrate --conf /private/path/config.toml
```

Back up the intended database and test the migration against a representative
copy first. Ordinary server startup does not migrate the database. Keep polling
dispatch disabled throughout the upgrade; migration is not permission to execute
pipelines.

## Additions and preserved evidence

- `scm_polling_stream` stores the explicit baseline, exact selected pipelines,
  continuing authorizer, revision/generation fences, cursor and pending batch
- `pipeline_run.polling_context` and `pipeline_run.polling_context_digest` preserve
  immutable polling provenance only for the dedicated trusted creation path
- Existing `Project.Settings` bytes and legacy cursor fields are not converted
  into a baseline or activation
- Existing runs and unresolved pending batches are not replayed or discarded
- Migration creates no enabled stream, baseline, provider request or run

The existing CLI also performs its existing authorization seeding. This change
adds no polling-specific role or permission grants and does not grant organization
administrators pipeline-trigger authority.

## Rollout ordering and fail-closed mutations

The updated MySQL project/pipeline mutation paths require the polling table for
actual relevant configuration changes, even when polling dispatch is disabled.
If that table is missing or unreadable, the entire mutation rolls back. Apply the
explicit migration before deploying these paths; do not work around a schema
error by disabling invalidation.

Project repository, authentication, trigger-mode and lifecycle changes, including
the dedicated enable/disable/delete routes, atomically invalidate existing
polling state. Pipeline repository, definition branch/path, project ownership,
enabled and deleted changes do likewise. Ownership moves conservatively
invalidate streams of both projects. A return to the old configuration does not
reactivate polling: each actual change advances the revision and activation
generation, leaves the stream paused, and retains its baseline, selection,
cursor and pending-batch evidence. A new reviewed baseline and explicit enable
action are required.

Ordinary Settings/statistics writes, unchanged relevant values and new pipeline
creation do not activate or extend polling selection. Only legacy SQLite tests
may omit the polling table; that compatibility path is not a supported MySQL
rollout sequence.

## Validation boundaries

The focused SQLite tests exercise mutation coverage, sticky invalidation,
retained evidence, atomic rollback and declared GORM lock ordering. They do not
prove MySQL row-locking or concurrent revocation behavior. Run the dedicated
MySQL integration coverage on disposable infrastructure, and validate existing-
data migration, backup/restore and index/storage behavior before production
rollout. Preserve polling evidence during rollback; do not drop state or relabel
old runs to make an unresolved observation appear safely replayable.

## Bounded state and dispatch receipts

One JSON document is limited to 1 MiB; the complete MR inventory to 2,000 entries;
the explicit selection to 100 pipelines; and a frozen batch to 2,000 targets.
An exceeded bound is an incomplete scan, never silently truncated success. Every
run admission/rejection receipt is recorded in the same stream transaction as its
run-state transition. The frozen batch revision remains stable while the stream
CAS revision advances. A failed acknowledgement retains all observations and
receipts. Existing PENDING runs are never resubmitted. Unknown creation or claim
commit outcomes never permit scheduling, even if a later read sees the write.

Provider rate limits and transient failures store a sanitized reason and a
separate next-poll timestamp (one minute to 24 hours), without advancing the
cursor or creating a new activation. Successful capture/acknowledgement clears
backoff. Last-completed observation and target counts are written only after a
complete acknowledged batch, including a valid zero-target batch.

The pending state has two bounded stages in the same row. `pending_observation_json`
preserves normalized provider observations before definition loading. A matching
execution batch atomically replaces that stage only after every frozen definition
is available. A definition failure pauses without erasing observations; rebaseline
requires explicit discard acknowledgement for either stage. Existing ambiguous
PENDING or untyped terminal runs cannot be concealed by rebaseline.

`baseline_cutoff` retains RFC3339Nano text independently of MySQL datetime precision.
The baseline comparison must use this exact value, not the millisecond-rounded
presentation timestamps. MR ID/IID, state, exact updated/merged times, source SHA,
merge SHA and squash SHA remain distinct immutable observation provenance.

A confirmed unresolved duplicate sets non-authoritative `recovery_required`
metadata without revoking a concurrently creating worker's generation. Successful
admission clears it. `last_attempt_at` supports oldest-attempt-first fair polling;
each cycle loads at most 32 due streams, including their bounded JSON evidence.
Recording an attempt before processing moves that stream behind older attempts,
so streams beyond the initial cap become eligible in later cycles without granting
execution authority. Pipeline-only edits invalidate streams only
when that pipeline is actually in their verified selection, preventing unrelated
pipeline editors from disabling a project's automation.
