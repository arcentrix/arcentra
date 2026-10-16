# Durable approval of an exact offline retry plan

This optional gate is separate from Kubernetes deployment approval and the legacy
in-memory generic approval plugin. It is disabled by default. It supports only
`containerd-offline-v1` with `whole_job` retry; combining this approval with
container `failed_step` remains rejected. It adds no network, host socket, privilege, concurrency or automatic
restart capability.

## Upgrade and operator configuration

Apply additive [migration 007](../migrations/007_retry_approval_binding.md)
explicitly after migration 006, with backup and a reviewed configuration. Startup
never migrates or backfills authority. Upgrade the matching Controller and Agent
before creating gated runs. Existing ungated execution keeps its prior contract.

Both existing offline runtime and whole-job retry policies must already be
configured. Enable `allowWholeJobRetryApproval = true` in both Control's
`pipeline.offlineRuntime` and the Agent's
`agent.sandbox.containerd.policy`. The new flag defaults to false. The selected
Agent must advertise the matching capability and verify the Controller TLS
certificate using the existing TLS configuration. Plaintext responses cannot
authorize this gate. No certificates or credentials are generated automatically.

The Control additionally needs a separate trusted policy, for example:

```toml
[pipeline.retryApproval]
id = "reviewed-build-policy"
candidates = ["EXISTING_APPROVER_USER_ID_A", "EXISTING_APPROVER_USER_ID_B"]
mode = "all"
requiredApproverCount = 2
timeoutSeconds = 300
```

These are placeholders for existing authorized users, not new accounts. The
server computes the policy fingerprint. Candidates must have current project
and pipeline read access plus the applicable approval permission. The requester
must have current pipeline trigger authority and cannot approve their own work.
Filtering ineligible candidates cannot reduce the configured quorum. Policy
changes require restart of the immutable configuration snapshot; database
permission changes remain live admission checks.

## Pipeline definition

```yaml
jobs:
  - name: reviewed-build
    approval:
      required: true
      type: manual
      plugin: builtin:retry-approval
      params: {}
    retry:
      mode: whole_job
      maxAttempts: 2
      delay: 1s
      idempotent: true
      allowedExitCodes: [23]
    # source, immutable upstream inputs and explicit steps follow the ordinary
    # offline profile. Dependencies must be available without container networking.
```

`params` may be absent or empty. Candidate lists, claimed identities, quorum,
callback URLs or a client-provided approval result are not accepted in the DSL.
The exact manual gate is required; arbitrary legacy approval plugins remain
unsupported. Only authenticated manual triggers can create this gate. SCM
webhooks and unverified internal submissions cannot impersonate that requester.

## What the reviewer authorizes

The Agent first obtains only a preparation workspace, resolves the full source
commit and pins the registered cumulative input manifest. The controller creates
one durable approval in the same input-pinning transaction. Before quorum there
is no container creation/start, pipeline shell execution, upload or retry advance.
Trusted source checkout and input staging are preparation, not execution of
repository scripts or hooks.

Run approvals shows the full source SHA separately from the pipeline definition
revision, the resolved-definition digest, every registered input and producer,
content SHA256/length/storage identity, immutable runtime image and limits, all
preallocated step attempts, exit-code policy and total attempt limit. The limit
includes the first round. Step definitions are bound by digests; raw command
arguments and secret environment values are not displayed. This is a frozen
execution-plan review, not a supply-chain attestation.

A vote is recorded under the authenticated user's identity. Review is refreshed
before a browser submits a reason-only decision. Duplicate votes are idempotent;
read-only refresh does not dispatch or resume work. A recorded Approved status
is historical and does not certify current execution permission.

## Deadlines, revocation and acknowledgement loss

The original expiry is the earliest of the configured 1–3600 second budget, the
job deadline and any bound input expiration. It is never extended by polling,
network failure, another vote, an ACK replay or a new generation. The immutable
queued plan carries the wait budget so total RPC outage also remains bounded.
While waiting, a single-capacity Agent holds its private workspace and queue
slot; it does not hold a running container. Capacity is not increased.

Every fresh admission and Step RUNNING/advance transaction rechecks the original
requester, actual approving voters, live roles/grants/team membership, policy,
inputs, claim and generation. Permission or policy changes, input changes,
expiry or cancellation deny subsequent work. A command already admitted can
have effects; revocation is not an instantaneous rollback guarantee. Existing
cancellation performs owned shutdown/cleanup, never compensating side effects.

Startup admission uses a separate durable receipt, not the preparation receipt.
A lost startup ACK permits at most three byte-identical transport attempts within
the original deadline, only for eligible transient RPC failures. Every response
must freshly authorize execution. Historical receipts and votes remain
inspectable after revocation; replaying them alone never grants permission.
Unconfirmed admission, cleanup or ownership fails closed.

If an approved retry successor is explicitly refused, the Agent may inspect the
exact original claim and already-cleaned predecessor over authenticated TLS. Only
an unchanged predecessor with no execution permission permits an attempt to
finalize that predecessor as failed. The observation is not a durable revocation
of the advance request: the existing current-generation transaction arbitrates
late advance, cancellation and terminal reports. If a successor already exists,
a competing transition wins, inspection is uncertain, or persistence fails, the
Agent retains the original intent and receipts for read-only/offline recovery.
It does not start a successor or discard evidence. The journal is cleared only
after the final terminal acknowledgement is durably confirmed.

Restart does not resume this wait or execute a new generation. Preserve journals
and receipts, use documented read-only inspection/offline recovery when needed,
and create a separately authorized fresh run. Changing the source, input set,
commands or attempt limit requires a fresh captured run and approval.

## Acceptance boundaries

Ordinary tests cover canonical binding, live authorization, UI scope/preflight,
fixed waiting budgets, cancellation and replay. Dedicated disposable MySQL tests
exercise actual row-lock/current-read contention with both repository predicates
and the full service authorization graph. The existing privileged container
acceptance runs real Process → Kafka → authenticated TLS Agent → MySQL →
containerd → S3 with synthetic voters and inputs. An acceptance-only SDK observer
records exact container/task creation and init-start RPC attempts without
changing them, proving no such calls across pending/canceled lifecycles and
positive calls after quorum. It is absent from normal builds.

Test results must name the exact tested commit and distinguish fixture-driven UI
checks from real backend/browser journeys. No production cluster or external
model is configured by these tests. The exact whole-job approval implementation
passed at merged backend commit `bda78247222a48ac1a0425e64769fda73e2ba129`:
[ordinary/actual-browser CI](https://github.com/arcentrix/arcentra/actions/runs/38673170855)
and [dedicated runtime CI](https://github.com/arcentrix/arcentra/actions/runs/38673170856).
These V17 results do not authorize approval-bearing `failed_step` plans. The
separate [V19 failed-step approval contract](failed-step-retry-approval.md) records
its own closed-gate implementation evidence and successful exact gate-enabled
ordinary-entry acceptance. Whole-job approval remains a distinct kind and capability.
