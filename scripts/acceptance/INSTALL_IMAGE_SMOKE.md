# Install image loader and entrypoint gate (V24 first stage)

This is a **Linux amd64 loader/CLI/configuration-failure smoke**, not a healthy
Control/Agent startup test, complete installation acceptance, ARM64 acceptance,
or permission to publish images. Healthy synthetic Control/Agent integration
remains a separate follow-on gate. In particular the default Agent sandbox
configuration must not be interpreted as a healthy service scenario.

## Execution contract

Run only on a disposable GitHub-hosted Linux X64 runner, non-root, explicitly
opted in with `ARCENTRA_INSTALL_IMAGE_SMOKE=1`. The script verifies the Actions
runner/run/attempt/SHA variables and rejects custom Docker endpoints. These
variables are an accident-prevention guard, not an attestation against a caller
intentionally forging its environment. The workflow is the runner trust boundary.

The current job must generate all three binaries into the fixed checkout path
`dist/linux/amd64/{arcentra,arcentra-agent,arcentra-cli}`, with release-compatible
CGO build settings and these linker definitions:

- `github.com/arcentrix/arcentra/pkg/version.Version=$VERSION`
- `github.com/arcentrix/arcentra/pkg/version.GitCommit=$GITHUB_SHA`

Copy the checked-in `conf.d/*.toml` into `dist/linux/amd64/conf.d/`. No restored or
user-provided binary artifacts are intended. The harness verifies checkout SHA,
ELF class/machine, pinned go1.25.6 toolchain, CGO_ENABLED=1, trimpath,
Go GOOS/GOARCH and the Git VCS revision, hashes each
binary, and checks that packaged configurations exactly equal checked-in defaults.
Metadata checks establish consistency, not cryptographic build provenance.
Go 1.25.6 omits linker flags from trimpath build metadata, so those records cannot
prove embedded application version values. The supplied version is explicitly
recorded as expected. Git working-tree state must explicitly be clean
(`vcs.modified=false`); missing/dirty state fails the CI gate. Local uncommitted
development artifacts are therefore intentionally rejected. Build all three
binaries outside the checkout before copying them into dist, and disable Python
bytecode writes, to avoid generated files changing VCS state between builds.
Only the separate
container CLI version case verifies actual application version and commit.
Control/Agent binaries do not expose a version flag; their runtime versions are
not claimed to be verified.

Run:

```
python3 scripts/acceptance/run_install_image_smoke.py \
  --expected-version "$VERSION" --evidence-dir "$RUNNER_TEMP/install-image-evidence"
```

An independent `if: always()` step must run with the same opt-in/run environment:

```
python3 scripts/acceptance/run_install_image_smoke.py \
  --cleanup-only --evidence-dir "$RUNNER_TEMP/install-image-evidence"
```

Do not execute this smoke on a developer workstation or the cloud editor. Local
unit tests are subprocess fixtures and invoke no Docker, containers or privilege:

```
python3 -m unittest discover -s scripts/acceptance -p test_run_install_image_smoke.py -v
```

## What is exercised

A minimal private temporary build context includes the **actual `Dockerfile.ci`**,
the three verified binaries and default TOML files. Both real targets are built
locally with BuildKit and explicit linux/amd64/TARGETARCH=amd64. There is no
registry login, push, credential inheritance, arbitrary extra image or emulation.
The image OS, architecture, immutable image ID, entrypoint and empty Cmd are
checked. Startup executes that immutable image ID without replacing the original
ENTRYPOINT or mounting a host executable:

1. Append `-h`; require successful Go flag help containing the configuration flag.
2. Append `-conf /conf.d/acceptance-missing.toml`; require exit 1 and the precise
   application `failed to read configuration file: open ...: no such file or
   directory` error. Control must also load its packaged plugin config without a
   fallback warning. Missing loaders/binaries, generic ENOENT, OCI errors, OOM,
   timeout and arbitrary nonzero exits fail; they can never count as success.
3. In separate Control-image containers, invoke the image's own
   `/usr/local/bin/arcentra-cli --help` and `version`. Check version, commit and
   linux/amd64 JSON metadata. These CLI overrides are not startup evidence.

Separate diagnostic containers collect runtime libc/ldd information, while host
`readelf --program-headers --version-info` records artifact interpreter and ABI
requirements. Diagnostics cannot substitute for actual application execution.
This intentionally exposes, rather than repairs or masks, a potential newer
Ubuntu CGO builder versus Debian bookworm runtime GLIBC incompatibility.

The Debian bookworm-slim base is pinned in the actual Dockerfile to official index
`sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587`.
The official Docker Hub tag API was checked on 2026-10-16 at approximately 18:00 UTC:
https://hub.docker.com/v2/repositories/library/debian/tags/bookworm-slim
The reported amd64 manifest is
`sha256:a4672c0cb26fbdde88e38fa2dfb6c681942306680e41e4378b28770b6e79ee91`.
The Dockerfile's apt repositories remain time-dependent; this is not a fully
bit-reproducible build. No runtime compatibility claim follows from a digest pin.

## Bounds, cleanup and evidence

Application/diagnostic containers have network `none`, no port publishing or
mounts, all capabilities dropped, no-new-privileges, 512 MiB/one CPU/128 PID
bounds and a 30-second execution timeout. Only image build steps access public
base-image/apt sources. No host networking, socket mount, privileged container or
host security changes are used. Docker client uses the runner's ordinary local
daemon, with an empty temporary Docker config rather than inherited auth.

Every prospective resource is recorded before creation with a random bounded name
and owner label. Cleanup checks run/attempt/commit and ownership, deletes only
those named containers/image tags, and verifies absence. Normal exceptions and
SIGTERM trigger finally cleanup; the workflow's always cleanup is the second
line of defense for step termination. SIGKILL/runner loss cannot be handled by
Python; disposable runner destruction is the final boundary. Client/buildx processes run in an owned process group, killed and reaped on
interruption. BuildKit is enabled, but client termination does not prove daemon
solve termination. Interrupted builds persist an uncertainty marker, stop further
resource admission and fail cleanup closed; disposable runner destruction is
required to establish the final boundary. Base images/build cache belong to the disposable daemon lifetime;
no global prune or unrelated resource removal is performed.

The internal work deadline is 5 minutes, individual builds at most 3 minutes each,
and cleanup has a separate 1-minute budget. Workflow timeouts can be stricter.
Raw command records stay local, truncated to 16 KiB each; decoded command output
is capped at 4 MiB (subprocess output spools to a temporary local file).
Upload only these fixed allowlisted JSON files, not raw command records,
resource manifests, build contexts or TOML files:

- `evidence.json`: sanitized image identity, case exit/status/results, bounded
  libc/loader diagnostics and successful CLI version metadata
- `artifacts.json`: binary SHA256/expected-version/platform, observed Git revision/dirty state,
  explicit unverified per-binary runtime version, toolchain/CGO/trimpath facts,
  metadata-check flag, and bounded
  readelf GLIBC requirements/interpreter
- `summary.json`: overall pass/fail and explicit limited coverage
- `cleanup-summary.json`: independent cleanup outcome

A passing gate proves only the scope above. A failing gate blocks an installation
compatibility claim and preserves evidence for the next narrowly scoped repair.
