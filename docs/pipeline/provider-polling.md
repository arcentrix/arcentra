# GitHub.com and Gitea pull-request snapshot polling

Polling is optional and starts disabled. An authorized project operator creates a baseline, reviews the existing eligible pipelines, then explicitly enables that exact selection. Baseline never builds historical pull requests. No credential or permission grant is created by these actions.

## Supported configuration

- GitHub: an existing project with repository `https://github.com/owner/repository`, using the fixed `https://api.github.com` API. GitHub Enterprise and alternate API origins are unsupported.
- Gitea: an existing project with a configured HTTPS origin and `owner/repository` path. The API is fixed to that origin's `/api/v1`; deployments under a URL subpath are unsupported.
- GitLab: existing version-1 scopes and persisted cursor/batch identities remain unchanged. See [GitLab polling](gitlab-polling.md).

Executable pipeline-definition repositories currently require SHA-1 Git object format (40-character definition commits). Snapshot readers can retain a 64-character provider source SHA as evidence, but this does not add SHA-256 repository checkout/definition support.

Projects must be active, enabled, and configured for MR/PR triggers with existing public/token authentication. Gitee and Bitbucket are not yet supported by this bounded polling path. No live customer provider account was configured for implementation tests.

## Observation and execution boundaries

GitHub/Gitea use version-2 provider scopes binding project ID, provider, canonical repository, API origin, stable numeric repository ID, and pull-request stream. A provider or repository change invalidates the prior activation rather than transferring its authority.

Each scan reads repository identity, two bounded complete PR inventories, and repository identity again. Both inventories must agree, including entries older than the cursor lower bound. Pages, items, response bytes and time are bounded. Missing fork identity, malformed revision/ref evidence, contradictory pagination, rate limits, redirects and unstable inventories fail closed. This is a bounded consistency observation, not a lossless event history: rapid intermediate updates can be missed, and an inventory exceeding limits cannot advance. Operators must resolve the blocker; the service does not silently truncate or replay old history.

Observed PR source SHA, target SHA, source repository ID, PR ID/number and refs describe provider evidence. Definition commit is the immutable configured pipeline definition revision. Actual checkout commit is independently reported by the Agent. Enabling polling does not rewrite the pipeline's source checkout to the observed PR SHA; do not label the resulting run as PR validation unless the configured source actually matches that evidence.

Gitea can report a manually merged commit’s author time in `merged_at`. That raw value is retained, while Gitea post-baseline eligibility uses the observed `updated_at`. A merged result already seen at baseline stays deduplicated even if metadata later changes. A previously invisible historical merged PR first appearing with a new update can be a newly observed state; this does not prove when the merge action happened. Future/skewed author dates later than `updated_at` fail closed. GitHub/GitLab merge cutoff behavior is unchanged.

## Authorization and recovery

Baseline, enable and disable use current authenticated project authority. Each selected pipeline needs current trigger authority. Durable dispatch rechecks project/provider configuration, immutable definition and current authorization. Empty eligible selections or unmatched refs complete the observed batch without inventing a build. Duplicate durable admissions and acknowledged receipts retain their existing idempotency contract. An uncertain submission stays unresolved and is not automatically replayed as new execution.

Existing migration 009 provides durable streams; this extension adds no schema migration. Upgrade paired backend/frontend versions with controllers stopped according to the existing deployment guide. GitLab serialized scope and event identities remain compatible. New GitHub/Gitea scopes should not be run by older controllers.

## Acceptance evidence

Tests use synthetic TLS provider responses and temporary Git repositories. Repository races run against disposable MySQL in the hosted acceptance lane; execution tests traverse the real Process, Kafka, Agent and S3-compatible storage. Frontend fixture tests exercise provider-specific controls and authorization states. These fixtures do not establish real GitHub/Gitea account connectivity or remote provider behavior. Exact successful commit/run/artifact evidence is recorded separately after CI completes.
