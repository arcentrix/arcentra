# Copilot bound retry-approval execution

This first execution slice is explicitly disabled by default through
`copilot.execution.retryApprovalEnabled`. It only submits an approve vote to an
existing exact-content retry-approval gate. It does not create a run, edit commands,
retry a terminal job manually, resume execution, create credentials, or bypass
ordinary approval/runtime authorization.

## Prepare, review, confirm

1. An authenticated owner reviews their private `retry_approval_review` plan.
2. Explicit preparation validates the reviewed plan and current retry evidence,
   then creates a detached intent with fixed owner, project, pipeline, run, job,
   approval, reviewed plan/version/digest, evidence digests, request key, and stable
   decision ID. No prompt, source, command, environment or plan body is copied.
3. The user reviews the bound intent and its expiry, then explicitly confirms it.
   Confirmation revalidates the exact current domain binding and current authority.
   The existing retry-approval domain transaction inserts the vote and changes the
   intent to `submitted` atomically. Lost acknowledgements reconcile against this
   durable primary receipt; retries must not produce another vote.

The approval covers the existing waiting job's complete finite retry budget before
its first command starts. A submitted receipt means one vote was recorded. It does
not mean quorum was reached, dispatch occurred, or a command ran. The ordinary
approval policy, requester/voter permissions, source/artifact permissions, expiry,
Agent admission and runtime gates continue to apply. Every actual retry remains
subject to those existing controls.

This slice validates a complete bounded run context of at most 200 steps when
checking that bound evidence contains no current secret values. Larger or incomplete
contexts fail closed; preparation is not available for them in this slice.

## Deletion and cancellation

An explicitly prepared intent is detached from history. Deleting a conversation,
turn or plan removes private content according to history policy, but does not
withdraw a prepared intent or an immutable vote. This boundary must be visible
before preparation and deletion. The detached record contains identifiers/digests
only and remains subject to fresh authorization when read or acted upon.

Intent cancellation is a compare-and-swap from `prepared` only. It prevents future
submission if it wins the race with confirmation. Once submission wins, the
approval vote is immutable and cannot be withdrawn by deleting history or
cancelling an intent. Cancelling an actual job is a separate domain operation with
its own permission and effects; an intent cancel is not process termination.

Submission eligibility expires at a fixed bounded deadline. Expiry does not erase
the request/decision receipt. Audit and deduplication identifiers are retained;
this slice has no automatic ledger purge. Owner capacity bounds prevent unbounded
admission, and reaching the bound fails closed. History bodies have their separate
retention policy. Retaining a receipt is never fresh permission to execute.

## Operator rollout and acceptance

Apply [migration 014](migrations/014_copilot_execution_intent.md) explicitly with old
controllers stopped. Leave the flag off until compatible Control/Agent versions,
ordinary retry policy and all pre-existing operator gates are configured and
validated. No feature-specific credential or authorization grant is created.

Acceptance requires more than a submitted vote or a successful first attempt:

- Unauthorized, cross-owner, cross-project, stale/changed evidence, expired,
  wrong-version and wrong-digest requests fail closed without creating a vote.
- Repeated preparation/confirmation and lost acknowledgement reconcile to one
  request and stable decision. Concurrent confirmation/cancel has one winner.
- History deletion preserves detached intent and immutable receipt semantics.
- Real MySQL validates primary transactions, unique keys, concurrency and
  database-collation identity handling; SQLite is not substitute evidence.
- A real gated Agent job is blocked before its first command. After the necessary
  approval votes, its first attempt actually fails, the authorized retry actually
  runs within the original finite budget, and terminal evidence confirms the
  outcome. Merely testing a successful first attempt is not retry acceptance.

The default remains off if any acceptance dependency is unavailable. Do not claim
production readiness from unit tests, mocked execution, or a skipped MySQL suite.
