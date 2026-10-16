# A real, dependency-free Agent build

This authored synthetic Go program accepts one ASCII-letter name, normalizes it,
and prints JSON. It uses only the standard library. It does not open network
connections, start subprocesses, read credentials, or write host files. Its tests
exercise both normalization and invalid inputs. `encoding/json` here is the
standard-library fixture dependency, not a change to the backend's JSON policy.

The operator must already have **Linux Go 1.25.6**, Git, Python 3 and `/bin/sh` on
the trusted Agent's PATH. Missing or wrong Go fails; nothing installs a toolchain
or downloads a module. Each stage exports `GOTOOLCHAIN=local`, `GOPROXY=off`,
`GOSUMDB=off`, `GOWORK=off`, `GOENV=off`, `CGO_ENABLED=0`, read-only module mode,
and job-private `.cache` paths. The toolchain stage checks the process settings,
effective `go env`, and actual `go version`. Go reports an empty effective GOENV
filename when the process sets `GOENV=off`.

Copy these source files into an approved Git repository and commit them, then
copy `pipeline.example.yaml` to `.arcentra/pipeline.yaml` and replace its repository
placeholder with that same approved repository. The actual acceptance harness
generates this definition itself for its disposable read-only loopback Git server.

The pipeline runs five steps on the actual Agent:

1. `sh build.sh toolchain`: check the installed pinned compiler and effective guards.
2. `sh build.sh test`: run `go test -count=1 ./...`.
3. `sh build.sh build`: run `go build -trimpath -buildvcs=false -o stdlib-demo .`.
4. `sh build.sh verify`: execute the new binary, check valid and invalid input, and
   write `manifest.json` from the full checkout SHA and binary bytes.
5. Publish only `stdlib-demo` and `manifest.json`. No cache or home directory is uploaded.

Expected output for `./stdlib-demo arcentra` is exactly the following plus a newline:

```json
{"message":"HELLO, ARCENTRA!","length":8}
```

The unsigned manifest records `schemaVersion: 1`, `sourceCommit`, `goVersion`,
`binary: {name, sizeBytes, sha256}` and `behavior: {args, stdout, stdoutSha256}`.
It is an integrity record for this test, not a signed supply-chain attestation.

The acceptance harness first appends an undefined identifier to `main.go`, commits
it, and requires a real compiler failure with zero run artifacts. Only after that
failed run is independently verified does it commit the fix and let the browser
review and run the new revision. The browser downloads and validates both files;
the harness separately compares the downloaded bytes to the actual artifact API,
then executes only this reviewed, current-run synthetic program with a five-second
timeout. Browsers never execute downloaded binaries. Arbitrary user artifacts are
not authorized for execution by this fixture.

This is trusted **baremetal** execution. Disabled Go dependency resolution is not
network isolation, and does not establish offline container builds or untrusted
multi-tenant safety. See [the first-build runbook](../../FIRST_BUILD.md) and
[actual-stack acceptance](../../ACTUAL_APP.md) for scope and remaining proof.
