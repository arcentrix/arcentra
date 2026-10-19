// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package pollsnapshot provides neutral bounded PR inventory observations.
//
// Supported contracts, consulted 2026-10-19:
//   - GitHub.com REST, pinned X-GitHub-Api-Version: 2026-03-10:
//     https://docs.github.com/en/rest/pulls/pulls
//     The list endpoint supports state=all, sort=created, direction=asc,
//     page and per_page; GitHub Enterprise/custom API origins are unsupported.
//   - Gitea 1.25 API v1 compatible deployments:
//     https://docs.gitea.com/api/1.25/operations/repo-list-pull-requests/
//     The list endpoint supports state=all, sort=oldest, page and limit.
//     PR head/base include ref, sha, repo_id and repository identity; merged,
//     merged_at and merge_commit_sha provide distinct merge evidence.
//
// Both adapters enumerate all states twice, with fixed creation ordering, and
// validate repository identity before and after. Bounds filter emitted updates
// only after both complete inventories compare equal. Missing terminal metadata
// requires an empty sentinel page, never an assumed terminal short page. Caps,
// malformed evidence, missing fork repositories, unsafe links, changed totals
// and moving inventories fail closed with a zero result. Page budget is shared
// by both passes, byte budget also includes repository identity requests.
//
// These observations are not an atomic snapshot or lossless event history.
// Two equal inventories cannot detect every concurrent change or recover rapid
// intermediate changes between polls. Baseline does not imply historical replay.
// Observed PR source/target revisions do not establish actual build checkout.
// Gitea manual-merge marking can report the commit author time as merged_at,
// even before PR creation. The raw provider value is preserved; it must not be
// treated as the time the merge was observed or used alone for polling cutoff.
// A merged_at later than updated_at is contradictory and still fails closed.
// See https://github.com/go-gitea/gitea/blob/v1.25.0/services/pull/merge.go
// (MergedManually calls SetMerged with commit.Author.When).
//
// Version 1 GitLab aliases preserve original JSON order/tags and omit all new
// fields. Version 2 identities explicitly bind provider, API base, repository
// path and stable repository/PR numeric identities. Titles and authors are not
// retained by the new readers; GitLab legacy fields remain wire compatible.
//
// Version 3 has separate Gitee.com v5 and Bitbucket Cloud 2.0 readers:
//   - https://gitee.com/api/v5/swagger and https://gitee.com/api/v5/oauth_doc
//     Gitee enumerates state=all, sort=created, direction=asc. It constructs
//     numeric pages locally and validates total_count/total_page and quoted
//     Link relations, including narrowly scoped harmless generated echoes.
//     Only an existing configured access_token is sent in the documented query
//     route; credential-bearing URLs and raw transport errors never escape.
//     Merged state plus merged_at is provider_state_timestamp evidence. Its
//     merge-result SHA is unavailable and never synthesized. Private Git
//     definition authentication is a separate readiness check, not established
//     by a successful API read.
//   - https://developer.atlassian.com/cloud/bitbucket/rest/api-group-pullrequests/
//     https://developer.atlassian.com/cloud/bitbucket/rest/intro/#pagination
//     https://support.atlassian.com/bitbucket-cloud/docs/using-api-tokens/
//     Bitbucket requests OPEN, MERGED, DECLINED and SUPERSEDED explicitly, with
//     sort=created_on. Body next is an opaque exact-origin/path continuation;
//     only a bounded ASCII page token may vary. Optional size/page metadata must
//     agree when supplied. Missing next terminates even a full page. Other
//     continuation shapes fail closed until separately supported.
//     Canonical brace-free UUIDs bind target and source repositories. Existing
//     tokens use Bearer authentication. MERGED plus full merge_commit.hash is
//     provider_state_commit evidence; updated_on never becomes merge time.
//
// V3 identities occupy an appended string-key lane; numeric identity fields
// remain zero. Gitee keys are positive decimal int64s; Bitbucket keys are
// lowercase nonnil UUIDs. Repository names use a bounded ASCII two-part subset:
// at most 100 bytes per part, no internal "..", .git suffix or leading hyphen.
// Source/base refs and full lowercase 40/64-hex hashes are
// mandatory; deleted forks or abbreviated hashes block the entire inventory.
// New-provider cutoffs use updatedAt, inclusively, after complete comparison;
// merge identities exclude mutable update timestamps. Both metadata reads and
// both inventory passes consume the same V3 request and aggregate-byte budgets.
// No live private-provider or authenticated Git acceptance follows from these
// synthetic readers, and self-hosted Gitee/Bitbucket are unsupported.
package pollsnapshot
