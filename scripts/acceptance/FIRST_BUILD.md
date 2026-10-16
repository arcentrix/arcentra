# Operator-managed installation to first Go build

This is the current single-administrator, trusted-code **baremetal** path. It is
not organization self-service, production deployment automation, or a container
build guide. Never use the CI-only credentials or loopback repository in a real
installation. No step here grants permission to change production credentials,
network policy or privileges.

1. Prepare your approved MySQL, Redis, Kafka and object-storage services, private
   Control/Agent configuration, verified TLS, and built Control, Agent, CLI and
   frontend versions. Put the existing **Linux Go 1.25.6**, Git, Python 3 and shell
   on the Agent PATH. Stop if a prerequisite is missing; this sample never installs
   tools or opens egress. Follow the repository deployment documentation for your
   chosen installation rather than using the disposable CI compose file.
2. Run `arcentra-cli migrate --conf /path/to/private-control.toml`. Start Control
   and register the intended administrator using ordinary registration. Stop
   Control, explicitly run `arcentra-cli bootstrap-admin --conf
   /path/to/private-control.toml --username YOUR_SELECTED_ACCOUNT`, restart Control
   and sign in again. Ordinary registration does not grant administrator rights.
3. As that administrator, configure the default object store through the existing
   `/api/v1/storage/configs` API and Agent signing configuration through
   `/api/v1/settings/AGENT_SECRET_KEY`, then restart Control as documented for
   startup-bound configuration. Supply only values already approved for that
   installation. Saving configuration alone does not prove connectivity.
4. Use **Models → Agents** to create an enrollment token. Start the Agent with its
   approved private configuration, verified Control TLS and private identity
   directory. Review and explicitly approve the pending Agent in the Agent UI.
   Check that it becomes ready and reports heartbeats. Preserve its saved identity
   across restarts; do not copy a real enrollment token into source control.
5. Create a project in **Projects**. The current **Organization ID** field is an
   administrator-managed scope identifier, not an organization-creation form.
   Use a stable scope chosen for this installation and authorized for the account;
   creating a project still requires `org:create_project`. Do not copy the
   acceptance-only `ci-acceptance-org` value as an implied organization or grant.
   The **First build setup** panel can show saved configuration presence and a
   recently seen Agent only when your existing platform permissions allow it.
   Refresh explicitly after setup or permission changes. These observations do
   not prove connectivity, runtime capability, available capacity or eligibility
   for a particular pipeline. Restricted observations mean your account cannot
   inspect that information, not that the installation is unconfigured.
   **Authorized organization scopes** lists current role-grant scope IDs, not
   organization records or membership. A newly bootstrapped administrator may
   have no choices yet and can still explicitly enter an approved scope. The
   list is bounded; an omitted scope is not evidence of absent permission.
   Organization creation and membership administration remain outside this path.
6. Commit the [stdlib fixture source](fixtures/stdlib-build/) and its adapted
   `.arcentra/pipeline.yaml` to a repository already approved and reachable by
   Control and Agent. Private Git authentication remains an operator prerequisite;
   the sample does not obtain credentials or authorize new access.
7. From the project's pipeline page, create the pipeline with the repository,
   branch and definition path. Review the full definition revision, start a run,
   and inspect the toolchain, test, build, verify and publication steps. A Go
   version error means the pinned compiler is missing or not on the Agent PATH;
   fix the approved installation explicitly, rather than enabling auto-download.
   Project creation permission alone does not grant `pipeline:create` or
   `pipeline:trigger`; existing organization roles may lack them. Ask the
   installation administrator to review the necessary existing permissions if
   those actions are unavailable. This guide and the setup panel grant none.
8. Filter project artifacts by this run ID. Download `stdlib-demo` and
   `manifest.json`; verify full source revision, Go version, length, digest and
   expected output contract. Refresh and sign back in to confirm the stored run
   and artifacts. A separate failed compilation must publish no successful build
   outputs; correcting source requires a new commit and a new run.

The automated [actual application journey](ACTUAL_APP.md) exercises this path with
disposable dependencies and authored synthetic source. It configures storage and
Agent enrollment/approval through APIs; it does not claim those setup operations
have also passed browser acceptance. Successful local fixture tests alone do not
prove an actual Agent build. Exact backend/frontend CI revisions and successful
real-stack/browser receipts are required before calling that milestone accepted.
