#!/bin/sh
# A trusted baremetal build: module downloads are disabled, not host networking.
set -eu
umask 077
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOENV=off
export CGO_ENABLED=0 GOTELEMETRY=off GOFLAGS=-mod=readonly GOMAXPROCS=2
export GOPATH="$PWD/.cache/go-path" GOMODCACHE="$PWD/.cache/go-mod"
export GOCACHE="$PWD/.cache/go-build" GOTMPDIR="$PWD/.cache/go-tmp"
mkdir -p "$GOPATH" "$GOMODCACHE" "$GOCACHE" "$GOTMPDIR"
command -v go >/dev/null 2>&1 || { echo 'Go go1.25.6 must already be installed on the Agent PATH' >&2; exit 1; }
version="$(go env GOVERSION)"
[ "$version" = go1.25.6 ] || { echo "Agent requires go1.25.6; found $version" >&2; exit 1; }
case "${1:-}" in
  toolchain)
    printf 'ACTUAL_PROJECT_TOOLCHAIN %s\n' "$version"
    python3 verify.py toolchain
    ;;
  test)
    echo 'ACTUAL_PROJECT_TEST go test -count=1 ./...'
    go test -count=1 ./...
    ;;
  build)
    echo 'ACTUAL_PROJECT_BUILD go build -trimpath -buildvcs=false -o stdlib-demo .'
    go build -trimpath -buildvcs=false -o stdlib-demo .
    ;;
  verify)
    python3 verify.py
    ;;
  *) echo 'usage: sh build.sh {toolchain|test|build|verify}' >&2; exit 2 ;;
esac
