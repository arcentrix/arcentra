// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type bitbucketRepo struct {
	UUID     string `json:"uuid"`
	FullName string `json:"full_name"`
}
type bitbucketCommit struct {
	Hash string `json:"hash"`
}
type bitbucketBranch struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit     bitbucketCommit `json:"commit"`
	Repository *bitbucketRepo  `json:"repository"`
}
type bitbucketPR struct {
	ID          int64            `json:"id"`
	State       string           `json:"state"`
	Created     string           `json:"created_on"`
	Updated     string           `json:"updated_on"`
	Source      bitbucketBranch  `json:"source"`
	Destination bitbucketBranch  `json:"destination"`
	MergeCommit *bitbucketCommit `json:"merge_commit"`
}
type bitbucketPage struct {
	values              []bitbucketPR
	next                string
	size, page, pagelen int
}

func (r *V3Reader) normalizeBitbucket(req Request, w bitbucketPR) (Observation, error) {
	zero := Observation{}
	created, ec := snapshotParseTime(w.Created)
	updated, eu := snapshotParseTime(w.Updated)
	if ec != nil || eu != nil || created.After(updated) || updated.After(req.UpperBound) || w.ID <= 0 || !validV3Branch(w.Source.Branch.Name) || !validV3Branch(w.Destination.Branch.Name) || w.Source.Repository == nil || w.Destination.Repository == nil || !validV3Repository(w.Source.Repository.FullName) {
		return zero, failure(ReasonMalformed)
	}
	if !snapshotSHA.MatchString(w.Source.Commit.Hash) || !snapshotSHA.MatchString(w.Destination.Commit.Hash) {
		return zero, failure(ReasonUnsupportedEvidence)
	}
	source, es := CanonicalProjectKey("bitbucket", w.Source.Repository.UUID)
	target, et := CanonicalProjectKey("bitbucket", w.Destination.Repository.UUID)
	if es != nil || et != nil || target != req.ProviderProjectKey || w.Destination.Repository.FullName != req.Repository || (source == target) != (w.Source.Repository.FullName == req.Repository) {
		return zero, failure(ReasonIdentity)
	}
	state := ""
	switch w.State {
	case "OPEN":
		state = "opened"
	case "MERGED":
		state = "merged"
	case "DECLINED", "SUPERSEDED":
		state = "closed"
	default:
		return zero, failure(ReasonMalformed)
	}
	out := Observation{ID: w.ID, IID: w.ID, State: state, SourceBranch: w.Source.Branch.Name, TargetBranch: w.Destination.Branch.Name, SourceSHA: w.Source.Commit.Hash, TargetSHA: w.Destination.Commit.Hash, UpdatedAt: updated, CreatedAt: &created, SourceRepository: w.Source.Repository.FullName, EvidenceVersion: Version3, ProviderProjectKey: target, SourceProjectKey: source, MergeEvidence: MergeEvidenceStateCommit}
	if state == "merged" {
		if w.MergeCommit == nil || !snapshotSHA.MatchString(w.MergeCommit.Hash) {
			return zero, failure(ReasonUnsupportedEvidence)
		}
		out.MergeSHA = w.MergeCommit.Hash
	} else if w.MergeCommit != nil {
		return zero, failure(ReasonMalformed)
	}
	r.identifyV3(req, &out)
	return out, nil
}
func decodeBitbucketPage(body []byte, limit int) (bitbucketPage, error) {
	out := bitbucketPage{size: -1, page: -1, pagelen: -1}
	if err := validateV3JSON(body); err != nil {
		return out, err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return out, failure(ReasonMalformed)
	}
	seenValues := false
	for d.More() {
		key, _ := d.Token()
		switch key {
		case "values":
			out.values, err = decodeV3Array[bitbucketPR](d, limit)
			seenValues = true
		case "next":
			var v any
			v, err = d.Token()
			var ok bool
			out.next, ok = v.(string)
			if !ok || out.next == "" {
				err = failure(ReasonMalformed)
			}
		case "size":
			out.size, err = decodeV3Int(d, 0)
		case "page":
			out.page, err = decodeV3Int(d, 1)
		case "pagelen":
			out.pagelen, err = decodeV3Int(d, 1)
		default:
			nodes := 0
			err = walkV3JSON(d, 0, &nodes)
		}
		if err != nil {
			return out, err
		}
	}
	if !seenValues {
		return out, failure(ReasonMalformed)
	}
	return out, nil
}
func bitbucketInitial(endpoint string, pageSize int) *url.URL {
	u, _ := url.Parse(endpoint)
	u.RawQuery = url.Values{"state": {"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}, "sort": {"created_on"}, "pagelen": {strconv.Itoa(pageSize)}}.Encode()
	return u
}

// A next link is an opaque continuation: it is never generated as page+1.
// Only a bounded page token may vary. Unknown continuation parameters fail closed.
func bitbucketNext(raw string, initial *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, failure(ReasonPagination) }
	if len(raw) > 8192 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\\r\n\x00") {
		return bad()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != initial.Scheme || u.Host != initial.Host || u.Path != initial.Path || u.RawPath != "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return bad()
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad()
	}
	page := query["page"]
	if len(page) != 1 || len(page[0]) < 1 || len(page[0]) > 512 {
		return bad()
	}
	// Hash/string page cursors are supported without interpreting or incrementing.
	for _, r := range page[0] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.~", r)) {
			return bad()
		}
	}
	query.Del("page")
	states := query["state"]
	sort.Strings(states)
	expected := initial.Query()
	sort.Strings(expected["state"])
	if query.Encode() != expected.Encode() {
		return bad()
	}
	return u, nil
}
func (s *v3Scan) listBitbucket(ctx context.Context, endpoint string, req Request) ([]Observation, error) {
	initial := bitbucketInitial(endpoint, s.reader.limits.PageSize)
	current := initial
	observations := make([]Observation, 0)
	seenIDs := map[int64]bool{}
	seenLinks := map[string]bool{}
	var lastCreated time.Time
	total := -1
	lastPage := -1
	for {
		// Compare normalized query sets for cycle detection without altering the
		// actual continuation sent to the API.
		query := current.Query()
		sort.Strings(query["state"])
		signature := current.Path + "?" + query.Encode()
		if seenLinks[signature] {
			return nil, failure(ReasonPagination)
		}
		seenLinks[signature] = true
		body, _, err := s.get(ctx, current.String())
		if err != nil {
			return nil, err
		}
		page, err := decodeBitbucketPage(body, s.reader.limits.PageSize)
		if err != nil {
			return nil, err
		}
		if page.pagelen > s.reader.limits.PageSize || page.pagelen >= 0 && len(page.values) > page.pagelen || page.page >= 0 && lastPage >= 0 && page.page != lastPage+1 {
			return nil, failure(ReasonPagination)
		}
		if page.page >= 0 {
			if raw := current.Query().Get("page"); raw != "" {
				if n, e := strconv.Atoi(raw); e == nil && n != page.page {
					return nil, failure(ReasonPagination)
				}
			} else if current == initial && page.page != 1 {
				return nil, failure(ReasonPagination)
			}
			lastPage = page.page
		} else {
			lastPage = -1
		}
		if page.size >= 0 {
			if total >= 0 && total != page.size {
				return nil, failure(ReasonPagination)
			}
			total = page.size
		}
		for _, pr := range page.values {
			o, err := s.reader.normalizeBitbucket(req, pr)
			if err != nil {
				return nil, err
			}
			if seenIDs[o.ID] || o.CreatedAt.Before(lastCreated) {
				return nil, failure(ReasonMovement)
			}
			if len(observations) >= s.reader.limits.MaxItems {
				return nil, failure(ReasonLimit)
			}
			seenIDs[o.ID] = true
			lastCreated = *o.CreatedAt
			observations = append(observations, o)
		}
		if total >= 0 && len(observations) > total {
			return nil, failure(ReasonPagination)
		}
		if page.next == "" {
			if total >= 0 && len(observations) != total {
				return nil, failure(ReasonPagination)
			}
			break
		}
		if len(page.values) == 0 || total >= 0 && len(observations) >= total {
			return nil, failure(ReasonPagination)
		}
		current, err = bitbucketNext(page.next, initial)
		if err != nil {
			return nil, err
		}
	}
	sortV3(observations)
	return observations, nil
}
