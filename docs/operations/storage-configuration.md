# Storage configuration

The administration page manages MinIO and S3 configurations. Existing OSS, GCS,
and COS records remain visible as read-only entries in this UI; their existing API
provider support is unchanged. There is no connection-test, delete, or disable
control in this milestone, and saving does not contact the storage provider.

## Credentials and API contract

All storage configuration endpoints require a currently enabled user with live,
platform-scoped `org:update` authority. Creating, editing, and setting the default
also require live `secret:write`. Project- or organization-scoped grants are
insufficient. This is a request-time authorization check, not a claim that a role
revocation and configuration commit share one database transaction.

- `GET /api/v1/storage/configs` returns the enabled configurations in `detail`.
  Each `config` is a redacted JSON object; `configuredFields` describes credential
  presence. Credential values are never returned.
- `POST /api/v1/storage/configs` accepts `storageId`, `name`, `storageType`,
  `description`, `isDefault`, and `config`. The write-side `config` is a base64
  string containing UTF-8 JSON bytes, not a nested JSON object. IDs must satisfy
  the artifact identity rule (1–64 UTF-8 bytes, no slash,
  backslash, colon, or control characters; no `.` or `..` alone), and names must be
  nonempty. Use a new ID for each new target.
- `PUT /api/v1/storage/configs/:id` accepts partial edits. Omitted or JSON-null
  `name`, `description`, `isDefault`, and `isEnabled` preserve the live stored
  value. An explicit empty description clears it; an empty name is rejected.
  Explicit zero flags persist. A default must be enabled; disabling the current
  default through the existing API requires explicitly clearing its default flag
  as well. The UI does not expose these destructive controls.
- Omitted, JSON-null, or empty-string `config`, or base64-encoded `{}`, preserves
  the existing configuration. Inside its decoded JSON object, omitted,
  exact-empty-string, or JSON-null credential values preserve the stored
  credentials. A nonempty new credential string replaces that credential.
  Mask placeholders such as `********` and `[redacted]` are rejected. There is no
  credential-clearing operation. On creation, missing required credentials fail
  local validation. Base64-encoded JSON `null`, arrays, or scalar values are not
  configuration objects and are rejected.
- `POST /api/v1/storage/configs/:id/default` explicitly selects an existing enabled
  configuration. No request body is needed. Missing or disabled targets return
  HTTP 404; duplicate/ambiguous IDs return HTTP 409. Repeating the current target
  is safe. Malformed input, invalid flags, or target mutation returns HTTP 400.
  Database failures return a sanitized HTTP 500 without config or driver details.

Metadata and credential changes merge against the locked, current database row.
An omitted credential in an overlapping metadata edit therefore cannot write
back a stale credential from a prior read. SQL-value logging is disabled inside
storage mutation transactions to avoid credential exposure on slow/error paths.

## Existing targets are immutable

This release intentionally tightens update compatibility: an existing record's
provider, endpoint, bucket, region, base path, and TLS flag cannot change, even via
a crafted API request. Comparisons preserve exact string spelling; changing case,
trailing slashes, or scheme also changes the target. An omitted optional string
and its explicit empty string are equivalent; an omitted TLS flag means false.

Historical artifacts store the storage ID and a fingerprint of these location
fields. Changing an existing target would make those artifacts unavailable or
redirect future access. To use a new target, create a new configuration, make it
the default, and retain the old enabled record and provider objects. Credentials
are excluded from the location fingerprint, so explicit credential rotation and
metadata edits preserve historical artifact identity. Rotation still needs
credentials that can access the old objects.

## Atomic defaults and operational rollout

Create-with-default, edit-with-default, and explicit switching share one database
transaction discipline: lock the storage rows in primary-key order under MySQL
REPEATABLE READ, validate the target, clear former defaults, and commit the new
selection together. This also covers the empty-table creation case. A transaction
may retry an explicitly rolled-back MySQL deadlock up to three total attempts;
unknown commit results and lock timeouts are not retried. A failed switch cannot
leave the former default cleared. Storage reads use the primary database and
do not reuse the former configuration cache, including after an uncertain commit response. Successful mutations also
invalidate affected legacy cache entries.

No uniqueness migration is performed. New duplicate IDs, including aliases that
compare equal under the database's collation, are rejected. An existing duplicate
ID is refused as ambiguous for edits and default selection. Resolve such legacy
data deliberately before managing that ID; the UI does not silently delete or
merge records. Legacy IDs outside the artifact identity contract remain visible
in the list, but edits and default selection return an error. Use a valid new ID
and retain the legacy data for explicit remediation.

The default marker is persisted configuration, not a hot-reload mechanism. The
control plane and Agents can retain clients loaded at startup. Restart the
relevant control-plane and Agent processes after changing the target or rotating
credentials, and verify a new upload and an authorized historical download. Keep
old credentials valid until those clients have restarted and in-flight work has
completed. Do not revoke the old provider access merely because the save response
succeeded. This milestone does not reconnect running clients automatically.

## Verification

Repository, router, and service tests cover zero values, omitted/blank credentials,
masked-secret rejection, default rollback, cache invalidation, live authorization,
immutable target fields, and historical artifact access after a default change
and credential rotation. The real-MySQL acceptance suite is
`TestStorageMySQLAcceptance` in `internal/control/repo`; it requires an explicitly
provided, initially empty local `arcentra_storage_test_...` schema using
`ARCENTRA_STORAGE_MYSQL_DSN`. Set `ARCENTRA_REQUIRE_STORAGE_MYSQL=1` in CI to make
missing configuration fail rather than skip. It does not create a server or
schema, discover credentials, or connect to production.
