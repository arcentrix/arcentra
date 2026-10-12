# Project artifacts

Successful artifact uploads now produce immutable `project_artifact` records. The registry binds every file to its project, pipeline, run, job and step. Two upload steps can publish the same relative filename independently. Repeating the same callback is idempotent; changing the registered content, expiry or location for that identity is rejected.

## Installation and storage

Run the existing explicit migration command against the intended database before starting the updated control plane:

```sh
./cli migrate --conf /private/path/config.toml
```

Normal server startup never modifies the schema. Review and test the migration on a staging copy first. SQLite migration and query behavior are covered by automated tests; a production MySQL migration remains an operator validation step.

Local content is copied into `data/artifacts` by default. Set `ARCENTRA_ARTIFACT_ROOT` to an absolute directory on a persistent volume for production. Every control-plane replica serving downloads needs access to that same managed content. Run workspaces and their cleanup are independent from this durable root. Do not make the directory publicly accessible or writable by untrusted processes.

Local paths are project/run/job/step/name scoped. Reads reject traversal, symlinks, non-regular files and changed content. A recorded SHA256 protects content integrity. Symlinked root directories are rejected too; configure a real directory path.

Remote uploads bind the storage configuration ID and a fingerprint of provider, endpoint, bucket, region, base path and TLS settings. Credentials are excluded from this fingerprint so credential rotation can preserve access. Changing a location field makes existing artifacts unavailable rather than reading from a different bucket. Remote keys include run/job/step/SHA256/name and are never accepted from the HTTP client.

Older job records containing only opaque object keys appear as unavailable historical entries. They cannot be downloaded or cleaned by guessing the current default storage. Legacy local workspace files are not automatically imported into the registry.

## HTTP API

Both endpoints require the existing bearer session and `pipeline:read` permission for the owning project/organization/pipeline. Ownership is checked through the persisted pipeline, run, job and step relationships. Listing filters unauthorized pipelines before counting and pagination.

- `GET /api/v1/projects/:projectID/artifacts`
- `GET /api/v1/projects/:projectID/artifacts/:artifactID/download`

List parameters:

| Parameter | Meaning |
|---|---|
| `page` | 1-based page, default 1 |
| `pageSize` | 1–100, default 20 |
| `name` | Literal filename substring |
| `runId` | Exact pipeline run ID |
| `status` | `available`, `expired`, or `unavailable` |

The normal response envelope contains `detail: { list, total, page, pageSize }`. Each entry includes `artifactId`, `projectId`, `pipelineId`, `pipelineRunId`, `jobRunId`, `stepRunId`, `name`, `size`, `sizeKnown`, `mediaType`, `storageType`, `createdAt`, optional `expiresAt`, `status`, `downloadAvailable`, and optional `unavailableReason`. A legacy size is unknown when `sizeKnown` is false, even though its numeric `size` is zero. Timestamps use RFC3339. Lists are a live view, not a transactional snapshot: concurrent uploads or cleanup may appear on the next refresh. Stable IDs are deduplicated while scanning so shifted pages cannot inflate totals. Internal paths, object keys, credentials and storage configuration IDs are excluded.

Downloads return the actual binary content, including empty files and JSON files. Clients should use an authenticated blob request and respect `Content-Disposition: attachment`, not interpret file bytes as API envelopes. `Content-Disposition` is exposed through CORS. Downloads use `nosniff`, `private, no-store`, and a sandbox content security policy. Responses are streamed without loading the entire file into the HTTP middleware buffer; the transport closes the reader when finished.

Download failures have real HTTP status codes: 401 for no session, 403 for insufficient permission, 404 for a missing artifact or different project, 410 for expiry, 409 for unavailable content or changed storage, and 503 for an unavailable service. Error messages never include storage credentials, object keys or local paths. Expiry is checked again at download time even if a client has a stale list.

Remote list availability means the original configuration remains resolvable. Object existence is checked when downloading, including a pre-read before sending success headers. A remote storage outage during a stream may still interrupt an otherwise authorized download; clients should treat an incomplete transfer as failed.

## Verification

```sh
go test -race -count=1 ./pkg/artifact ./internal/control/repo ./internal/control/service ./internal/control/router ./pkg/http/middleware
```

Tests use disposable SQLite databases, local temporary files, and fake object storage. They cover ownership, idempotency/conflicts, retention, pagination/filtering, permission grants, actual empty/JSON downloads, symlink and traversal refusal, digest mismatch, fixed storage identity, secret-safe errors and stream cleanup. They do not contact or mutate a real bucket.
