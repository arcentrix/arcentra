#!/usr/bin/env bash
# Copyright 2026 Arcentra Authors. Licensed under the Apache License, Version 2.0.

# The environment may contain credentials. Never trace its values or the command.
set +x
set -euo pipefail

if (( $# == 0 )); then
    printf '%s\n' 'usage: with-clean-git-env.sh COMMAND [ARG ...]' >&2
    exit 2
fi

clean_git_environment() {
    local entry name environment_fd environment_pid

    # NUL delimiters preserve newlines in values. A dedicated descriptor leaves
    # the test command's stdin untouched; wait also checks producer failures.
    exec {environment_fd}< <(env -0)
    environment_pid=$!
    while IFS= read -r -d '' entry <&"$environment_fd"; do
        name=${entry%%=*}
        if [[ $name == GIT_* ]]; then
            # Bash preserves invalid environment names but cannot unset them.
            # Refuse to run rather than silently pass any Git override onward.
            if [[ ! $name =~ ^GIT_[a-zA-Z0-9_]*$ ]] || ! builtin unset -v "$name" 2>/dev/null; then
                printf '%s\n' 'cannot clear inherited Git environment; command not started' >&2
                return 1
            fi
        fi
    done
    exec {environment_fd}<&-
    if ! wait "$environment_pid"; then
        printf '%s\n' 'cannot inspect inherited environment; command not started' >&2
        return 1
    fi
}

clean_git_environment
exec -- "$@"
