# Offline terminal-conflict quarantine operations

## What quarantine means

An Agent terminal report can conflict with an already immutable controller result. A controller-requested cancellation may win a race with a late successful command, for example. The command may have had real external effects even though the controller cannot accept its result.

The Agent automatically quarantines only a structured, non-retryable controller conflict that exactly matches the original report bytes, execution claim, Agent, run, and validated execution scope, received over a verified TLS connection. A quarantine is **rejection evidence, not an accepted receipt or proof of successful execution**. The controller's terminal state and artifacts remain unchanged. The journal envelope is atomically rewritten to add quarantine/review metadata; the original serialized terminal payload, claim, and original receipt checksum remain unchanged. The entire file is not byte-identical after annotation. The Agent keeps the original receipt and its immutable evidence, stops replaying that receipt, and may continue unrelated work after the quarantine is durably synced. A dependent job receipt is also quarantined with an explicit reference to its quarantined step.

The Agent independently matches only fields present in the original terminal wire request, plus the original execution claim and full payload digest. The V7 terminal wire request does not carry pipeline lineage, attempt index, step index, or predecessor. Pipeline/job scope, attempt/index/predecessor, and claim-source provenance in rejection evidence are controller-validated attestations delivered over authenticated TLS; the Agent does not independently reconstruct that database lineage.

The worker logs only aggregate quarantined and unreviewed counts at background replay startup and when counts change, so retained conflicts remain visible after restart. This stage has no central quarantine dashboard or controller review API.

Ordinary network failures, authentication errors, generic or malformed gRPC errors, mismatched evidence, and unverified/plaintext transport remain pending and block new commands. An older controller that does not supply the protocol also remains fail-closed. Never infer permanent conflict from an error message or status code alone.

## Prerequisites

- Upgrade controller and Agent together. This quarantine stage adds no database migration. Installations missing the earlier terminal-receipt schema must apply that existing migration as described in [Durable Agent terminal reports](agent-terminal-reports.md). An older controller with a newer Agent, or older Agent with a newer controller, keeps conflicts pending and blocking until the compatible upgrade is in place.
- Retain the original configured `agent.id`, per-Agent `grpc.token`, normalized `grpc.serverAddr`, and durable workspace. Dynamic enrollment is not credential recovery.
- To enable automatic quarantine, configure the Agent to use an already available trusted TLS endpoint. Use the optional controller gRPC TLS listener with operator-supplied files, or an existing trusted TLS proxy that reaches the intended controller. See `deployment-readiness.md`. Protect any proxy-to-controller plaintext connection and preserve the existing Agent authentication checks. No server certificates are provisioned automatically.

```toml
[grpc]
serverAddr = "controller.example.internal:9443"
# Keep the original Agent token in the existing protected configuration.

[grpc.tls]
enabled = true
caFile = "/private/arcentra/controller-ca.pem"
serverName = "controller.example.internal"
```

`caFile` is optional when the endpoint chains to the host's system trust roots. `serverName` selects the certificate hostname when necessary; it never disables verification. There is no insecure-skip-verify setting or automatic plaintext fallback. A `https://` controller address requires TLS enabled, and `http://` requires TLS disabled. Use the same normalized logical controller address bound to the journal; changing the endpoint spelling/address of an existing installation requires an explicit identity/state migration plan. Do not alter the `identity` file to force a match.

Plaintext configurations retain their prior pending-and-blocking behavior for conflicts. Enabling TLS alone does not grant successful receipt status, remove a journal entry, or change a terminal run.

## Stop and preserve first

1. Stop the Agent and confirm the process has exited. All offline commands require the **same exclusive journal lock** as the running Agent.
2. Preserve the entire journal and relevant controller state using your approved backup procedure. Include the original receipt, quarantine evidence, and any local review record. Do not copy a partially written live journal and assume it is consistent.
3. Run the commands as the journal's owning OS user. Directories must already have mode `0700`; files must already have mode `0600`. Symlinks, hard-linked files, changed ownership, malformed data, unknown entries, identity mismatch, and unavailable locking fail closed.
4. Select the actual `.terminal-report-outbox` directory, the original Agent ID, and the normalized controller address explicitly. These commands never read Agent configuration, initialize the Agent, register an identity, create credentials, or contact any network service. Do not pass a token or registration token on the command line.

The commands open existing state only. A mistyped or missing directory, missing identity/lock, or wrong binding cannot create a replacement journal. List and inspect do not rewrite records, change permissions, or clean temporary files. An abandoned `.pending` file prevents offline opening and is left untouched; let the Agent's normal recovery validate/recover it using the original identity before attempting offline review again. Do not delete it yourself.

## List safe summaries

```sh
arcentra-agent terminal-quarantine list \
  --directory /srv/arcentra/workspace/.terminal-report-outbox \
  --agent-id ORIGINAL_AGENT_ID \
  --controller controller.example.internal:9443
```

The single JSON object contains `records`, `total`, `offset`, and, when another page exists, `nextOffset`. Records are sorted by kind and run ID. The default page has up to 100 summaries; `--limit` accepts 1–1000 and `--offset` accepts 0–10000. Use the returned `nextOffset` while the Agent remains stopped. The journal is bounded to 10,000 total records and the CLI caps one JSON output at 8 MiB. Empty results use `records: []`; they mean no quarantined receipts, not necessarily an empty pending outbox or successful controller acceptance.

Each summary includes the run kind/ID, original claim ID, storage identity binding, original receipt checksum, requested terminal status, quarantine reason/time/checksum, and available typed rejection scope or dependency evidence. IDs and digests remain operationally sensitive; share summaries only with authorized operators. The summary deliberately excludes protobuf payloads, command output, artifact metadata, transport credentials, and free-text remote errors.

## Inspect one record

```sh
arcentra-agent terminal-quarantine inspect \
  --directory /srv/arcentra/workspace/.terminal-report-outbox \
  --agent-id ORIGINAL_AGENT_ID \
  --controller controller.example.internal:9443 \
  --kind step \
  --run-id ORIGINAL_STEP_RUN_ID
```

Use `--kind job` for a job record. This returns one safe summary as JSON. Rejection `claimSource` distinguishes a stored `execution_claim`, a never-started child bound to a `cancelled_parent_claim`, and a never-started child bound to a `terminal_parent_claim` after timeout/recovery. Parent-derived evidence requires the exact terminal parent claim, derived step-attempt identity, and validated current scope. These sources do not assert that the child command ran. Pending non-quarantined receipts and nonexistent selectors are rejected. Correlate the summary with the controller's immutable run, original execution claim, and separately verified external effects. Inspect any dependency referenced by a quarantined job. A matching local checksum detects corruption; it is not a signature or a security boundary against an account that can rewrite the journal.

## Record an operator review

Only record a review after the investigation that the selected disposition describes:

- `investigated`: the local operator reports that the conflict was investigated
- `verified_external_effects`: the local operator reports that the external effects were checked

```sh
arcentra-agent terminal-quarantine mark-reviewed \
  --directory /srv/arcentra/workspace/.terminal-report-outbox \
  --agent-id ORIGINAL_AGENT_ID \
  --controller controller.example.internal:9443 \
  --kind step \
  --run-id ORIGINAL_STEP_RUN_ID \
  --disposition investigated \
  --confirm-reviewed
```

The explicit confirmation flag records your intent. The operation atomically appends one local review event with UTC time, fixed disposition, and checksums. It returns the safe summary including `reviews`. Repeating the same disposition is idempotent and preserves the first review time; changing it is rejected. Keep any broader incident notes in the organization's authorized incident system, not in arbitrary journal edits.

A review is a **self-reported local OS-operator action**. It does not authenticate a product user, prove which human performed it, independently verify external effects, or confirm controller acceptance. Review does not unblock a pending non-quarantined receipt, alter a quarantine decision, remove quota usage, or authorize a retry.

There is intentionally no command to unquarantine, replay, retry, delete, discard, or rewrite a terminal result. Both reviewed and unreviewed quarantine records remain durable tombstones for the original execution identity. Never manually delete or rewrite them, replace claims, re-enroll to bypass the conflict, or modify controller terminal rows to make the late receipt appear accepted. Any new authorized execution uses the normal execution policy and a fresh step-attempt identity; operator review itself does not create one.

## Failures and restart behavior

- Exit `0` means the local command completed; it does not mean the controller accepted the terminal result. Exit `2` means invalid CLI arguments; exit `1` means an operation or output failure.
- Errors intentionally omit caller-supplied paths, credentials, payloads, and underlying free-text errors. Verify the explicit selectors, stopped Agent, owner/modes, and journal integrity when opening fails.
- SIGINT/SIGTERM cancel the command context; operations also have a 30-second context deadline. Output is bounded. If a review was interrupted, storage sync failed, or result output failed, re-open and inspect the selected record before repeating it. A durable review may already have been written.
- After the Agent restarts, committed quarantine and review evidence are revalidated. Quarantined receipts are not sent again and do not permit the same command identity to run again.
- Quarantined records count toward the existing byte/record/record-size limits. Exhaustion, corruption, missing dependency evidence, unsupported filesystem operations, or uncertain sync outcomes fence new work. Review never reclaims capacity; stop and preserve state, then escalate for an explicit storage/reconciliation plan.
- This is local-disk/process-restart recovery. It does not provide command exactly-once semantics, restore lost disks, undo external effects, authenticate a product reviewer, or transfer receipts to a newly enrolled Agent.
