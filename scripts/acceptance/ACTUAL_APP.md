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
7. Create a synthetic Git repository containing a checkout marker and two
   Agent-executed steps. The browser signs in, creates the project and pipeline,
   triggers execution, checks the real source/logs and downloads the exact artifact.

The Agent is deliberately **trusted baremetal execution**, not sandbox isolation.
The tiny repository fixture is authored by this test and executes only on the
disposable runner. No arbitrary remote repository is accepted. Organization
onboarding remains incomplete in the product: the current administrator project
API accepts the synthetic `ci-acceptance-org` value without an organization row.
The fixture explicitly records that limitation rather than seeding a hidden row.

Private configuration, TLS keys, persisted identity, workspaces and raw logs are
removed when the fixture exits. Only sanitized process logs, a status/step receipt
and browser evidence are retained in the requested evidence directory. Browser
failure, process failure, timeout or an unmet contract produces a failed result.
Implemented acceptance is not evidence that the real stack has passed; that claim
requires the corresponding GitHub-hosted run and its successful receipt.

## Offline checks

The following check is safe without any running service and launches no browser,
container or listener:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/acceptance -p 'test_run_actual_app.py' -v
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
