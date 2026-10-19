# Agent source checkout isolation

Source checkout is preparation, before independent retry execution approval. It
must not run repository hooks, attribute filters, or inherited executable helpers.
The Agent now uses fixed Linux `/usr/bin/git` and child `PATH=/usr/bin:/bin`; system
Git, shell and helpers must be installed in operator-controlled system paths.
It does not modify host configuration, environment, permissions or trust stores.

Supported transports:
- HTTPS with the system trust store and existing `source.auth` credentials. No
  inherited proxy, custom CA environment, headers, helper or redirect settings.
- Anonymous HTTP to a literal loopback IP, for controlled local acceptance servers.
  The DNS name `localhost` is deliberately not accepted.
- Absolute local repository paths, for operator-controlled sources and fixtures.
  Local auth values are unused and never passed to the upload-pack subprocess.

Explicit compatibility changes: SSH, external remote helpers, nonloopback HTTP,
URL userinfo/query/fragment, relative local source paths and file URLs are rejected.
Submodules/gitlinks, LFS config, recognized canonical LFS pointers and checkout filter attributes are rejected
before checkout. Pointer recognition checks complete blobs up to 1024 bytes using
the current LFS v1 URL, LF lines and canonical field order; noncanonical/legacy
pointer-like text is ordinary content. Host filter execution is disabled separately
and does not depend on pointer recognition.
Local sources using config includes, worktree-config extensions, partial-clone or
promisor settings, common-directory indirection, object alternates, or symlinks at
the repository/config/HEAD/objects/info/pack roots are unsupported. This is not a
recursive audit of every object or ref file.
Local upload-pack and pack-objects use fixed system commands with a minimal child
environment; source repository config cannot select their executable hook.

Workspaces must be empty, with no existing `.git` directory/file, content, or
symlink ancestors. The Agent must own/control the parent directories throughout
checkout; this is not protection from a concurrent privileged host administrator.
Private HOME, templates, temporary files and authentication helper are removed at
completion. Credentials never enter command arguments, repository config or errors.
Git output is not included in failures, and inspected metadata output is bounded.
On Unix, cancellation kills the Git process group, including transport/local
helpers; tests verify closed HTTP connections and stopped descendant processes.

No claim/approval ordering changes are made here. Container Start and user commands
still require the existing independent approval admission. Local helper tests do
not replace the full taskqueue race suite or real container/image acceptance.
