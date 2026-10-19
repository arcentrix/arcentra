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
package pollsnapshot
