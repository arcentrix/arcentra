# Artifact lifecycle and offline retention cleanup

## Operator prerequisites

Artifact registration uses the `project_artifact` table and the persisted project → pipeline → run → job → step ownership chain. Apply the additive schema migration deliberately against the intended database before enabling this feature:

```sh
arcentra-cli migrate --conf /private/arcentra/config.toml
```

Back up an existing database first. The migration is an operator action; starting the API, listing/downloading artifacts, and running `prune-artifacts` never creates or migrates schema. A missing table is a deployment error, not permission to initialize it automatically. Migration also applies the repository's existing idempotent authorization seeds; it does not create an administrator or grant a user access.

For control-plane local artifacts, set a stable, dedicated, persistent root:

```sh
export ARCENTRA_ARTIFACT_ROOT=/srv/arcentra/artifacts
```

Use the same absolute path and mounted filesystem for the controller, download service and cleanup CLI. The fallback is `data/artifacts`, relative to the process working directory, so an explicit absolute path is strongly preferred. Keep this directory separate from source workspaces and application configuration. Its directory components must not be symlinks. Provision appropriate ownership, capacity and backups. Before applying cleanup, positively verify that the intended volume is mounted and healthy; an empty replacement mount cannot be distinguished from missing artifact files solely by its path.

## What is registered

- Successful local `artifacts:upload` execution copies selected regular files into managed storage and then registers their exact project/run/job/step identity, relative name, size, SHA-256 and optional expiry. Registration follows durable publication; identical retries are idempotent and cannot rebind an artifact ID to different content.
- Local storage retains nested names under project/run/job/step directories. Source outputs and the intermediate job workspace remain separate. Artifact pruning removes only eligible registered managed copies, never source outputs, build workspaces or their staging directories.
- Agent uploads register the original storage ID, provider and location fingerprint together with the exact content-bound object key. Later downloads and deletion resolve that stored binding. Changing the default bucket does not redirect old artifacts. Endpoint/bucket/region/base-path/TLS changes make the old binding unavailable; credential rotation for the same location does not change its fingerprint.
- An object uploaded before registration failed can be orphaned. This command does not scan buckets, infer ownership from filenames or delete unregistered objects. Reconcile such objects separately using a reviewed ownership inventory.
- Legacy URI-only entries without a trustworthy storage binding can appear as unavailable history. They are not guessed into the current default store, downloadable, or eligible for registry-driven cleanup.

Local upload arguments include explicit `paths` and optional `expireIn`. Wildcards use slash-separated file patterns; names retain their relative directory structure. Symlinks, traversal and non-regular files are rejected. Named bundles, public uploads and implicit untracked uploads are unsupported. Use the step's condition to govern execution; artifact `when` supports only an omitted value or `always`.

## Retention and access

An omitted or empty `expireIn` means indefinite retention. Positive duration strings such as `24h` and day counts such as `7d` are supported, with an upper bound of 3650 days. Expiry is persisted at registration, tied to the original execution timestamp; a retried callback does not extend retention.

Both local and remote jobs are limited to 1,000 registered files across all upload steps. Repeated identical callbacks do not consume another slot. Exceeding this limit fails the upload explicitly; it never silently truncates metadata.

Local inter-job downloads also validate the current registry entry, expiry, size and digest before using the staging copy. Once registration is pruned, a leftover staging/source copy does not authorize a download. Reclaiming those source bytes is a separate, explicitly reviewed workspace-retention policy.

### Agent download integrity and upgrade compatibility

Agent-to-Agent output references carry `sha256` (exact lowercase SHA-256) and an explicit non-negative `size` as well as the original storage ID, location fingerprint and expiry. A JSON `size: 0` is a registered empty file; an omitted or null `size` is unknown. The authenticated terminal report validates the content metadata and execution ownership, then atomically commits the job receipt, enriched output references and `project_artifact` records. Downloads of outputs uploaded earlier in the same job use the exact upload result metadata.

Both automatic `.artifacts` prefetch and explicit Agent artifact downloads require this metadata in baremetal and container modes. They stream into a temporary file, checking the byte count and SHA-256 before an atomic replacement. Overlong objects stop after the declared length plus one probe byte; short content, a digest mismatch, interrupted reads, cancellation and close failures leave the existing destination untouched and remove the temporary file. Path traversal, symlink and special-file rejection still applies. Failure is per file; previously verified files in a multi-file request are not rolled back.

Downloads use a private mode-0700 staging directory and mode-0600 file. The Agent holds their original file descriptors, rechecks the staged bytes after the input stream closes, and verifies the directory/file identities immediately before rename. Detected replacement or modification fails without replacing the destination. This is not isolation from a malicious process running under the same host UID: portable rename has no inode-conditional compare-and-swap, so a continuously racing same-UID writer remains outside this guarantee. Container inputs are staged before their session starts; baremetal work must remain trusted. If a same-UID attacker moves the staging directory to an unknown name, cleanup removes its held contents but may leave the renamed empty directory; it never recursively deletes an unknown replacement.

This is deliberately a fail-closed upgrade for artifact consumption. Historical records and previously accepted terminal receipts remain readable and replayable. A lost-ACK replay of the identical authenticated terminal report confirms its existing receipt without rewriting terminal status, timestamps, references or registry rows. Old cached references lacking the content fields are not silently enriched during replay, and URI-only records do not acquire fabricated checksums. Consequently, previously queued baremetal consumers of such references must fail explicitly rather than execute with unverified bytes. Baremetal runs without artifact inputs are unchanged. Historical missing metadata is also ineligible for immutable container input capture or any future immutable retry path; container retry remains unsupported.

Upgrade the control plane and Agents together before starting new artifact-producing runs. To consume an old output, rerun its authorized producer on the upgraded system so the new run registers full content metadata, then dispatch a new consumer using that run's references. There is no automatic historical backfill or operator bypass. Do not set a missing size to zero, infer a digest solely from an object key, rewrite an accepted terminal receipt, or read a mutable bucket object to invent its original identity. A future explicit registry-based migration would need to verify the original project/run/job/step and object identity transactionally; this release does not perform one.

Expiry blocks new downloads; it does not trigger automatic object deletion. There is no background cleanup timer, startup sweep or deletion side effect in the list/download APIs.

Authenticated APIs are:

- `GET /api/v1/projects/:projectID/artifacts/`, with `page`, `pageSize`, `runId`, `name`, and `status=available|expired|unavailable`
- `GET /api/v1/projects/:projectID/artifacts/:artifactID/download`

Listing filters each artifact by the caller's pipeline-read permission before pagination. Download rechecks ownership, authorization, expiry and original storage binding. It streams through the authenticated API, without issuing public object URLs. Expired content returns HTTP 410; unavailable content returns an explicit error rather than a successful JSON download.

## Preview one bounded cleanup batch

The CLI requires a project business ID. Add a run ID to narrow the scope further. There is no cross-project or unbounded cleanup option.

```sh
arcentra-cli prune-artifacts \
  --conf /private/arcentra/config.toml \
  --project-id PROJECT_ID \
  --run-id RUN_ID \
  --before 2026-10-01T00:00:00Z \
  --limit 100
```

The default is a dry run. `--executors-stopped` without `--apply` is still a dry run. Preview reads registry and ownership records but does not resolve storage providers, make object-storage calls, inspect artifact file contents, or delete metadata. SQLite previews open the existing database in read-only mode; cleanup never creates a missing SQLite database. No Redis/cache service is needed.

`--before` is an RFC3339 timestamp with an explicit timezone and cannot be in the future. If omitted, the command captures the current UTC time and includes it in its report. For a repeatable review, supply the same explicit cutoff to preview and apply. The default limit is 100, with a range of 1–500. The command processes one batch only and never auto-pages.

The report contains project/run scope, cutoff, batch limit, and safe per-artifact outcomes. `would-delete` means the registry row is eligible by scope, expiry and terminal run status; it is not a storage connectivity, content-integrity or deletion-permission check. No object-store secrets, bucket configuration, internal paths or storage keys are printed in the per-artifact report.

## Apply only during an offline maintenance window

1. Back up the database and artifact contents. Deletion is irreversible without an independent backup or provider recovery facility.
2. Stop every controller and Agent that could write these records or objects. Stop download-serving instances and confirm no other artifact consumers or external build/deployment processes remain active. The flag below is an operator attestation, not a distributed lock or automatic shutdown.
3. Verify the exact target database, project/run IDs, managed root mount and original storage configurations. Preview again with the intended cutoff and batch limit.
4. Apply the reviewed bounded scope:

```sh
arcentra-cli prune-artifacts \
  --conf /private/arcentra/config.toml \
  --project-id PROJECT_ID \
  --run-id RUN_ID \
  --before 2026-10-01T00:00:00Z \
  --limit 100 \
  --apply \
  --executors-stopped
```

5. Inspect the full report and process exit status before deciding whether another batch is appropriate. Keep executors stopped for further batches. Resume normal services only after the maintenance outcome is understood.

Only registered rows whose explicit expiry is at or before the cutoff are candidates. The run must be terminal (`success`, `failed`, or `cancelled`), and its pipeline must still belong to the selected project. Pending/running/paused/unknown runs, unavailable ownership, other projects/runs, future expiry and indefinite retention are preserved. A corrupted out-of-scope repository result aborts processing rather than broadening scope. New expiry timestamps are normalized to UTC when registered. Historical SQLite rows containing mixed timezone offsets may not sort chronologically under SQL text comparison; if a selected row fails the actual timestamp/scope recheck, cleanup fails closed. There is no automatic rewrite or migration of those historical values. Review and correct them through a separately authorized data-maintenance procedure before retrying.

The object is removed before its matching metadata version. Local deletion requires a canonical identity-derived path, a valid lowercase SHA-256, matching file size/content and a regular non-symlink file. Missing managed roots fail safely; a genuinely absent file beneath an available managed root can complete a previous interrupted removal. Remote deletion resolves the original stored binding, never the current default store.

## Outcomes and safe retry

- `would-delete`: dry-run candidate only; no deletion attempted
- `deleted`: object removal succeeded and the matching registry version was removed
- `skipped`: active/unavailable ownership, or the registry version changed/already disappeared
- `failed`: object removal or registry removal failed; inspect the reason and retained metadata before retrying

Object-storage failures, mismatched local content, missing checksum, unsafe paths and unavailable storage preserve registration. If object removal succeeds but metadata removal fails, the report says so; a later retry can reconcile it when the provider's missing-object deletion is idempotent. A changed registry version is not deleted. Do not edit metadata merely to bypass a safety check.

The CLI exits with an error if any item failed or the batch stopped unexpectedly, and prints any partial result returned before that error. Earlier successfully processed items are not rolled back. Repeating a bounded batch with unchanged failures/skips may return those rows again; do not loop blindly or assume every invocation advances through all history.

## Local verification

The regression suite uses fake object storage, temporary files and temporary SQLite databases. It performs no external object deletion:

```sh
go test -race -count=1 -run TestArtifactCleanup ./internal/control/process
go test ./cmd/cli
```

Production credentials, deletion permissions, mount health and provider recovery/versioning remain operator acceptance checks. A passing dry run or local test suite does not prove those external conditions.

## Local upload platform boundary

Artifact uploads now open every path component through directory descriptors with no-follow semantics and use nonblocking file opening before verifying the original regular-file inode. This prevents a replaced FIFO from blocking before file-type validation. The implementation supports Linux, macOS, FreeBSD, OpenBSD, NetBSD, DragonFly and Solaris. Other platforms (including Windows) fail artifact upload explicitly rather than falling back to a weaker open path; this also applies to baremetal Agents. Actual platform coverage remains Linux, not a certification of every compiled Unix target. Offline containerd jobs additionally seal all container processes before artifact publication.


The offline single-job implementation additionally passed byte-verified S3 output,
background-process sealing and special-file/FIFO upload rejection in [runtime run
38508866022](https://github.com/arcentrix/arcentra/actions/runs/38508866022), PR head
`e7132be275c144a2eadc03b61e3c4f16dd302c4e` (tested merge
`12e6cd81e2ca6f79bb5967f457aa6b3f377b1a9d`). Independent gate-enabled
[run 38512588313](https://github.com/arcentrix/arcentra/actions/runs/38512588313) repeated those checks through actual production `NewProcess` and
`StartWorker` constructors at PR head `69baaebef85e1f161d4ea2298e6e180e1c56aa8c` (tested
merge `1d2e29030741963897b7056c7d15249f2637d757`), with no reported cleanup leaks. These
hosted test-binary results do not certify production storage, packaged-service or
OCI-image startup. Subsequent [main run 38522727881](https://github.com/arcentrix/arcentra/actions/runs/38522727881) at `4acc47fbdeb23efeabf0bc80939ff0c276a0b574` verified the bounded linear A→S3→B chain with distinct sessions/workspaces and clean teardown. That historical run used trusted object storage; the new per-download digest and immutable input-manifest changes require their own exact-commit acceptance evidence. See [the bounded acceptance record](offline-runtime-acceptance.md).
