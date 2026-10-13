# Disposable real-stack and browser acceptance

The normal PR/main CI has a separate, time-bounded acceptance job after the existing quality gate. It uses standard GitHub-hosted Ubuntu, read-only repository permissions, fixed official MySQL/Kafka/Redis versions and a pinned official MinIO source build, and the lockfile's official Playwright Chromium. It does not use production secrets, `pull_request_target`, privileged containers, production deployment, or paid infrastructure provisioning.

## Two complementary checks

1. `TestRealStackAcceptance` runs the production Process → real Kafka → Agent shell → authenticated TLS gRPC → MySQL path, real object storage and Kafka log persistence. It checks artifact bytes/identity, duplicate task suppression, lost terminal acknowledgments and failure results. Its database must be a fresh `arcentra_acceptance_<suffix>` schema, and may not be the browser database.
2. `scripts/acceptance/run_actual_app.py` launches the compiled CLI, Control and Agent. It explicitly migrates a separate empty `arcentra_acceptance` database, registers a synthetic user through HTTP, stops Control, runs the real bootstrap-admin CLI, restarts, configures storage and Agent registration through authorized APIs, enrolls an Agent over verified TLS, approves it, and resumes its persisted identity. A browser then logs in and creates a project/pipeline, reviews the real Git definition, starts an actual build and verifies downloaded artifact bytes and refresh/re-login persistence. No API interception, token injection, pre-seeded business rows or double-encoded password workaround is allowed.

The separately named frontend fixture-browser check still uses simulated transport and is not this end-to-end check. Both must be reported distinctly. Real acceptance remains **unverified until the exact remote commit's designated job succeeds**; a local skipped test or compiled script is not a pass.

## Isolation and data boundary

`compose.yml` publishes dependency ports only on numeric loopback, with separate Compose project names per run, volatile dependency data, memory limits and bounded health waits. It contains intentionally public synthetic passwords only. No persistent credentials are created outside the disposable runner. Real-stack tests reject non-loopback targets and non-acceptance database names; actual-app fixture additionally requires the GitHub-hosted runner environment and exact documented synthetic endpoint values. Do not weaken these guards to point acceptance at an existing environment.

The controller/Agent configuration, CA/private keys, token-bearing identity state, workspace and raw child logs live in a private temporary directory and are removed in cleanup. Every spawned process is terminated and waited for, then this run's Compose resources are removed even on failure. Runner destruction remains the final isolation boundary after job timeout/cancellation. No cleanup targets external buckets, databases or user work.

Only allowlisted screenshots and small structured status summaries may be uploaded for seven days. Do not upload Playwright network traces, token-bearing HAR, raw API responses, private configurations, identity directories or raw runtime logs. Screenshots must mask password inputs. Diagnostic logs are synthetic and sanitized; authentication tokens and encoded forms must not be logged.

## Required CI inputs

All values are temporary fixture configuration, not repository secrets:

- `ARCENTRA_REAL_STACK=1` and `ARCENTRA_REQUIRE_REAL_STACK=1`
- `ARCENTRA_TEST_MYSQL_DSN` with numeric loopback and a disposable acceptance schema
- `ARCENTRA_TEST_KAFKA_BROKERS=127.0.0.1:19092`
- `ARCENTRA_TEST_REDIS_ADDRESS=127.0.0.1:16379`
- `ARCENTRA_TEST_S3_ENDPOINT=http://127.0.0.1:19000` plus synthetic access/secret keys

The designated Go command is `go test -race -count=1 -timeout=8m -run '^TestRealStackAcceptance$' ./internal/agent/taskqueue`. Missing required opt-in or dependencies fails instead of silently using mocks. Ordinary local full tests may skip it when neither flag is set. CI pins the cross-repository frontend by a reviewed immutable commit, not a drifting branch.

## Finite MVP boundary

Required: explicit installation/bootstrap, login and authorization, operator-scoped project, real supported pipeline execution and Agent enrollment/approval/restart, logs, artifacts, cancellation/replay, and honest unavailable views for integrations that lack operator configuration. Ordinary registered accounts must not reach administrative settings, storage credentials, enrollment-token management or other users' administration.

Current project creation accepts an operator-selected organization identifier; organization lifecycle is not implemented. Kubernetes release and Copilot remain disabled until explicitly configured for an authorized destination. This acceptance makes no cluster deployment or model request, and does not certify containerd isolation, HA fencing, arbitrary plugins, general organization management or external provider availability. Those boundaries must be shown honestly rather than replaced with fake success.

## Local verification limitation

The assistant's cloud workspace has no supported container runtime and Chromium's socket is denied. It can compile these tests, run safeguards and existing process integration, but cannot claim the real services/browser passed here. Do not bypass the restriction or switch to a user's computer. The official hosted-runner result is the evidence for this gate.

References: [GitHub service containers](https://docs.github.com/en/actions/tutorials/use-containerized-services/use-docker-service-containers), [Apache Kafka Docker examples](https://github.com/apache/kafka/tree/trunk/docker/examples), [Playwright CI](https://playwright.dev/docs/ci).

## MinIO source-only test distribution

The former official `quay.io/minio/minio` image returned registry authorization failure on the first hosted acceptance run. CI does not retry it with credentials or use a mirror. The [official source-only guidance](https://github.com/minio/minio/blob/9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a/README.md#source-only-distribution) supports building the public source instead. CI checks out release `RELEASE.2025-10-15T17-29-55Z`, verifies exact commit `9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a`, and builds it with the repository's pinned Go 1.25.6 and upstream readonly module lock. The static binary, AGPL-3.0 license and NOTICE are packaged into a local scratch image, run as UID 65532 with a read-only root and disposable writable tmpfs only. `pull_policy: never` prevents any fallback registry pull. The build has a 12-minute limit within the 35-minute acceptance budget; Go's existing cache covers build dependencies.

This unmodified source build is used only as an isolated S3-compatible test dependency. Its image and binary are not pushed or shipped with Arcentra, and it is not a production storage recommendation or a claim of upstream maintenance. The source license and notices are retained; operators choosing their own production storage must assess its support and license requirements separately. The real tests still exercise MinIO's S3 protocol implementation rather than a mock or a replacement vendor.
