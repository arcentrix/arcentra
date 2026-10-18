# Disposable personal Inbox acceptance

This supplements the existing fresh-installation and real Agent build journey.
It does not seed notifications, substitute fake successful jobs, or send external
messages. The first slice delivers notifications inside Arcentra only.

## Evidence boundary

The existing fixture must first prove a real compiler failure with no artifacts,
then a corrected-source test/build, authenticated artifact download, digest and
program behavior. Only those exact failed/successful run IDs, pinned project and
pipeline IDs, and the authenticated requester may be supplied to Inbox checks.

`ARCENTRA_ACCEPTANCE_INBOX_JOURNEY=1` explicitly opts the disposable fixture into:

1. Wait at most 45 seconds for the authoritative projector to deliver both real
   terminal facts. A duplicate, different scope, partial page, or previously read
   result fails; no row is inserted by the fixture.
2. Start a fresh real Chromium context, log in through the production API, match
   the returned profile's immutable user ID, and show those exact Inbox rows.
3. Mark only the successful run's delivery read through its visible button.
   The failed run stays unread. Reload and require identical delivery metadata.
4. Stop and restart the same disposable Control using its existing lifecycle.
   Keep the existing Agent and database. A second real browser login verifies
   the exact delivery IDs, timestamps and read state after the restart.
5. After each browser closes, independently read the primary authenticated API.
   A separate read-only fixture identity must receive 404 when attempting to
   mark either owner's delivery read.

Each browser phase is bounded to 100 seconds inside the existing job budget;
its parent waits at most 120 seconds. The fixed reviewed Node entrypoint is
required. Preflight-derived child environments omit build-only synthetic secret
values. No database mutation, new listener, external provider, or host security
change is part of this check.

## Output and failure handling

`inbox-read/summary.json` and `inbox-resume/summary.json` retain the existing strict
zero unexpected console/network/page/privacy-error checks. Receipts contain only
allowlisted synthetic source IDs, enum statuses and delivery/read timestamps;
no names, messages, logs, credential material or raw API payloads are accepted.
Only a fully passed browser writes its receipt. Screenshots show the Inbox page,
never the login form. Raw browser diagnostics and request headers are not upload
inputs.

`inbox-verification.json` is finalized only after the full fixture and cleanup
succeed. It contains boolean guarantees, phase enums and receipt hashes, not the
receipt IDs. Image cleanup failure removes this success file. An incomplete or
failed phase cannot become passing evidence through a later retry.

Approval delivery is verified separately through genuine service request/decision
paths in the MySQL/runtime suite. This browser run does not prove the approval
UI path. External email/chat delivery is outside this first in-app slice.

## Current validation status

Local validator and orchestration unit tests are present. Hosted real Inbox
browser/MySQL/runtime acceptance is pending; no result is implied by this file.

## Conservative admission boundary

A manual run whose creation commit outcome was uncertain is never automatically
submitted again merely because an identical persisted row can be found. Returning
that existing run ID is not proof of execution. Such a run may remain pending;
pre-admission or unresolved records intentionally do not manufacture terminal
Inbox notifications. Existing explicit offline recovery remains the operator
path. This slice introduces no transparent task replay.
