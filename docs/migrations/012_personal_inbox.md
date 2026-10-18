# 012: Personal in-app Inbox

Baseline: accepted Visual CI main b932f7c194d2d7617a39b2bbdcacdc38d58efed9,
which retains migration 011 from the Secrets release. Apply the existing explicit operator command
`arcentra-cli migrate --conf /private/path/config.toml` after backup, stopping old
Controls and pausing administrative writes. Test a representative copy first.
No startup AutoMigrate and no mixed-version operation are supported. MySQL DDL
implicitly commits: preflight precedes all DDL; inspect partial failures before
retrying. Rollback must preserve the added columns, delivery/read history and
unique dedupe keys. No automatic history purge or backfill is performed.

## Added storage

- pipeline_run: private default-empty inbox_actor_id, default-zero inbox_origin.
  Only the dedicated verified manual writer mints origin 1. Project scope remains
  the existing admission-only project_id. Generic writers/updates reject fields.
- approval_request: private default-zero inbox_origin, set only inside supported
  retry input-pinning/deployment release creation after historical proof checks.
  This marker accelerates discovery; the trusted run/binding is still authority.
- inbox_delivery: typed recipient/source/event/project/run/pipeline/status/times;
  nullable read_at. Unique recipient+source kind+source ID+event kind. Owner/read
  and owner/ID indexes support descending keyset scans.
- inbox_work: unique source identity; state/due index with source-kind prefix;
  revision fencing, separate requested/terminal progress, a durable next-recipient
  checkpoint for timeout fairness, bounded attempts and
  fixed error enum. No logs/reasons/credentials/arbitrary content.
- inbox_sweep: finite repair round upper bound and keyset position per source.
- inbox_cursor: random opaque 15-minute continuations, owner/filter-bound,
  maximum 32 active entries per owner, 100 expired entries removed per tick.
  Tokens never authenticate requests and contain no hidden source IDs. Cursor
  expiry/eviction is an explicit 400, not an empty successful page.
- composite origin+ID indexes on pipeline_run and approval_request.

Preflight rejects ambiguous source business IDs and either direction of partial origin/actor
shapes before DDL. Legacy source rows keep empty actors and zero markers, even
when TriggeredBy resembles a real user. Environment/secret data is neither read
nor rewritten by Inbox migration. No users, grants or external providers are
created by this slice (the migration command retains its pre-existing builtin
catalog seeding behavior, which does not grant permissions).

## Lifecycle, authorization and locks

Control starts InboxService with its managed context. Every five seconds it
repairs at most 100 indexed sources per type, claims at most 100 source work
items in alternating type order, and attempts at most 200 recipients. Each tick
has a five-second context. Stop cancels and joins before database cleanup.
Claims commit before domain reads; a 30-second lease and revision CAS prevent
stale workers from committing deliveries/progress. Exact immutable existing
deliveries count as progress even after revocation; read APIs still reauthorize.
All requested and terminal replays compare recipient, source/event, run, pipeline,
project, status and occurrence time. Conflicting existing rows fail closed without
marking work complete or rewriting delivery/read timestamps.
A checkpoint before each recipient prevents slow/denied prefixes from starving
later candidates across restarts. Conflicting existing event identities fail
closed, including terminal replay. Unique delivery insertion
never resets read_at or delivered_at. Unknown commit outcomes are retried from
durable facts; no run is resubmitted and no source status is manufactured.

Source validation reads exact trusted runs and recognized historical approval
bindings. It does not call mutating approval locks, expiry, execution admission,
or external adapters. Requested and terminal obligations are separate, including
approvals already terminal at first observation. Denied recipients remain due
with bounded backoff, permitting regrant catch-up. Permanently invalid sources
remain observable through fixed error codes and bounded aggregate log counts.

Lock order: pipeline -> original project -> exact users/teams -> permission
catalog/grants -> delivery/work CAS. Claims and final reschedules hold only work
locks; repair never takes domain locks. Cursor capacity locks the exact owner
before cursor writes, and never proceeds to domain locks. No work/delivery lock
is held while acquiring domain authorization. Authorization expiry is checked
again after a mutation before commit. Project archive alone is not revocation.
Moved/deleted pipeline history keeps pinned-scope checks but exposes no link.

The HTTP API scans at most 200 delivery rows per request. Revoked entries are
filtered before list/count semantics. Counts disclose incompleteness and cap at
99+. See ../INBOX_API.md for DTOs and explicit read behavior. HTTP/API failures
never stand in for empty history. No recipient field or public create endpoint.

## Verification and limits

SQLite unit/integration suites cover provenance, rollback/unknown commit replay,
read durability, ownership, revocation, source repair/wrap, fencing, real durable
retry pinning, deployment preparation/decision/cancellation and HTTP contracts.
Opt-in MySQL tests run only with explicit disposable local fixture DSNs:

- ARCENTRA_INBOX_MYSQL_DSN and ARCENTRA_REQUIRE_INBOX_MYSQL=1:
  TestManualInboxMySQLProvenanceBothCollations and
  TestInboxMySQLRealOldShapeMigrationAndLargeHistoryPlans. Fresh empty schema
  arcentra_inbox_test[_suffix]; tests own and clean only their fixture tables.
- Existing ARCENTRA_RETRY_APPROVAL_MYSQL_DSN and
  ARCENTRA_REQUIRE_RETRY_APPROVAL_MYSQL=1:
  TestInboxMySQLVerifiedRetryServiceJourney, using the established dedicated
  retry authorization fixture and real input pin/decision APIs.

Both supported MySQL collations are required. Local skipped tests are not MySQL
acceptance. Actual browser/Control restart and container-runtime proof are
separate hosted gates. External email/chat/webhook delivery is not implemented.
No notification is inferred from audit, log, plugin-only payload or crash alone.
Retain source/binding evidence until projection completes; future source purge
requires a separate retention/outbox contract.
