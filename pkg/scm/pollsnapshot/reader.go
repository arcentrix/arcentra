// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Version 2 inventories are bounded consistency observations, not lossless histories.
// Two identical passes cannot recover intermediate updates between polls.
const Version = 2

func (r *Reader) Read(ctx context.Context, req Request) (Result, error) {
	zero := Result{}
	if r == nil || r.client == nil || !validSnapshotRepository(req.Repository) || req.ProviderProjectID < 0 || !validSnapshotTime(req.UpperBound) || (!req.LowerBound.IsZero() && (!validSnapshotTime(req.LowerBound) || req.LowerBound.After(req.UpperBound))) {
		return zero, failure(ReasonRequest)
	}
	ctx, cancel := context.WithTimeout(ctx, r.limits.ScanTimeout)
	defer cancel()
	req.LowerBound = req.LowerBound.UTC()
	req.UpperBound = req.UpperBound.UTC()
	s := scan{reader: r}
	endpoint := r.base.String() + "/repos/" + req.Repository
	id, err := s.repository(ctx, endpoint, req)
	if err != nil {
		return zero, err
	}
	req.ProviderProjectID = id
	first, err := s.list(ctx, endpoint+"/pulls", req)
	if err != nil {
		return zero, err
	}
	second, err := s.list(ctx, endpoint+"/pulls", req)
	if err != nil {
		return zero, err
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		return zero, failure(ReasonMovement)
	}
	if _, err = s.repository(ctx, endpoint, req); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, failure(ReasonTransport)
	}
	observations := make([]Observation, 0, len(first))
	for _, o := range first {
		if !o.UpdatedAt.Before(req.LowerBound) {
			observations = append(observations, o)
		}
	}
	out := Result{Version: Version, APIOrigin: r.base.Scheme + "://" + r.base.Host, Repository: req.Repository, ProviderProjectID: id, LowerBound: req.LowerBound, UpperBound: req.UpperBound, Observations: observations, Provider: r.provider}
	encoded, _ := json.Marshal(out)
	out.Digest = snapshotDigest(string(encoded))
	return out, nil
}

type wireRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

func (s *scan) repository(ctx context.Context, endpoint string, req Request) (int64, error) {
	body, _, err := s.get(ctx, endpoint)
	if err != nil {
		return 0, err
	}
	var repo wireRepo
	if json.Unmarshal(body, &repo) != nil || repo.ID <= 0 || repo.FullName != req.Repository || (req.ProviderProjectID != 0 && req.ProviderProjectID != repo.ID) {
		return 0, failure(ReasonIdentity)
	}
	return repo.ID, nil
}

type wireBranch struct {
	Ref    string    `json:"ref"`
	SHA    string    `json:"sha"`
	Repo   *wireRepo `json:"repo"`
	RepoID int64     `json:"repo_id"`
}
type wirePR struct {
	ID       int64      `json:"id"`
	Number   int64      `json:"number"`
	State    string     `json:"state"`
	Created  string     `json:"created_at"`
	Updated  string     `json:"updated_at"`
	MergedAt *string    `json:"merged_at"`
	Merged   *bool      `json:"merged"`
	MergeSHA string     `json:"merge_commit_sha"`
	Head     wireBranch `json:"head"`
	Base     wireBranch `json:"base"`
}

func (s *scan) list(ctx context.Context, endpoint string, req Request) ([]Observation, error) {
	base, _ := url.Parse(endpoint)
	query := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}, "per_page": {strconv.Itoa(s.reader.limits.PageSize)}}
	if s.reader.provider == "gitea" {
		query = url.Values{"state": {"all"}, "sort": {"oldest"}, "limit": {strconv.Itoa(s.reader.limits.PageSize)}}
	}
	observations := make([]Observation, 0)
	seenIDs, seenNumbers := map[int64]bool{}, map[int64]bool{}
	var lastCreated time.Time
	meta := snapshotPagination{total: -1, totalPages: -1, perPage: -1}
	if s.reader.provider == "github" {
		meta.canonicalPath = "/repositories/" + strconv.FormatInt(req.ProviderProjectID, 10) + "/pulls"
	}
	for page := 1; ; page++ {
		if s.pages >= s.reader.limits.MaxPages {
			return nil, failure(ReasonLimit)
		}
		s.pages++
		query.Set("page", strconv.Itoa(page))
		base.RawQuery = query.Encode()
		body, headers, err := s.get(ctx, base.String())
		if err != nil {
			return nil, err
		}
		wire, err := decodePage(body, s.reader.limits.PageSize)
		if err != nil {
			return nil, err
		}
		for _, pr := range wire {
			o, err := s.reader.normalize(req, pr)
			if err != nil {
				return nil, err
			}
			if seenIDs[o.ID] || seenNumbers[o.IID] || o.CreatedAt.Before(lastCreated) {
				return nil, failure(ReasonMovement)
			}
			seenIDs[o.ID] = true
			seenNumbers[o.IID] = true
			lastCreated = *o.CreatedAt
			observations = append(observations, o)
			if len(observations) > s.reader.limits.MaxItems {
				return nil, failure(ReasonLimit)
			}
		}
		more, err := meta.next(headers, base, page, len(wire), len(observations), s.reader.limits.PageSize)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ID < observations[j].ID })
	return observations, nil
}
func (r *Reader) normalize(req Request, w wirePR) (Observation, error) {
	zero := Observation{}
	created, ec := snapshotParseTime(w.Created)
	updated, eu := snapshotParseTime(w.Updated)
	if ec != nil || eu != nil || created.After(updated) || updated.After(req.UpperBound) || w.ID <= 0 || w.Number <= 0 || !snapshotSHA.MatchString(w.Head.SHA) || !snapshotSHA.MatchString(w.Base.SHA) || !validSnapshotBranch(w.Head.Ref) || !validSnapshotBranch(w.Base.Ref) || w.Head.Repo == nil || w.Base.Repo == nil || w.Head.Repo.ID <= 0 || !validSnapshotRepository(w.Head.Repo.FullName) || w.Base.Repo.ID != req.ProviderProjectID || w.Base.Repo.FullName != req.Repository || (w.MergeSHA != "" && !snapshotSHA.MatchString(w.MergeSHA)) {
		return zero, failure(ReasonMalformed)
	}
	if r.provider == "gitea" && (w.Head.RepoID != w.Head.Repo.ID || w.Base.RepoID != w.Base.Repo.ID || w.Merged == nil) {
		return zero, failure(ReasonIdentity)
	}
	if w.State != "open" && w.State != "closed" {
		return zero, failure(ReasonMalformed)
	}
	out := Observation{ID: w.ID, IID: w.Number, ProviderProjectID: req.ProviderProjectID, State: "opened", SourceBranch: w.Head.Ref, TargetBranch: w.Base.Ref, SourceSHA: w.Head.SHA, TargetSHA: w.Base.SHA, UpdatedAt: updated, CreatedAt: &created, SourceProjectID: w.Head.Repo.ID, SourceRepository: w.Head.Repo.FullName}
	if w.State == "closed" {
		out.State = "closed"
	}
	if w.MergedAt != nil {
		merged, err := snapshotParseTime(*w.MergedAt)
		if err != nil || (r.provider != "gitea" && merged.Before(created)) || merged.After(updated) || w.State != "closed" || w.MergeSHA == "" {
			return zero, failure(ReasonMalformed)
		}
		out.State = "merged"
		out.MergedAt = &merged
		out.MergeSHA = w.MergeSHA
	}
	if w.Merged != nil && *w.Merged != (out.MergedAt != nil) {
		return zero, failure(ReasonMalformed)
	}
	// GitHub's unmerged merge_commit_sha is a mutable test merge, not a merge result.
	scope := []string{r.provider + "-pr-snapshot-v2", r.base.String(), req.Repository, strconv.FormatInt(req.ProviderProjectID, 10), strconv.FormatInt(w.ID, 10), strconv.FormatInt(w.Number, 10)}
	out.Fingerprint = snapshotDigest(out.State, out.SourceSHA, out.SourceBranch, out.TargetBranch, out.TargetSHA, out.MergeSHA, strconv.FormatInt(out.SourceProjectID, 10), out.SourceRepository)
	out.ObservationID = r.provider + ":pr:v2:" + snapshotDigest(append(scope, updated.Format(time.RFC3339Nano), out.Fingerprint)...)
	if out.State == "merged" {
		out.MergeIdentity = r.provider + ":merge:v2:" + snapshotDigest(append(scope, out.MergedAt.Format(time.RFC3339Nano), out.MergeSHA)...)
		out.ObservationID = out.MergeIdentity
	}
	return out, nil
}

// decodePage caps typed allocations before decoding another array member. A byte
// cap alone does not bound the allocation amplification of many tiny objects.
func decodePage(body []byte, limit int) ([]wirePR, error) {
	if limit < 1 || limit > DefaultLimits().PageSize {
		return nil, failure(ReasonLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, failure(ReasonMalformed)
	}
	out := make([]wirePR, 0, limit)
	for decoder.More() {
		if len(out) >= limit {
			return nil, failure(ReasonLimit)
		}
		var item wirePR
		if err := decoder.Decode(&item); err != nil {
			return nil, failure(ReasonMalformed)
		}
		out = append(out, item)
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim(']') {
		return nil, failure(ReasonMalformed)
	}
	// Token checks trailing input without decoding another arbitrarily large value.
	if _, err = decoder.Token(); err != io.EOF {
		return nil, failure(ReasonMalformed)
	}
	return out, nil
}
