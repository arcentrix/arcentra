# Personal Inbox HTTP contract

Authenticated human-only routes, standard data envelope, `Cache-Control: no-store`.
No external sends. No client-specified recipient. No implicit approval/read action.

GET /api/v1/inbox?filter=all|unread&limit=20&cursor=opaque

Limit defaults to 20, range 1–50. Response: `{list: InboxItem[], nextCursor: string|null, scanIncomplete: boolean}`. No total. Each item:

- id: decimal string
- kind: run_result | approval_requested | approval_result
- status: success | failed | cancelled | pending | approved | rejected | expired
- occurredAt, deliveredAt: ISO timestamps; readAt: ISO timestamp or null
- target: `{type:run|approval,runId,pipelineId,projectId,approvalId?}` or null
- availability: available | unavailable | awaiting_resolution

Titles are fixed localized UI strings derived from kind/status. No source names,
comments, evidence, environment or logs. A pending approval past its deadline is
awaiting_resolution; its authoritative status is not changed. Historical scope
is immutable. Moved/deleted pipelines have no detail target.

GET /api/v1/inbox/unread-count: `{count:number,capped:boolean,incomplete:boolean}`.
Max count is 99. Capped means 99+; incomplete otherwise means the count is a lower
bound, never an exact count. Visibility is checked before counting.

POST /api/v1/inbox/:id/read with empty body or `{}`: `{id:string,readAt:ISO}`.
Explicit idempotent mutation. Foreign, unknown or currently hidden IDs all return
fixed 404. A failed mutation must leave UI read state unchanged.

Cursor tokens contain no IDs and require the current authenticated owner/filter.
They expire after 15 minutes. At most 32 active continuations per owner; oldest
continuations are evicted. Cleanup is bounded per request. Invalid, expired,
evicted or wrong-filter tokens return fixed 400 `invalid inbox cursor`, not an
empty page. Reload explicitly without cursor after displaying the error. The
client must clear data on identity changes or 401/403 and ignore stale responses.

Each request scans at most 200 delivery rows; an empty page may have nextCursor
and scanIncomplete. Continue explicitly to scan more. Server failures return
503 and never fabricate empty history or exact zero.
