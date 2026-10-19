# Private Copilot conversations and review-only plans

This increment adds private persisted conversations. It does not complete Copilot
execution: plans in this increment cannot save code, rerun a job or release a
Deployment. Those operations still use their existing explicit application flows.

## Operator configuration

`copilot.history.enabled` defaults to `false`. Enable it explicitly and supply the
existing operator-managed `ARCENTRA_SECRET_ENCRYPTION_KEY` configuration. The
application does not generate or persist a replacement key. Missing or invalid
key configuration makes history unavailable. Retain the key needed to decrypt
existing records; changing it without a separate migration makes their content
unavailable. Conversation content is encrypted with identity-bound authenticated
encryption, separate from the existing Secret ciphertext format.

Apply migration 013 with old controllers stopped before running the paired
backend/frontend release. Do not mix old controllers with the new schema/contract.
The migration creates history tables; it does not import old prompts or run logs.

Model configuration and history availability are separate. Sending still requires
an allowed project, an operator-configured provider/model and explicit user
consent to the exact bounded redacted preview. Actual model endpoints and
credentials remain operator configuration. The disposable acceptance fixture
uses only its own loopback TLS synthetic model and synthetic credentials.

## Privacy, retention and authorization

- Each conversation is bound to one authenticated owner, project, pipeline and
  run. A platform administrator cannot read another person's private conversation.
- Reads and mutations recheck enabled identity, current project/pipeline access
  and archive state against primary storage. Revocation does not wait for a cached
  effective-actions refresh.
- Persisted questions and answers are redacted and encrypted. Raw selected
  context, assembled provider prompts, credentials and redaction dictionaries are
  not saved as conversation history. Content is rechecked and redacted on read.
- Creating a conversation explicitly acknowledges 30-day content retention.
  Individual turns expire independently; adding a turn does not renew old turn
  bodies. Listing, previewing or reviewing does not extend retention. Deletion
  fences the conversation and erases its encrypted content.
- No history is implicitly sent to a provider. Select at most six completed prior
  turns, within the 8 KiB history budget and the existing total input limit. The
  preview binds the selected ordered content before transmission.
- Missing, expired or no-longer-authorized evidence can make an old answer
  unavailable. The UI must not substitute stale browser content for it.
- A cancelled or interrupted provider request is not silently resent after a
  process restart. Durable request identity prevents a duplicate request from
  starting another provider call. An uncertain upstream result remains uncertain.

## Plans

Plans are immutable, bounded proposals with a separate review state. Their
encrypted payloads, including separately submitted patches, have a retention expiry
no later than 24 hours or the parent turn retention expiry. Earlier dependent-evidence
expiry makes content unavailable on access, without advancing ciphertext erasure.
Bounded maintenance erases expired ciphertext after the row retention deadline.
Reviewing never extends that deadline;
plan payloads are not copied into the 30-day conversation body history. Reviewing
means that the proposal was inspected; it does not authorize or perform execution.
The page displays `Not executed` and execution remains disabled.

A code patch is a single existing verified text-file proposal, checked in memory.
It cannot add/delete/rename files, change modes, follow symlinks or run Git/shell.
A new-run proposal labels the last saved definition revision explicitly. It does
not claim that revision is the current repository HEAD, and a new run is not a
resume of the original failed step. Retry-approval and Deployment proposals bind
existing authorized records without creating an approval or release as a preview.
Recheck the actual definition, inputs and permissions in the linked application
flow before taking any action.

Model text and selected history are untrusted text. Neither tool-like output,
embedded links nor instructions in logs can invoke application actions.

## Acceptance boundaries

Local tests exercise encryption/AAD, owner and project boundaries, live
revocation, request replay/cancellation, bounded retention and plan validation.
The dedicated hosted MySQL lane exercises both supported collations. The actual
browser fixture uses a real Control/Agent build run and a local TLS synthetic
model, creates two turns with an explicitly selected prior turn, reviews an inert
plan, restarts Control and independently compares primary API hashes. Its exact
published run results are supplied in the release acceptance report; this document
alone is not evidence that a candidate passed hosted acceptance.

No real external provider, customer source/log transmission, production credential
configuration or automated code/retry/deployment action is validated by that
fixture. The install-image and container lanes retain their existing scope; the
new model/history browser journey runs in the host acceptance profile.
