# Per-job initial checkout provenance

## Rollout

Back up the intended database and test the additive migration on a staging copy. Explicitly run the existing migrator before deploying the updated controller:

```sh
arcentra-cli migrate --conf /private/arcentra/config.toml
```

Ordinary startup does not migrate tables. The migration adds job-level source expectation and result fields with empty identities and `unknown` provenance defaults. It never backfills a checkout from historical run revisions. Existing CLI authorization seeding is unchanged and does not grant an administrator. Production MySQL and production filesystem behavior require operator validation beyond the local SQLite/Git fixtures.

Deploy the controller before upgraded Agents. Old Agents remain compatible: valid terminal receipts without the new source result are accepted, and their checkout remains unknown. Historical jobs, jobs without an active Git source, clone/checkout failures, and controller-local jobs do not acquire an inferred commit.

## Evidence contract

Before publishing a remote job, the controller resolves its source once and atomically captures the selected Agent and immutable credential-free source identity. The versioned digest binds pipeline, pipeline-run and job-run IDs, source type, canonical repository identity, and exact requested branch/ref. The dispatched source parameters are the same resolved object; canonicalization does not rewrite the clone URL or branch. No explicit source-commit DSL feature is introduced. The unrelated `PipelineRun.CommitSha` may describe definition or trigger context and is never used as checkout evidence.

Source authentication stays outside the snapshot. Embedded URL credentials, query strings and fragments are rejected. Known source passwords/tokens and declared `secrets.*` values, including URL-decoded and common base64 forms, are rejected if they enter persisted metadata. This cannot discover arbitrary undeclared secrets; do not put secrets into repository paths or ref names.

The Agent recomputes and checks the binding before clone. The version-one binding rejects any extra explicit `Commit` argument. After the final initial clone/checkout, before artifact download or user steps, it reads a full 40- or 64-character Git commit and freezes the value. Same-workspace retries and steps that later change HEAD do not replace it. A missing legacy binding produces no source evidence.

The Agent includes that value and binding in its existing durable terminal protobuf receipt. The authenticated controller requires the original established job claim, matching immutable expectation, exact job identity and a full lowercase SHA. It commits checkout evidence, terminal receipt/status and artifacts in one transaction. Locked comparisons include execution and source fields and compare strings byte-for-byte even when the database collation is case-insensitive. Generic update methods cannot write source fields or reassign a captured source job. Queue retries cannot recapture a source or choose a new Agent for a claimed job.

An exact replay acknowledges only the same receipt and consistent persisted source evidence. Missing evidence remains explicitly unknown; unexpected, changed or mismatched evidence is rejected.

## Cancellation and limitations

A controller-cancelled job can receive checkout evidence only in the same original-claim-validated transaction that records its matching cancellation receipt. That operation does not replace the controller's cancellation status, reason, timing or artifacts. Cancellation before successful checkout has no evidence. A conflicting success/failure result is still handled by the existing terminal-conflict quarantine, which never grants source provenance to the database. Quarantined evidence is retained locally and is not a successfully committed checkout record.

The UI labels this **Agent-reported initial checkout** for each job. It is not a build attestation, verification by an independent party, or a statement about later workspace contents, images or artifacts. A trusted bare-metal Agent and user steps sharing its OS identity remain outside an isolation boundary. A full commit identifies the initial Git checkout, not effects caused by later commands.

Local tests cover source binding and credential rejection, capture before queue publication, immutability and case-insensitive database fencing, original Agent/claim validation, old receipts, atomic rollback, replay, cancellation, actual Git HEAD changes, lost acknowledgements and duplicate queue deliveries. No live SCM, model endpoint, production cluster or deployment is used.
