# Visual CI definition acceptance

This fixture validates the web graph editor against the real definition API,
Git save path, scheduler and Agent. It is a disposable CI test, not a Git hosting
configuration or a deployment guide.

The normal compiler fixture repository remains read-only. An explicit
`--visual-editor-acceptance` flag prepares a second synthetic `visual.git`
repository, exports only these two exact repositories on the existing isolated
Git transport, and enables receive-pack only for `visual.git`. No credentials,
external remote, additional port or host networking permission is used.

The trusted fixture hook binds the initial full commit and permits exactly one
fast-forward commit on `refs/heads/main`, changing only a non-executable regular
`pipeline.json` file of at most 1 MiB. It rejects other branches/ref updates,
multiple commits, merges, symlinks, deletion and subsequent saves. Received packs
are limited to 2 MiB, including commit metadata and extra objects. Incoming files
cannot replace the hook and are never executed by it. The image profile retains
its read-only repository parent mount and grants write access only to the exact
synthetic bare-repository subdirectory. Both profiles retain owned cleanup.

The browser creates a pipeline in a dedicated disposable project, edits the
original command, adds a step and a dependent job, rejects a cycle, explicitly
validates, saves with the original full definition revision, reloads the saved
semantic definition, and separately requests execution. Editing or validation
must not trigger a job or grant deployment approval. The existing real compiler
failure/corrected-source/test/build/download journey remains independent.

Independent verification checks the actual bare repository tip, single-parent
history and exact changed file/content; the authenticated read-only observer
then verifies the exact saved-definition run, two distinct Agent jobs, three
successful steps and all three command outputs. No job source checkout is
configured in this extra fixture: a definition commit is not represented as a
checked-out build source commit. This shell-only fixture creates no artifacts.

The product supports only runtime capabilities validated by the backend. A
visual graph does not enable unsupported parallel container scheduling, online
dependencies, arbitrary OCI builds or bypass approval/retry restrictions.

Local Git and pure command/evidence tests do not establish actual hosted
receive-pack, image mount, browser or Agent success. Those require the exact
PR/main hosted acceptance results and the independent
`visual-ci-verification.json` receipt.
