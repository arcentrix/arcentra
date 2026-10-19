# Explicit GitLab MR snapshot polling

This optional trigger source is disabled until a project operator creates a baseline and explicitly enables a selection of existing pipelines. This page describes the GitLab merge-request adapter. GitHub.com and configured HTTPS Gitea pull-request adapters use the same explicit activation workflow; see [provider polling](provider-polling.md). Historical event replay, webhook deduplication and lossless event delivery are not provided.

## Before enabling

Apply the opt-in schema upgrade in [migration 009](../migrations/009_scm_polling_activation.md) with controllers stopped. Do not mix old and new controllers: older code's implicit polling/cursor behavior is no longer used. Existing project settings and legacy cursor values are preserved but do not enable the new stream.

The project must be active, enabled, configured for GitLab over HTTPS and MR triggers, and use its existing supported token/public authentication configuration. No credential is created by baseline or enable. The service validates the configured repository against the provider's stable numeric project identity. Redirects and incomplete, inconsistent or oversized snapshots fail closed. Self-managed GitLab is supported only through the project's configured HTTPS origin; this interface does not accept a per-request URL.

The authenticated operator needs current project read/update permission and current pipeline read/trigger permission for every selected pipeline. Permissions, project configuration and selected pipeline bindings are checked again inside durable transactions at capture, creation and admission. Existing role defaults are not expanded.

## Operator flow

1. Open the project's Pipelines page and review the GitLab polling panel.
2. Create a baseline. This inventories the currently visible MR revisions without creating runs. It is not enablement.
3. Select eligible existing pipelines and explicitly enable. Their configured repository, default branch and definition path are bound to this activation.
4. Polling observes subsequent visible changes. A complete changed-observation batch is persisted before definition loading. Only pipelines whose DSL event and target-branch filter match are considered for dispatch.
5. Refresh to inspect state, blockers and the last completed observation/target counts. Zero matching targets is a valid observed batch, not a successful build.
6. Disable to stop new admission. Disabling does not cancel or roll back an already admitted run.

A replacement baseline discards unadmitted observations only after explicit acknowledgement. An unresolved execution outcome cannot be erased with that acknowledgement. Configuration changes or permission denial invalidate the activation; restoring a grant does not silently re-enable it. Revisit the panel and establish a new explicit baseline/selection when appropriate.

## What a run means

An MR observation is a trigger, not proof that the MR's source was tested. Three revisions can differ:

- Observed source SHA: the revision reported by GitLab, with MR ID/IID, state, exact update/merge times and separate merge/squash revisions retained as evidence.
- Definition commit SHA: the exact configured pipeline-definition commit fetched from its configured branch. The expanded parsed definition is also digest-bound.
- Agent checkout SHA: the source actually checked out by each job, reported through the existing authenticated execution protocol.

The job's existing source configuration still determines what it builds. Do not describe these runs as MR validation unless that configuration actually builds the intended observed revision. Approval/deployment definitions requiring a human-bound execution plan are rejected for a matching polling event in this first version; nonmatching definitions do not dispatch.

## Durability and recovery

Scope includes provider origin, canonical repository, stable provider project ID and stream kind. Request identity is stable for a provider observation and pipeline, independent of processing time or activation generation. Frozen observation and target stages, immutable run evidence and admission receipts are stored separately from general project settings.

Only the caller that durably creates a new run may attempt its first Submit. A duplicate or restarted worker never resubmits an existing unresolved PENDING run. A definitive rejected admission is recorded as such; a network/database/commit uncertainty is not invented into a failure or success. An unresolved status is operator-visible and does not itself grant execution or invalidate a concurrently submitting creator. Original evidence must be preserved for explicit investigation; there is no automatic ambiguous-outcome replay.

Complete admitted/rejected receipts permit cursor acknowledgement. Interrupted or incomplete provider reads leave the cursor unchanged; bounded retry metadata respects rate limits. The controller processes streams with bounded scans and cycles. Snapshots represent observations, not a complete event history: rapid intermediate changes can be missed, and absence from one inventory does not authorize removal of deduplication history.

## Bounds and verification

The reader performs two matching bounded inventory passes and validates the stable project identity. The default cap is 2,000 MRs, 64 list requests across both passes, 2 MiB per response, 16 MiB total and 45 seconds per scan. Each persisted evidence document is additionally bounded to 1 MiB, and a dispatch batch to 2,000 targets. Oversized/inconsistent responses or evidence require operator investigation rather than truncation. Full-precision timestamps are stored as strings for cutoffs and observations; database datetime rounding cannot authorize historical replay.

Tests use synthetic GitLab HTTP/TLS fixtures and disposable databases. Local broker/storage fixtures must not be reported as real infrastructure acceptance. The hosted acceptance lane separately exercises actual MySQL, Kafka, Agent and S3-compatible storage using synthetic repositories and observations. No live customer GitLab credentials or repository data are used by these tests. Exact commit-specific CI and artifact evidence belongs in the delivered acceptance report.
