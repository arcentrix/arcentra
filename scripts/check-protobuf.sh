#!/usr/bin/env bash
# Copyright 2026 Arcentra Authors. Licensed under the Apache License, Version 2.0.
set -euo pipefail

buf="${1:?usage: check-protobuf.sh BUF PROTO_DIR}"
proto_dir="${2:?usage: check-protobuf.sh BUF PROTO_DIR}"
snapshot="$(mktemp -d)"
trap 'rm -rf "$snapshot"' EXIT
mkdir -p "$snapshot/committed" "$snapshot/generated"

cd "$proto_dir"
while IFS= read -r -d '' file; do
    mkdir -p "$snapshot/committed/$(dirname "$file")"
    cp "$file" "$snapshot/committed/$file"
done < <(find . -type f -name '*.pb.go' -print0)

# Generate outside the checkout so missing, stale and extra files are all detected.
"$buf" generate --template buf.gen.yaml --output "$snapshot/generated"
if ! diff -ru "$snapshot/committed" "$snapshot/generated"; then
    echo 'Protobuf files are out of date. Run make buf, remove obsolete .pb.go files, and commit the results.' >&2
    exit 1
fi
