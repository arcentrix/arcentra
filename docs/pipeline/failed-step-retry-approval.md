# Approval-gated live failed-step retry (candidate, unavailable in production)

The independent compiled `taskqueue.FailedStepRetryApprovalAccepted` gate remains
false. Setting `allowFailedStepRetryApproval = true` is rejected by production
configuration validation even if ordinary live failed-step retry is enabled.
No combination of `allowWholeJobRetryApproval`, `allowWholeJobRetry`, or
`allowFailedStepRetry` enables this candidate.

Both Control `pipeline.offlineRuntime.allowFailedStepRetryApproval` and Agent
`agent.sandbox.containerd.policy.allowFailedStepRetryApproval` default to false.
Existing configuration templates may omit them; omitted means false. The examples
in [offline runtime acceptance](../offline-runtime-acceptance.md) show both policies.
After separate acceptance/release review, the mode additionally requires each
side's existing `allowFailedStepRetry` permission and actual live-session readiness.

The exact new evidence tuple is kind `retry_failed_step_v1`, evidence version 1,
plan version 2, retry mode `failed_step`, `retryApprovalRequired=true`, and a trusted
1–3600-second approval timeout matching the frozen approval policy. The manual
`builtin:retry-approval` gate accepts no caller-supplied parameters. All ordered
logical steps, globally unique preallocated attempt IDs and definition digests
remain bound, along with runtime, source commit and registered input identities.
The budget is per logical step and includes its first attempt. Whole-job v1 and
ungated failed-step v2 canonical formats and digest domains remain unchanged.

The new Agent capability is `containerdFailedStepRetryApprovalV1` (protobuf field
21). The whole-job approval and live failed-step capabilities retain their original
meanings. A missing, mistyped or unknown capability fails closed. Older Control
JSON decoders reject unknown fields; no transparent mixed-version upgrade is
promised. Upgrade Control before enabling any later accepted Agent capability.

This retains one live session and workspace, earlier successful steps, and partial
filesystem changes from failed commands. It does not provide rollback or recovery
across Agent restart. Background processes, zombies and `continueOnError` remain
unsupported. Approval is not a reusable permission to start later steps: admission
and execution must recheck current authority. Historical receipts preserve factual
ACKs but do not grant renewed execution. No new schema migration is required for
the approval kind; existing generation/input/step admission prerequisites remain.
