# Kubernetes image releases: bounded milestone

This milestone provides **explicit releases from successful pipeline runs**, plus a separately opt-in [bounded pipeline Kubernetes job](pipeline/kubernetes-jobs.md). It is not a GitOps controller, general Kubernetes manifest engine, or background reconciliation service. No real cluster was connected or deployed during development. Tests use injected HTTP transports and disposable SQLite databases.

## Supported workflow

1. An operator explicitly migrates the application schema using the existing schema-migration command. The new models are `DeploymentEnvironment` and `DeploymentRelease`. Starting the HTTP server does not migrate a production database.
2. An operator configures a fixed Kubernetes target (below). The browser cannot supply an API-server address, kubeconfig, authentication token, or authorization tier.
3. In a project's Environments tab, a user with `project:update` declares the exact target reference, namespace, Deployment, and container. Bindings cannot be edited through the API. Unconfigured declarations can be saved, but cannot prepare or execute releases.
4. A pipeline builds/publishes an OCI image, resolves its immutable registry digest, and publishes a registered artifact containing the small image manifest below. This implementation does not build or upload container images.
5. After the pipeline run succeeds and all persisted approval requests for that run are approved, select that pipeline ID, run ID, and artifact ID in Deployments. The server checks ownership, a full immutable definition revision SHA, artifact expiry, artifact size and SHA256, and the image reference.
6. **Prepare release** records immutable provenance and reserves the environment. It does not contact Kubernetes.
7. **Review execution → Apply selected image** performs one bounded apply attempt. The first request reads the existing Deployment, durably records its UID/generation, and conditionally patches only the named container's image and a pod-template release annotation. An accepted patch leaves the record `observing`.
8. **Check rollout** explicitly reads Kubernetes and records the latest observation. Refreshing the page only reads persisted records. There is no automatic polling or autonomous cluster write.

Prepare, execute, and cancel require project update, pipeline read/trigger, and the operator-assigned `release:deploy_dev`, `release:deploy_staging`, or `release:deploy_prod` permission. The default authorization tier is `prod`. List results are filtered by pipeline-read authorization before pagination; totals do not include unauthorized releases.

## Image manifest artifact contract

The artifact must be an already registered, nonexpired file from the exact selected project/pipeline/run, between 1 and 4096 bytes. It is one JSON object containing **only** one string field:

```json
{"image":"registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
```

The example digest is a placeholder. Use the digest returned by the actual image registry. A mutable `:latest` or version tag alone is rejected. A digest-qualified tag is still content-addressed and accepted. The file's own SHA256 verifies the JSON bytes; it is **not** treated as the container-image digest. Release records snapshot both the image reference and artifact SHA256, artifact ID, pipeline/run IDs, and definition revision SHA. `commitSha` is the pipeline definition revision, not verified build-source provenance. The actual source checkout revision is not established by this field. Current guarantees are the artifact-byte checksum and digest-pinned image reference, not an attestation that an image was built from a particular source commit. Subsequent artifact expiry does not rewrite a prepared release's pinned image.

## Operator configuration

Default: disabled, no credential-file reads and no cluster network requests. The following is a **disabled example**, not a request to create credentials or contact the example server:

```toml
[deployment]
enabled = false

[[deployment.targets]]
ref = "staging-web"
projectId = "REPLACE_WITH_PROJECT_ID"
tier = "staging" # dev | staging | prod; omitted means prod
allowPipelineDeployments = false # separate per-target opt-in; manual releases remain unchanged
server = "https://kubernetes.example.invalid"
caFile = "/run/arcentra/kubernetes/ca.pem" # optional; otherwise system trust
bearerTokenFile = "/run/arcentra/kubernetes/token"
namespace = "staging"
deployment = "web"
container = "app"
```

Only enable after the operator supplies an approved server, trust configuration, existing mounted token, exact project/workload, and appropriate Kubernetes RBAC. The implementation does not create service accounts, tokens, RBAC grants, or persistent external access. No kubeconfig exec plugins, shell commands, insecure TLS, arbitrary-resource apply, or HTTP redirects are supported. Token files are read once at startup, so rotation requires an application restart. Never put token contents in TOML or browser forms.

Minimum Kubernetes access for each target is `get` and `patch` on the named `apps/v1` Deployment in the configured namespace. Restrict the credential using Kubernetes `resourceNames` and namespace-scoped RBAC. Namespace creation, Deployment creation, Secret reads, and other resources are not used.

Targets are bound to an exact project, namespace, Deployment, and container. A target reference does not grant access to other namespaces or projects. The registry is validated before HTTP serving; malformed enabled configuration aborts startup rather than partially enabling targets. Duplicate bindings of the same server/namespace/Deployment are rejected. Configuration presence is not a cluster health check.

Each prepared release snapshots a fingerprint of the target reference, endpoint, project, workload, and authorization tier. Changing those values cannot silently redirect an existing release after restart; advancement fails closed. Token/CA rotation alone does not change this identity. Cancel an unapplied queued release and prepare a new one after an intentional target change. For an already-applying release, restore its original authorized binding and inspect it before retrying.

## API

All endpoints are JWT-authenticated and scoped to `/api/v1/projects/:projectID`. Responses use the existing success/error envelope and actual HTTP error status codes. Reads are private/no-store.

| Method/path | Purpose |
| --- | --- |
| `GET /environments?page=1&pageSize=20` | Persisted target bindings and exact configuration availability |
| `POST /environments` | Create immutable environment declaration |
| `GET /releases?page=1&pageSize=20` | Authorized persisted release history |
| `POST /releases` | Prepare an immutable release; no cluster write |
| `GET /releases/:releaseID` | Persisted release/provenance/observation |
| `POST /releases/:releaseID/advance` | One explicit manual apply/observation pass; pipeline uncertain applies require recovery |
| `GET /releases/:releaseID/inspect` | Read-only bounded Kubernetes inspection |
| `POST /releases/:releaseID/recover` | Explicit confirmed same-ID uncertain-apply recovery |
| `POST /releases/:releaseID/cancel` | Cancel only an unapplied queued release |

Create-environment fields: `name`, `targetRef`, `namespace`, `deployment`, `container`.

Create-release fields: `requestId`, `environmentId`, `pipelineId`, `pipelineRunId`, `artifactId`. IDs are at most 64 characters. Supply the same `requestId` on an uncertain retry. It cannot be rebound to another source or environment. Preparing a different active release for the same environment returns conflict. The browser retains its request key for repeated submissions of the same selection, and prevents concurrent button actions.

Page size is 1–100. Error categories: 400 invalid input; 401 unauthenticated; 403 unauthorized; 404 wrong/missing scoped identity; 409 state/target/concurrency conflict; 422 invalid source; 503 unconfigured target, unavailable API/database, or ambiguous remote result. Error text never includes the Kubernetes response body or token.

## Durable state and uncertainty

`queued → applying → observing → succeeded | failed | superseded`

`queued → cancelled`

- An environment's active release is reserved transactionally with creation. Versioned compare-and-swap transitions prevent cancellation from racing an apply claim.
- The baseline UID/generation is durable **before** PATCH. PATCH tests UID, resourceVersion, generation, and container identity. Unrelated workload fields are not rewritten.
- The pod-template annotation `arcentra.io/release-id` identifies an admitted release. A retry after a lost response observes the same marker/image/generation and does not patch again.
- Network errors leave `applying` or `observing` unresolved. They never become fabricated success or failure. Manual releases can retry `advance` using the same ID. Pipeline-origin releases require explicit `recover` with `confirmRecovery:true`; an uncertain apply cannot be cancelled as if nothing happened.
- Replaced workload UIDs, changed generations, or another release's marker are not overwritten. Observed replacement/supersession is recorded as `superseded`.
- Success requires the exact release UID/generation/image/marker, a current observed generation, all desired replicas updated and available, no old replicas, and no unavailable replicas. Paused and scaled-to-zero workloads remain unresolved; zero replicas do not prove serving health.
- `Progressing=False / ProgressDeadlineExceeded` from Kubernetes produces `failed`. API dispatch success alone does not. Kubernetes does not automatically roll back a failed Deployment.
- Terminal records are historical observations. They do not prove present workload health. A completed release does not continue monitoring later drift.

Cancellation does not reverse Kubernetes mutations. Active cancellation, automatic rollback, and a generic rollback endpoint are intentionally absent. To restore a previous image, an authorized user can prepare and review a **new release** from that successful run's still-available immutable manifest. It follows the same ownership, permissions, target fingerprint, concurrency, and actual observation checks.

## Verification

Covered with local-only tests: scoped API authorization and actual status codes; target tier restrictions; digest/ownership/commit/approval validation; idempotency and environment leases; wrong project/namespace/container; configuration disabled/error cases; target identity changes; restart-safe apply markers and conditional patches; stale observations, replacement UID, changed image/generation, partial rollout, paused/scaled-to-zero, and deadline failure. UI tests cover error/empty/unconfigured states, review/Back, repeated clicks, uncertain preparation retries, scoped navigation and browser Back/Forward.

Not verified here: a real API server, actual admission controllers, registry pulls, service traffic, mounted-token permissions/rotation, production MySQL migration, or an end-to-end pipeline-to-live-cluster release. A local authenticated fake HTTP transport proves protocol behavior without private artifact uploads or live cluster access.

## Official API references

- [Kubernetes Deployment controller and rollout status](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)
- [Kubernetes API concepts: resourceVersion and conditional PATCH](https://kubernetes.io/docs/reference/using-api/api-concepts/)
- [apps/v1 Deployment API](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/deployment-v1/)

Documentation was checked against these official primary sources during implementation. The implementation uses the stable `apps/v1` Deployment API and JSON Patch.

## Approval provenance

The public manual release API still verifies every persisted approval associated with its completed source run. An empty list is not proof that a legacy DSL gate executed. The bounded pipeline engine now supports exactly one final-job durable manual gate, with trusted operator candidates, exact immutable release evidence and fresh authorization before any PATCH. See [durable deployment approvals](pipeline/durable-deployment-approvals.md).

Targets with an operator `pipelineApproval` policy require that workflow: public manual release preparation and first apply are rejected, including successful runs with no approval records. Legacy plugin/IM gates and predecessor gates do not gain deployment authority. Ordinary targets with no approval policy retain their explicit manual release workflow.

## Inspecting an uncertain apply

Do not create a replacement release or delete its environment reservation when an
apply response is lost. Refresh the release record and retain its release ID,
workload UID and baseline generation. An authorized cluster operator can use
read-only Kubernetes GET on the exact configured Deployment to compare its UID,
generation, image digest and `spec.template.metadata.annotations["arcentra.io/release-id"]`
with that record. No mutation is required for this inspection. The explicit
recovery action on the **same** pipeline-origin release (or advance for a manual release) then reconciles the marker and either
observes an admitted patch or conditionally applies the original release if it
was not admitted. It never licenses changing targets or creating a second release.
If the target changed or observation is still unavailable, leave the reservation
in place and resolve the target/connectivity issue before advancing.
