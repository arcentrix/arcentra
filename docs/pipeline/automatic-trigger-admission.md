# Automatic-trigger admission

Cron and verified SCM webhook runs use a durable admission boundary before an
execution coordinator is created. This boundary concerns accepting one persisted
run; it is not an execution lease, cross-controller takeover, or an exactly-once
side-effect guarantee.

## Outcomes

- A newly created automatic run starts as `PENDING`.
- The submitted trigger type must agree with the persisted record. Missing
  records or a failed identity read do not authorize scheduling; relabeling an
  automatic run as manual or unknown cannot bypass admission.
- The Process checks an already registered coordinator before validating a
  repeated submission. An invalid duplicate cannot reject work already accepted
  by that Process.
- A deterministic pre-admission validation, initialization or shutdown rejection
  can transition the original automatic record to `FAILED` with an end time.
  It creates no job or step. Manual submission persistence is unchanged.
- Positive admission and rejection use the same locked database transition.
  The stored run, pipeline, request, trigger, definition revision/path, source
  fields must match exactly; string-valued environment maps must contain the
  same case-sensitive keys and values (JSON spacing/key order may differ).
  Approval/deployment contexts,
  existing timestamps, execution records or a non-pending state prevent this
  transition. A confirmed positive transaction sets `RUNNING` before scheduling.
- `RUNNING` at this boundary means accepted and potentially waiting for execution
  capacity. It does not prove that an Agent or command has started.
- A duplicate, failed database operation or uncertain transaction result is not
  permission to schedule, replace a terminal state, or retry execution. A typed
  pre-admission rejection describes that call only; persistence must succeed
  independently before a durable rejection can be claimed.

A controller crash after admission can therefore leave an interrupted `RUNNING`
record. A crash between creation and admission can leave `PENDING`. Neither is
resubmitted automatically. Inspect surviving receipts and external side effects;
use the existing [explicit offline recovery procedure](implementation.md#interrupted-execution-recovery-explicit-offline-only)
only after all controllers, Agents and external work are stopped. Recovery keeps
terminal records and does not perform a rollback. A new manual run is a separate
operator decision.

## Approval and compatibility boundaries

Automatic triggers cannot supply the authenticated manual consent required by
Kubernetes deployment or exact-content retry approval. Such definitions are
refused before automatic run creation. Cron also rechecks this restriction when
the scheduled callback fires, so editing a definition cannot bypass the guard.

The existing request-ID uniqueness constraint is retained. This change does not
backfill or mutate old pending records, add a startup migration, or change the
manual-trigger API. Its concurrency guarantee requires all controllers that can
submit these runs to use the new admission boundary; mixed old/new controller
versions do not share that guarantee. Stop the old controllers during upgrade.

## Remaining event-delivery scope

Enabled/active checks at the trigger entry are snapshots, not transactional
revocation fences across repository loading. Existing registered cron callbacks
are refreshed by schedule synchronization; this admission boundary does not
prove that an edited definition still has the same registered cron expression.


This admission change does not repair SCM polling dispatch. The old polling
cursor represents observation, not durable execution. It must not be treated as
proof that events ran or as authority for automatic historical replay.

The existing webhook response reports parsed events; it is not a durable
execution-acceptance receipt for every matched pipeline. Entry-point dispatch
errors are logged, and downstream provider retry/acknowledgement semantics remain
part of the separate event-delivery work.

Webhook request IDs still follow the existing normalized event/time scheme and
cron IDs use the pipeline plus minute slot. Durable admission prevents repeated
execution of the same persisted run; it does not establish lossless provider
history or universal webhook/poll cross-channel deduplication. Stable event
identity, pagination/completeness, cursor storage and explicit polling cutover
are separate work.

## Verification

Ordinary tests exercise actual Process admission using SQLite, a local Git
repository, signed synthetic webhook events and manually invoked cron callbacks.
They cover deterministic rejection, duplicate submissions and durable state
conflicts. Hosted real-stack acceptance exercises the same webhook entry with
its disposable MySQL, Kafka, authenticated Agent and S3-compatible service,
including competing admission/rejection and existing artifact/receipt checks.
Actual hosted results are recorded for the exact reviewed commit in the delivery
acceptance record; local skipped opt-in tests are not hosted acceptance evidence.
