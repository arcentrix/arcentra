# Gitee.com and Bitbucket Cloud polling

This extends the explicit-baseline polling contract with version-3 evidence. Operator activation remains off by default. Existing GitLab v1 and GitHub/Gitea v2 evidence is preserved byte-for-byte; old captures and receipts are not rewritten. Stop old controllers before deploying a version that can create v3 scopes. No new schema migration is required beyond the existing polling migrations.

## What is observed

| Provider | Stable repository identity | Merged observation evidence | Unavailable field |
| --- | --- | --- | --- |
| Gitee.com v5 | Canonical positive decimal ID | Provider state and reported merge timestamp | Merge-result commit SHA |
| Bitbucket Cloud 2.0 | Canonical lowercase, brace-free UUID | Provider state and full merge-result SHA | Merge timestamp |

Missing evidence is absent, not invented from source/base commit, current time, or commit author time. A source PR SHA, the configured pipeline-definition commit, and the Agent's actual checkout are separate facts. Polling runs the configured trusted definition and does not replace its checkout with a fork URL or PR head.

New-provider cutoff eligibility uses the observed update time, not a fabricated merge-action time. Stable merged identities exclude later comment/update timestamps, so an already-baselined merged object does not run again merely because its metadata changes. Two agreeing complete bounded inventories are consistency observations, not an atomic snapshot or lossless history. Intermediate changes, deleted objects and inaccessible history may be missed.

## Configuration and permissions

Only existing project settings are used. Supported sources are exactly `https://gitee.com/{owner}/{repo}` and `https://bitbucket.org/{workspace}/{repo}` with the documented bounded ASCII repository-name subset. API origins are fixed; Gitee Enterprise custom installations and Bitbucket Server/Data Center are unsupported. No user-supplied source link or redirect can choose an authenticated request destination.

A project must already be active, enabled and configured for MR/PR triggers. A currently authorized user explicitly saves a non-executing baseline, then separately selects and enables existing eligible pipelines. Each definition read, capture, admission and later replay retains current project/pipeline/configuration/authorization checks. Readiness does not create a credential or grant.

Gitee v5's documented token route uses the existing configured access token in a fixed-origin query. The reader never returns credential-bearing URLs, response bodies or transport errors. Gitee private Git username/token behavior has not been verified with a real account; definition access must succeed independently, and this release does not claim private Git acceptance.

Bitbucket API requests use an existing configured Bearer token. Polling Git-definition reads use the documented `x-token-auth` username through the isolated askpass environment, never a credential URL. Operators must provide an already appropriate token; this feature does not create OAuth grants, refresh credentials, or enlarge scopes.

## Bounded failure behavior

- Every inventory is subject to page, row, byte, allocation, header and deadline bounds, including both consistency passes and repository identity reads.
- Gitee validates page numbers, terminal evidence and only narrowly recognized same-repository Link echoes. Only empty or exact configured-owner source-namespace echoes are accepted, and are never copied into inventory requests. Other namespace filters or unknown continuation shapes are rejected.
- Bitbucket validates body `next` against the fixed repository path and query contract. Only supported bounded continuation tokens are accepted; abbreviated hashes or missing fork identity block the inventory.
- Redirects, identity changes, malformed evidence, 401/403, 429, transport errors and exhausted bounds never produce a partial successful cursor. Retry backoff is bounded; uncertain execution outcomes remain unresolved and are not automatically resubmitted.
- Hard v3 configuration, authentication, forbidden, malformed, identity, pagination and unsupported-evidence failures persistently pause the stream. A later valid response does not automatically resume it; the operator must inspect and explicitly recover the configuration/baseline. Transient transport, rate/status, movement and inventory-limit failures retain bounded retry behavior.
- v3 string identities and evidence modes are validated against the persisted authoritative provider scope. They cannot weaken v1/v2 validation or substitute for a different provider's identity.

## Validation status

Synthetic reader/domain/service tests and disposable CI acceptance are required before publishing this capability. Actual provider accounts and private repositories are not configured by the test suite. Refer to the released cumulative acceptance record for the exact commit and hosted result; development branch tests alone are not an operational acceptance claim.
