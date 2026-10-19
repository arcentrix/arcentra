// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

type giteeRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}
type giteeBranch struct {
	Ref  string     `json:"ref"`
	SHA  string     `json:"sha"`
	Repo *giteeRepo `json:"repo"`
}
type giteePR struct {
	ID       int64       `json:"id"`
	Number   int64       `json:"number"`
	State    string      `json:"state"`
	Created  string      `json:"created_at"`
	Updated  string      `json:"updated_at"`
	MergedAt *string     `json:"merged_at"`
	Merged   *bool       `json:"merged"`
	MergeSHA string      `json:"merge_commit_sha"`
	Head     giteeBranch `json:"head"`
	Base     giteeBranch `json:"base"`
}

// Gitee's shape resembles GitHub, but merged is a separate state and the v5
// contract does not provide a merge result SHA. Never substitute another hash.
func (r *V3Reader) normalizeGitee(req Request, w giteePR) (Observation, error) {
	zero := Observation{}
	created, ec := snapshotParseTime(w.Created)
	updated, eu := snapshotParseTime(w.Updated)
	if ec != nil || eu != nil || created.After(updated) || updated.After(req.UpperBound) || w.ID <= 0 || w.Number <= 0 || !snapshotSHA.MatchString(w.Head.SHA) || !snapshotSHA.MatchString(w.Base.SHA) || !validV3Branch(w.Head.Ref) || !validV3Branch(w.Base.Ref) || w.Head.Repo == nil || w.Base.Repo == nil || !validV3Repository(w.Head.Repo.FullName) {
		return zero, failure(ReasonMalformed)
	}
	source, es := CanonicalProjectKey("gitee", strconv.FormatInt(w.Head.Repo.ID, 10))
	target, et := CanonicalProjectKey("gitee", strconv.FormatInt(w.Base.Repo.ID, 10))
	if es != nil || et != nil || target != req.ProviderProjectKey || w.Base.Repo.FullName != req.Repository || (source == target) != (w.Head.Repo.FullName == req.Repository) {
		return zero, failure(ReasonIdentity)
	}
	if w.State != "open" && w.State != "closed" && w.State != "merged" {
		return zero, failure(ReasonMalformed)
	}
	out := Observation{ID: w.ID, IID: w.Number, State: w.State, SourceBranch: w.Head.Ref, TargetBranch: w.Base.Ref, SourceSHA: w.Head.SHA, TargetSHA: w.Base.SHA, UpdatedAt: updated, CreatedAt: &created, SourceRepository: w.Head.Repo.FullName, EvidenceVersion: Version3, ProviderProjectKey: target, SourceProjectKey: source, MergeEvidence: MergeEvidenceStateTimestamp}
	if w.State == "open" {
		out.State = "opened"
	}
	// An unexpected result hash is unsupported evidence, not an invitation to
	// silently change this version's capability or create mixed merge modes.
	if w.MergeSHA != "" {
		return zero, failure(ReasonUnsupportedEvidence)
	}
	if w.State == "merged" {
		if w.MergedAt == nil {
			return zero, failure(ReasonMalformed)
		}
		merged, err := snapshotParseTime(*w.MergedAt)
		if err != nil || merged.Before(created) || merged.After(updated) {
			return zero, failure(ReasonMalformed)
		}
		out.MergedAt = &merged
	} else if w.MergedAt != nil {
		return zero, failure(ReasonMalformed)
	}
	if w.Merged != nil && *w.Merged != (w.State == "merged") {
		return zero, failure(ReasonMalformed)
	}
	r.identifyV3(req, &out)
	return out, nil
}
func (s *v3Scan) listGitee(ctx context.Context, endpoint string, req Request) ([]Observation, error) {
	base, _ := url.Parse(endpoint)
	query := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}, "per_page": {strconv.Itoa(s.reader.limits.PageSize)}}
	observations := make([]Observation, 0)
	seenIDs, seenNumbers := map[int64]bool{}, map[int64]bool{}
	var lastCreated time.Time
	pagination := giteePagination{total: -1, totalPages: -1}
	for page := 1; ; page++ {
		query.Set("page", strconv.Itoa(page))
		base.RawQuery = query.Encode()
		body, headers, err := s.get(ctx, base.String())
		if err != nil {
			return nil, err
		}
		if err = validateV3JSON(body); err != nil {
			return nil, err
		}
		wire, err := decodeV3Array[giteePR](json.NewDecoder(bytes.NewReader(body)), s.reader.limits.PageSize)
		if err != nil {
			return nil, err
		}
		for _, pr := range wire {
			o, err := s.reader.normalizeGitee(req, pr)
			if err != nil {
				return nil, err
			}
			if seenIDs[o.ID] || seenNumbers[o.IID] || o.CreatedAt.Before(lastCreated) {
				return nil, failure(ReasonMovement)
			}
			if len(observations) >= s.reader.limits.MaxItems {
				return nil, failure(ReasonLimit)
			}
			seenIDs[o.ID] = true
			seenNumbers[o.IID] = true
			lastCreated = *o.CreatedAt
			observations = append(observations, o)
		}
		more, err := pagination.next(headers, base, page, len(wire), len(observations), s.reader.token, req.Repository)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	sortV3(observations)
	return observations, nil
}
