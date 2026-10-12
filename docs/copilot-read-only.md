# Read-only Copilot

This slice provides real OpenAI-compatible Chat Completions and Anthropic Messages adapters, with server-side project/model allowlists. No production provider endpoint, credentials, or project approval is configured by this change. No model request is made on startup, status checks, source listing, or previews. Configuration means configured, not a successful connectivity probe.

## Setup boundary

An operator must first approve the provider destination, data policy, model, cost policy and project scope, then explicitly configure the server. `copilot.enabled` defaults to false and `allowedProjectIds` defaults to empty. Secrets never belong in browser state or request bodies. Provider configuration is omitted from serialized application configuration.

Example schema only (intentionally disabled and no credentials):

```yaml
copilot:
  enabled: false
  allowedProjectIds: []
  providers: []
```

Each approved provider entry has `id`, `kind` (`openai-compatible` or `anthropic`), `baseUrl` (an HTTPS API prefix such as a vendor's `/v1`), `apiKeyEnv` (the name of a server environment variable), and an explicit `models` list. There is no default endpoint, key, model or allowed project. Arbitrary browser-supplied URLs/keys/tools are rejected. Redirects are refused, including same-host redirects. Keep network egress allowlists and provider credentials limited to the approved destination. Invalid or incomplete entries are unavailable, and duplicate provider IDs disable the registry. Restart after changing provider configuration or keys; the router takes a startup snapshot.

OpenAI-compatible uses `/chat/completions`, bearer authentication, `stream:false`, `store:false` and `max_completion_tokens`. A server-side `tokenLimitField: max_tokens` option supports older compatible servers; this must be explicitly configured and does not trigger fallback/retry. Anthropic uses `/messages`, `x-api-key`, `anthropic-version: 2023-06-01`, a top-level system prompt, and `max_tokens`. Both send text only. Provider errors are sanitized; raw response bodies/URLs/credentials are not returned to users. No tool schemas are sent and any tool-use response is rejected. `store:false` is not a guarantee of a vendor's retention policy; operators must review the destination's policy.

## User flow and API

All routes are JWT-authenticated and project-scoped under `/api/v1/projects/:projectID/copilot`:

- `GET /status`: authorized project model configuration and limits
- `GET /runs/:runID/context`: bounded, authorized step/code-artifact metadata, without reading log/artifact content or contacting a model
- `POST /preview`: locally read, verify and redact the explicitly selected evidence; return the exact bounded model-user text, warnings, evidence metadata and a digest
- `POST /analysis`: re-authorize, re-read, re-redact, and verify the preview digest; make one provider request only after `shareSelectedContext:true`
- `POST /analysis/:requestID/cancel`: owner/project-bound cancellation request; early cancellations create short-lived tombstones

Preview/analysis use a request with `requestId` (16–128 identifier characters), `runId`, `providerId`, `model`, `question`, `context:{stepRunIds,artifactIds,fromLine}`, `shareSelectedContext` and (analysis only) `contextDigest`. The digest binds provider, model, system prompt and exact redacted input. Changes to the selected logs, artifacts, question or model require a new preview. Legacy chat query parameters do not restore or manufacture conversations. Results live only in the current mounted view; no conversation/history storage is implemented.

The page and drawer require an explicit project/run, source selection, preview and per-request approval. They show only server-configured models. The UI renders provider text as escaped plain text and never executes commands, retries, deployments, HTML or model tools. Closing the drawer, navigating away or changing scope aborts the UI request, attempts authenticated cancellation for an active analysis and suppresses stale responses.

## Context and ownership

Both project-read and pipeline-read authorization must pass. The server resolves project → run → pipeline before reading any selected content. Every selected step and artifact must belong to the exact authorized project, pipeline and run. All selected source identities are validated before any content is read, so adding one foreign selection blocks the whole request.

Code is a **registered text-code artifact snapshot**, not arbitrary repository-file access. This uses the existing artifact registry, ownership chain, confined local/object paths, original storage identity and expiry checks. There is no SCM clone, repository URL fetch, path input or implicit workspace lookup. Only allowlisted source/text extensions (plus Dockerfile/Makefile), a recorded SHA-256, bounded size and valid UTF-8 without NUL are accepted. Dot-files/directories, traversal/encoded paths, credential/secret/private-key/kubeconfig names and binary/archive extensions are denied before opening content. SHA-256 is verified again over the bounded bytes for both local and remote objects. A missing, expired, mutated or unsupported artifact blocks the request. An empty code list is shown honestly as unavailable code context.

Artifact-producing step Env/Secrets are included in the redaction set even when those logs were not selected; this does not implicitly read that step's logs.

## Bounds and redaction

- Question: at most 4 KiB of UTF-8
- Log selection: at most 4 explicit steps, 200 persisted lines per step, explicit starting line; oversize raw entries/batches are rejected
- Code selection: at most 3 registered artifacts, 8 KiB each
- Combined selected evidence: at most 24 KiB after redaction; truncation is reported, UTF-8 boundaries are preserved
- Provider system + user text: at most 32 KiB; this is a byte budget rather than an exact tokenizer count
- Output: provider token limit 2,048, response envelope at most 512 KiB and accepted text at most 64 KiB
- Analysis: 45-second deadline, at most 16 in flight per instance and 2 per user; at most 1,024 active/90-second recent request records
- Local preview: 15-second deadline
- Request JSON: at most 16 KiB; unknown fields and trailing JSON are rejected
- Source picker: first 200 steps and first 100 registered artifacts (with explicit warnings when truncated)

Before truncation or transmission, the service redacts registered project, applicable pipeline and global secrets (using the existing SecretService), sensitive run/project/step env values, declared producer-step secrets, the provider key, common credential assignments, bearer/basic auth, token patterns, private-key blocks, and URL userinfo. Known values and common URL/base64 encodings are removed from prompt/question/content/metadata labels and returned model text. Secret-set size is bounded; incomplete secret enumeration, unreadable/undecryptable secrets or malformed sensitive env values fail closed before context is sent. The operator's `ARCENTRA_SECRET_ENCRYPTION_KEY` must match the key that encrypted existing secret records; absence or mismatch blocks analysis for those projects rather than skipping masking.

Pattern-based detection cannot guarantee discovery of every unregistered or transformed secret. Review the actual redacted preview and use approved egress/data policy; never claim perfect masking. No raw prompt, code/log text, upstream error body or provider credential is intentionally logged or persisted by this component.

## Cancellation and deployment limitations

Cancellation is best effort after data has been sent. It cannot recall transmitted data or guarantee provider-side billing stops. Requests use Go context cancellation and an explicit authenticated cancel endpoint because Fiber's request context does not itself establish client-disconnect cancellation. Cancellation and analysis must reach the same control-plane instance; use session affinity for this stateless slice. Restarting an instance stops its in-flight HTTP requests; there is no durable conversation/job recovery. A single API call is never automatically retried or silently switched to another model.

No model can trigger a pipeline retry, failed-step/job replay, deployment, filesystem write or other product mutation. Those existing product actions remain separate authorized flows.

## Verification

Tests use local `httptest` servers, synthetic fixtures and fake providers only. They cover both vendor request/response formats, server-side credentials, limits, redirect refusal, cancellation, foreign project/run/step/artifact refusal, malformed JSON, preview digest drift, secret redaction including artifact filenames and producer-only secrets, binary/oversize/expired/tampered/path-unsafe artifacts, UI approval/invalidation, escaped response text and stale/unmounted request suppression. They do not establish production connectivity, model availability, cost or retention guarantees.

Primary protocol references reviewed for implementation:

- OpenAI Chat API: https://developers.openai.com/api/reference/resources/chat
- Anthropic Messages: https://platform.claude.com/docs/en/api/messages/create
- Anthropic authentication/version headers: https://platform.claude.com/docs/en/api/overview
