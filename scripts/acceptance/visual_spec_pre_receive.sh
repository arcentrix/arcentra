#!/bin/sh
# Fixed synthetic CI repository guard. Never installed on a user repository.
# Git invokes this trusted file from the fixture-owned bare repository; received
# commits cannot replace it. No content from a commit is evaluated or executed.
set -eu
reject() { printf '%s\n' 'synthetic spec write rejected' >&2; exit 1; }
IFS=' ' read -r old new ref || reject
[ "$ref" = refs/heads/main ] || reject
[ "$old" = "$(git rev-parse --verify refs/arcentra/fixture-base)" ] || reject
case "$old:$new" in *[!0-9a-f:]* ) reject ;; esac
[ "${#old}" = 40 ] && [ "${#new}" = 40 ] || reject
[ "$old" != 0000000000000000000000000000000000000000 ] || reject
[ "$new" != 0000000000000000000000000000000000000000 ] || reject
# A push must contain exactly one update, even if a later line is empty.
if IFS= read -r extra; then reject; fi
[ "$(git rev-list --count "$old..$new")" = 1 ] || reject
[ "$(git rev-parse "$new^")" = "$old" ] || reject
# Exactly one parent, one ordinary file, one intended path. In particular reject
# merge commits, source changes, symlinks, submodules and executable definitions.
[ "$(git rev-list --parents -n 1 "$new" | awk '{print NF}')" = 2 ] || reject
[ "$(git diff-tree --no-commit-id --name-only -r "$old" "$new")" = pipeline.json ] || reject
entry=$(git ls-tree "$new" -- pipeline.json) || reject
case "$entry" in '100644 blob '* ) ;; * ) reject ;; esac
size=$(git cat-file -s "$new:pipeline.json") || reject
case "$size" in ''|*[!0-9]* ) reject ;; esac
[ "$size" -gt 0 ] && [ "$size" -le 1048576 ] || reject
