# Administrative HTTP authorization and write-only configuration

The administrative HTTP APIs use the existing bearer/cookie session verifier.
They then reload the enabled user, enabled team memberships, enabled roles, and
unexpired grants from primary storage for the request. Cached menus and claimed
request/body/header identities are not authorization. Organization/project
grants cannot authorize these platform-wide operations.

## Existing permission mapping

All actions in this list are checked at **platform scope**:

- Settings list/read/update: `org:update`. Supplying a secret field additionally
  requires `secret:write`. Deleting a setting requires `secret:delete`.
- Storage configuration list/read: `org:update`. Create/update/default selection
  and general storage uploads additionally require `secret:write`; deletion
  requires `secret:delete`.
- Registration-token list: `agent:read`. Mint/revoke: `agent:register`.
- User list, role-filtered user list, invitations, another user's profile/password
  or extension, enabled-state changes, and invitation-state changes:
  `org:manage_member`.
- Identity-provider management reads: `org:update`. Create/update/toggle
  additionally require `secret:write`; delete requires `secret:delete`.

The first operator-selected administrator still comes from explicit CLI
`bootstrap-admin` after registration and migration. Public registration never
creates a role grant. Login, registration, public login-provider metadata, and
the required external authentication endpoints remain public.

Enabled users retain their own profile, password, avatar, timezone, current-user
fetch, logout, and owner-bound refresh paths. The `me` user ID alias resolves only
to the verified current user. Self-service cannot change enabled state or
invitation authority. Generic extension-object updates are administrative;
ordinary users use their dedicated timezone endpoint.

## Write-only credentials

Settings and storage read/write responses omit credential values, including for
administrators. Identity-provider detail/create responses do the same; its list
and public login list contain only metadata. Administrative responses include
`configuredFields`, a list of hidden field paths such as `secret_key`, `salt`,
`clientSecret`, or `providers.0.password`. No placeholder is returned as a value.
Nested objects and arrays are traversed. These flags indicate stored presence,
not successful credential verification or provider connectivity.

- Settings continue to accept a JSON object in `value`; the path identifies the
  setting. An optional body `name` must match it.
- Storage requests retain their existing base64-encoded JSON `config` field.
  Responses contain only documented noncredential configuration fields.
- Identity configuration remains a JSON object. Provider identity/type changes
  require a separate provider record.
- Configuration updates merge into the stored object. Missing or empty secret
  inputs preserve existing values. Common redaction placeholders are rejected.
- Replacing/removing an object or array entry that contains stored credentials
  fails closed. There is no implicit credential-clearing operation through
  `null`, an empty array, or a changed container type. Deliberate whole-record
  deletion remains separately authorized.
- Newly submitted configuration URL/endpoint fields must not carry userinfo,
  query parameters, or fragments. Legacy such fields are hidden and treated as
  secret-bearing during updates; replace them with a safe URL under secret-write
  authority rather than relying on round-tripped masked data.

Keep credentials in designated credential fields. Field-name redaction is not a
general detector for arbitrary secrets hidden in unrelated free-form text.

## Verification

`go test -race ./internal/control/router` covers real session middleware with
synthetic session records, explicit migrated/bootstrap administrator fixtures,
unauthenticated/no-role/foreign-scope refusal, live role/grant/user/membership
revocation, write-only responses, nested secret handling, safe preservation,
legitimate self-service, and administrative token/password/provider operations.
These tests use disposable SQLite and no external account or production secret.
