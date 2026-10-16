# Actual application acceptance fixture

`run_actual_app.py` runs the **built CLI, Control and Agent processes** against the
disposable MySQL, Redis, Kafka and MinIO services in `compose.yml`. It then invokes
the frontend's actual-backend browser journey. No HTTP responses, broker messages,
repository, object store, database tables or Agent records are mocked or seeded by
this fixture.

This stage is implemented for a fresh GitHub-hosted Linux runner. It refuses to run
without `ARCENTRA_REAL_STACK=1`, `GITHUB_ACTIONS=true` and
`RUNNER_ENVIRONMENT=github-hosted`. Dependency overrides must equal the documented
synthetic loopback values in the script; there is no remote-service or real-secret
mode. The CI workflow starts the official service images, initializes the empty
database and creates the `acceptance` bucket with `create_bucket.go` first.

The browser command runs in the frontend checkout and owns its Vite server on
`127.0.0.1:5174`. The fixture provides the actual backend at
`http://127.0.0.1:18080` and a read-only Git daemon on `127.0.0.1:19418`. Set
`PLAYWRIGHT_BROWSERS_PATH` explicitly if Chromium was installed outside the normal
runner cache. Browser and application children use a private HOME and TMPDIR,
without inherited cloud credentials, proxy settings or SSH agents.
The Agent child must see preinstalled **Linux Go 1.25.6**, Git, Python 3 and a
shell on its sanitized PATH. The fixture fails before service startup if Go is
missing or wrong; it never downloads an alternate toolchain or project dependency.

```sh
python3 scripts/acceptance/run_actual_app.py \
  --control-bin "$PWD/.acceptance/bin/arcentra" \
  --agent-bin "$PWD/.acceptance/bin/arcentra-agent" \
  --cli-bin "$PWD/.acceptance/bin/arcentra-cli" \
  --browser-cwd "$PWD/.acceptance/frontend" \
  --evidence-dir "$PWD/.acceptance/evidence" \
  -- node tests/actual-backend-acceptance.mjs
```

The lifecycle under test is:

1. Run `arcentra-cli migrate` on the empty MySQL database.
2. Start Control without preconfigured storage and register `ci-admin` through the
   public HTTP endpoint using the normal single-base64 password wire contract.
3. Stop Control, explicitly run `bootstrap-admin`, restart and sign in again.
4. Configure MinIO and a generated disposable Agent signing secret through the
   authenticated APIs, then restart Control to reload startup-bound storage.
5. Generate a private short-lived CA/server certificate with official OpenSSL.
   Enroll the actual Agent over verified TLS using an API-generated enrollment
   token and identity persistence. Assert pending approval gives liveness 200,
   readiness 503, no heartbeat and exactly one Agent record.
6. Approve through the authenticated API, stop Agent, remove the enrollment token
   from its private configuration, and restart. Require the exact saved identity,
   unchanged identity file, one Agent record, authenticated heartbeat and readiness.
7. Copy the committed [stdlib Go fixture](fixtures/stdlib-build/) into the synthetic
   repository, append an actual compiler error, and commit that failing revision.
   The browser signs in, creates one project and pipeline, reviews and starts the
   run, then requires the actual compiler error, failed test step and zero artifacts.
8. Independently read that run, its Agent checkout/step evidence, compiler logs and
   exact-run artifact list through authenticated APIs. Only then fix the source in
   a new Git commit and advance the local bare repository's main branch. The Git
   daemon itself remains read-only. The browser must review that new full revision
   and trigger a distinct run on the same pipeline.
9. The real Agent checks the pinned Go version and effective environment, runs
   `go test -count=1 ./...`, builds `stdlib-demo`, executes valid/invalid inputs,
   creates `manifest.json` and publishes only those two files. Each stage must
   succeed; no marker or prebuilt fixture substitutes for compilation.
10. The browser checks all real job/step statuses, actual logs and full checkout
    SHA, downloads both artifacts, and independently validates the manifest,
    binary length/SHA-256, pinned compiler and fixed expected program output.
    Existing reload, mobile/small-mobile/desktop controls, repeated downloads and
    sign-out/sign-in assertions remain. The original failed run must still fail
    and still have no artifacts after the successful run.
11. After the browser exits successfully, the Python harness separately verifies
    the successful API run and checkout, compares browser bytes with fresh
    authenticated API downloads, and executes an exclusive private copy of exactly
    those verified download bytes with a five-second timeout. This is limited to
    the reviewed synthetic program, never arbitrary user artifacts. Browser code
    never executes a downloaded binary.

The browser alone owns the `ci-admin` session. Before it starts, the fixture uses
the existing administrator APIs to create a separate `ci-build-observer` identity
and a custom role with **only `pipeline:read`**. The invitation API grants this at
platform scope; this deliberately applies only to the fresh disposable synthetic
database, not a production observer or a general service account. Its initial
test password is set through the authorized user API. It is not bootstrapped as
another administrator. Tokens stay in memory and are redacted from diagnostics;
none are written to coordination files, browser environment or uploaded evidence.

This separation preserves the product's one-active-token-per-user behavior:
relogin must reject the same observer's old token while leaving the administrator
session valid. Real API assertions require observer management access to return
HTTP 403, pipeline triggering to return the existing HTTP 200/application 4030
denial, and observer reads to remain authenticated after browser login/logout.
The fixture never repairs a failed read by logging the administrator in again or
weakening authorization. Offline transport tests check these assertion sequences;
actual Control/Redis session behavior still requires the real CI journey.

## Versioned browser and binary contract

`ARCENTRA_ACCEPTANCE_CONTRACT=stdlib-build-v1` requires the matching frontend
acceptance client. The frontend preserves its earlier text-artifact contract for
older backends; this backend never silently falls back to marker/text acceptance.
The existing `ARCENTRA_ACCEPTANCE_COMMIT` now supplies the initial failing full
source SHA. Additional values are fixed: `ARCENTRA_ACCEPTANCE_ARTIFACT_NAME` is
`stdlib-demo`, `ARCENTRA_ACCEPTANCE_MANIFEST_NAME` is `manifest.json`,
`ARCENTRA_ACCEPTANCE_GO_VERSION` is `go1.25.6`, and
`ARCENTRA_ACCEPTANCE_EXPECTED_OUTPUT` is the fixture's documented JSON plus newline.

`ARCENTRA_ACCEPTANCE_COORDINATION_DIR` points to a private mode-0700 directory
inside the disposable fixture root, outside public evidence. Atomic mode-0600
`failed-run.json` and `successful-run.json` contain exactly `schemaVersion: 1`,
`projectId`, `pipelineId`, `runId` and `sourceCommit`. After independent failure
verification and a new commit, the harness writes `fixed-source.json` with exactly
`schemaVersion: 1` and `sourceCommit`. Files must be bounded, regular, private and
non-symlink/non-hardlink. The browser saves downloads under the private
`downloads/` directory. These files are not uploaded and disappear at cleanup.

The unsigned manifest contains exactly `schemaVersion`, full `sourceCommit`,
`goVersion`, `binary: {name, sizeBytes, sha256}` and
`behavior: {args, stdout, stdoutSha256}`. Both harnesses independently know the
expected output; a producer cannot redefine it through manifest/config fields.
The manifest is an integrity record, not a provenance signature or attestation.

The Agent is deliberately **trusted baremetal execution**, not sandbox isolation.
The tiny repository fixture is authored by this test and executes only on the
disposable runner. No arbitrary remote repository is accepted. Organization
onboarding remains incomplete in the product: the current administrator project
API accepts the synthetic `ci-acceptance-org` value without an organization row.
The fixture explicitly records that limitation rather than seeding a hidden row.
Go module/toolchain resolution is disabled (`GOTOOLCHAIN=local`, `GOPROXY=off`,
`GOSUMDB=off`, `GOWORK=off`, `GOENV=off`, `CGO_ENABLED=0`); caches are private to
the job workspace and only the two exact output files are uploaded. This is not
host egress isolation, a network-none container build or a new runtime profile.

Private configuration, TLS keys, persisted identity, workspaces and raw logs are
removed when the fixture exits. Only sanitized process logs, a status/step receipt
and browser evidence are written in the requested evidence directory. The public
CI artifact allowlist adds only `browser/project-build-verification.json` to its
existing screenshots and browser summary. That success-only file contains fixed
contract/status fields, failed/fixed full source SHAs, synthetic run IDs, Go
version, binary name/length/digest, expected behavior/digest and passed checks. It
excludes paths, URLs, credentials, raw logs, arbitrary error strings and executable
downloads. Failed/incomplete runs do not write it; a stale generated success file
is removed on failure. Other local diagnostic files are not implicitly uploaded.
Browser failure, process failure, timeout or an unmet contract produces a failed result.
Implemented acceptance is not evidence that the real stack has passed; that claim
requires the corresponding GitHub-hosted run and its successful receipt.

At this candidate's implementation stage the workflow still pins the older
frontend client. The new combination cannot pass until the reviewed frontend
change is published and that immutable cross-repository SHA is updated through
the authorized release process. No new actual-stack CI or browser result is
claimed by the local tests below. See [the operator first-build runbook](FIRST_BUILD.md)
for the bounded setup path; full organization onboarding is still separate.

## Offline checks

The following check is safe without any running service and launches no browser,
container or listener:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/acceptance -p 'test_run_actual_app.py' -v
```

With the already installed pinned Go on PATH, this separate local source test
actually produces a compiler failure, commits the fix in a disposable local Git
repository, runs the real tests/build/behavior/manifest checks and verifies the
download-execution helper. It starts no services, listeners, containers or browser
and is not actual Agent evidence:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/acceptance -p 'test_stdlib_fixture_build.py' -v
```

`TestRegisterHashesDecodedPasswordForLogin` checks real persisted bcrypt/login
semantics without a session cache. `TestRegisterLoginWithRealRedis` adds the real
Redis session write only when `ARCENTRA_REAL_STACK=1` and the fixed Redis fixture
address are supplied. The actual application/browser fixture covers the complete
HTTP registration and login against MySQL and Redis.

The registration password correction applies to newly registered accounts.
Existing hashes are not rewritten and login does not try a weaker legacy fallback.
Accounts affected by the old base64-hashing defect need the existing authorized
password-reset process; the acceptance fixture does not modify existing accounts.
