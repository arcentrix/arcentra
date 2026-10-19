// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Version3 = 3
const MergeEvidenceStateTimestamp = "provider_state_timestamp"
const MergeEvidenceStateCommit = "provider_state_commit"
const ReasonUnsupportedEvidence Reason = "unsupported_provider_evidence"

var v3UUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CanonicalProjectKey normalizes provider wire identities. Persisted/request keys
// must already equal the returned canonical value. No numeric UUID surrogate exists.
func CanonicalProjectKey(provider, raw string) (string, error) {
	switch provider {
	case "gitee":
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == raw {
			return raw, nil
		}
	case "bitbucket":
		normalized := strings.ToLower(raw)
		if strings.HasPrefix(normalized, "{") && strings.HasSuffix(normalized, "}") {
			normalized = normalized[1 : len(normalized)-1]
		}
		if v3UUID.MatchString(normalized) && normalized != "00000000-0000-0000-0000-000000000000" {
			return normalized, nil
		}
	}
	return "", failure(ReasonIdentity)
}

func (r *V3Reader) Read(ctx context.Context, req Request) (Result, error) {
	zero := Result{}
	if r == nil || r.client == nil || !validV3Repository(req.Repository) || req.ProviderProjectID != 0 || !validSnapshotTime(req.UpperBound) || !req.LowerBound.IsZero() && (!validSnapshotTime(req.LowerBound) || req.LowerBound.After(req.UpperBound)) {
		return zero, failure(ReasonRequest)
	}
	if req.ProviderProjectKey != "" {
		key, err := CanonicalProjectKey(r.provider, req.ProviderProjectKey)
		if err != nil || key != req.ProviderProjectKey {
			return zero, failure(ReasonRequest)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, r.limits.ScanTimeout)
	defer cancel()
	req.LowerBound = req.LowerBound.UTC()
	req.UpperBound = req.UpperBound.UTC()
	s := v3Scan{reader: r}
	path := "/repos/" + req.Repository
	if r.provider == "bitbucket" {
		path = "/repositories/" + req.Repository
	}
	endpoint := r.base.String() + path
	key, err := s.repository(ctx, endpoint, req)
	if err != nil {
		return zero, err
	}
	req.ProviderProjectKey = key
	first, err := s.list(ctx, endpoint, req)
	if err != nil {
		return zero, err
	}
	second, err := s.list(ctx, endpoint, req)
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
	out := Result{Version: Version3, APIOrigin: r.base.Scheme + "://" + r.base.Host, Repository: req.Repository, ProviderProjectKey: key, LowerBound: req.LowerBound, UpperBound: req.UpperBound, Observations: make([]Observation, 0, len(first)), Provider: r.provider}
	for _, o := range first {
		if !o.UpdatedAt.Before(req.LowerBound) {
			out.Observations = append(out.Observations, o)
		}
	}
	encoded, _ := json.Marshal(out)
	out.Digest = snapshotDigest(string(encoded))
	return out, nil
}
func (s *v3Scan) repository(ctx context.Context, endpoint string, req Request) (string, error) {
	body, _, err := s.get(ctx, endpoint)
	if err != nil {
		return "", err
	}
	var key, name string
	if s.reader.provider == "gitee" {
		var w giteeRepo
		if decodeV3Object(body, &w) != nil {
			return "", failure(ReasonIdentity)
		}
		key, err = CanonicalProjectKey("gitee", strconv.FormatInt(w.ID, 10))
		name = w.FullName
	} else {
		var w bitbucketRepo
		if decodeV3Object(body, &w) != nil {
			return "", failure(ReasonIdentity)
		}
		key, err = CanonicalProjectKey("bitbucket", w.UUID)
		name = w.FullName
	}
	if err != nil || name != req.Repository || req.ProviderProjectKey != "" && req.ProviderProjectKey != key {
		return "", failure(ReasonIdentity)
	}
	return key, nil
}
func (s *v3Scan) list(ctx context.Context, endpoint string, req Request) ([]Observation, error) {
	if s.reader.provider == "gitee" {
		return s.listGitee(ctx, endpoint+"/pulls", req)
	}
	return s.listBitbucket(ctx, endpoint+"/pullrequests", req)
}
func (r *V3Reader) identifyV3(req Request, out *Observation) {
	scope := []string{r.provider + "-pr-snapshot-v3", r.base.String(), req.Repository, req.ProviderProjectKey, strconv.FormatInt(out.ID, 10), strconv.FormatInt(out.IID, 10), out.MergeEvidence}
	merged := ""
	if out.MergedAt != nil {
		merged = out.MergedAt.Format(time.RFC3339Nano)
	}
	out.Fingerprint = snapshotDigest(out.State, out.SourceProjectKey, out.SourceRepository, out.SourceBranch, out.TargetBranch, out.SourceSHA, out.TargetSHA, out.MergeEvidence, merged, out.MergeSHA)
	out.ObservationID = r.provider + ":pr:v3:" + snapshotDigest(append(scope, out.UpdatedAt.Format(time.RFC3339Nano), out.Fingerprint)...)
	if out.State == "merged" {
		out.MergeIdentity = r.provider + ":merge:v3:" + snapshotDigest(append(scope, merged, out.MergeSHA)...)
		out.ObservationID = out.MergeIdentity
	}
}
func sortV3(observations []Observation) {
	sort.Slice(observations, func(i, j int) bool { return observations[i].ID < observations[j].ID })
}

// V3 provenance must fit the persisted branch contract and Git ref grammar.
func validV3Branch(name string) bool {
	return len(name) <= 256 && strings.TrimSpace(name) == name && name != "@" && validSnapshotBranch(name)
}

// Keep the reader's accepted namespace within persisted provider scope bounds.
func validV3Repository(name string) bool {
	if !validSnapshotRepository(name) || strings.Contains(name, "..") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if len(part) > 100 || strings.HasSuffix(part, ".git") {
			return false
		}
	}
	return true
}
