# Expanded product completion roadmap

This tracks the ten additional capability groups requested after the verified
pipeline/Agent/UI baseline. A passing milestone does not mean every group below
is implemented. Production credentials and external deployments are operator
configuration and require their own acceptance.

| Group | Deliverable | Current milestone |
| --- | --- | --- |
| Organization and members | Real organization directory, scoped member lifecycle and role changes, transactional live authorization | In implementation |
| Project settings | Versioned metadata editing and explicit archive/restore with admission enforcement | In implementation |
| Project Secrets | Scoped write-only create/rotate/revoke UI and execution-time version/scope resolution | Designed; implementation pending |
| Inbox and notifications | Durable personal inbox/read state, trusted event production and delivery history | Designed; implementation pending |
| Visual CI | Real DSL-backed graph editing, validation, advanced-field preservation/raw-mode fallback | Designed; implementation pending |
| Provider polling | Extend beyond current opt-in GitLab MR snapshots with provider-specific durable cursor semantics | Pending |
| Copilot | Authorized durable conversation history and explicit bounded action plans with approval | Private history and separately gated retry-approval execution implemented; [terminal whole-job new-execution wiring](copilot-terminal-whole-job-execution.md) implemented conditionally, release gate closed and operator settings default off; hosted acceptance pending |
| Network, OCI, parallel containers | Explicit egress/build/push policy and independent parallel job identities | Pending design and capability-specific acceptance; offline constrained runtime remains supported |
| Kubernetes delivery | Scoped YAML/Helm plans and explicit, auditable rollback | Pending; current existing-Deployment container release remains supported |
| Recovery and HA | Durable leases/fencing, ownership-safe adoption and explicitly supported resume boundaries | Pending; no transparent or exactly-once external-effect guarantee |

## First milestone acceptance

Use disposable synthetic users and projects to demonstrate organization creation,
metadata editing, scoped member add/change/remove, project settings editing and
archive/restore. Prove ordinary and revoked users cannot change permissions,
that concurrent membership decisions cannot remove the protected owner, and that
unversioned legacy mutations cannot bypass the new lifecycle rules.

Project archive rejects new run admission; already admitted work is not cancelled
or rolled back. New release side effects require a current active project. A
successful restore does not automatically resume polling or previously rejected
work. The exact implementation and SQL race evidence must be reviewed before
this behavior is marked accepted.

Membership changes only manage grants created by that workflow. Removing a
member does not silently erase separately assigned, inherited, or team grants.
Visibility metadata is not a permission grant. Existing arbitrary scope IDs are
not invented organization memberships.

## Acceptance boundaries

Each milestone requires local tests, independent authorization/correctness review,
exact-commit CI and post-merge verification. Synthetic MySQL/Kafka/S3/browser and
container checks use the existing disposable runner scope. Real registry pushes,
provider credentials, model data transmission and production cluster changes are
not implicit consequences of implementing these features.
