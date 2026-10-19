// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package polling

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Persisted keys are strict: normalization belongs at the provider boundary,
// never while validating immutable evidence or a previously captured scope.
func validProjectKey(provider, key string) bool {
	if provider != "bitbucket" {
		id, err := strconv.ParseInt(key, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == key
	}
	if len(key) != 36 || key != strings.ToLower(key) || key[8] != '-' || key[13] != '-' || key[18] != '-' || key[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(key, "-", "")
	if len(compact) != 32 || compact == strings.Repeat("0", 32) {
		return false
	}
	_, err := hex.DecodeString(compact)
	return err == nil
}

func (o Observation) hasV3Evidence() bool {
	return o.EvidenceVersion != 0 || o.ProviderProjectKey != "" || o.SourceProjectKey != "" || o.MergeEvidence != ""
}

func (o Observation) validateV3() error {
	provider := ""
	switch o.MergeEvidence {
	case MergeEvidenceStateTimestamp:
		provider = "gitee"
	case MergeEvidenceStateCommit:
		provider = "bitbucket"
	}
	parts := strings.Split(o.SourceRepository, "/")
	updated, err := time.Parse(time.RFC3339Nano, o.UpdatedAt)
	if provider == "" || o.EvidenceVersion != ScopeVersion3 || o.SourceProjectID != 0 || !validProjectKey(provider, o.ProviderProjectKey) || !validProjectKey(provider, o.SourceProjectKey) || len(parts) != 2 || !v3RepositoryPart(parts[0]) || !v3RepositoryPart(parts[1]) || strings.Contains(o.SourceRepository, "..") || !commit(o.TargetSHA) || !commit(o.SourceSHA) || !v3Branch(o.SourceBranch) || !v3Branch(o.TargetBranch) || o.MRID <= 0 || o.MRIID <= 0 || !identifier(o.ObservationID, 2048) || err != nil || updated.IsZero() || updated.Year() < 1 || len(o.UpdatedAt) > 35 || updated.UTC().Format(time.RFC3339Nano) != o.UpdatedAt {
		return fmt.Errorf("invalid v3 polling observation provenance")
	}
	if provider == "bitbucket" && o.MRID != o.MRIID {
		return fmt.Errorf("invalid v3 polling repository-local pull request identity")
	}
	if o.State != "opened" && o.State != "merged" && o.State != "closed" {
		return fmt.Errorf("invalid v3 polling observation state")
	}
	if o.SquashSHA != "" {
		return fmt.Errorf("unexpected v3 polling squash provenance")
	}
	if o.State != "merged" {
		if o.MergedAt != "" || o.MergeSHA != "" {
			return fmt.Errorf("unexpected v3 polling merge provenance")
		}
		return nil
	}
	if provider == "gitee" {
		merged, err := time.Parse(time.RFC3339Nano, o.MergedAt)
		if err != nil || merged.IsZero() || merged.Year() < 1 || len(o.MergedAt) > 35 || merged.UTC().Format(time.RFC3339Nano) != o.MergedAt || merged.After(updated) || o.MergeSHA != "" {
			return fmt.Errorf("invalid v3 polling timestamp merge evidence")
		}
	} else if o.MergedAt != "" || !commit(o.MergeSHA) {
		return fmt.Errorf("invalid v3 polling commit merge evidence")
	}
	return nil
}

func v3RepositoryPart(name string) bool {
	return repositoryName(name) && !strings.HasPrefix(name, "-") && !strings.Contains(name, "..")
}

func v3Branch(name string) bool {
	if !identifier(name, 256) || !utf8.ValidString(name) || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// ValidateForScope binds evidence capabilities and repository identities to the
// trusted scope. A discriminator alone cannot relax a legacy provider's rules.
func (o Observation) ValidateForScope(scope Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if scope.Version != ScopeVersion3 {
		if o.hasV3Evidence() {
			return fmt.Errorf("v3 polling evidence in legacy scope")
		}
		return o.Validate()
	}
	if err := o.validateV3(); err != nil {
		return err
	}
	mode := MergeEvidenceStateTimestamp
	if scope.Provider == "bitbucket" {
		mode = MergeEvidenceStateCommit
	}
	if o.MergeEvidence != mode || o.ProviderProjectKey != scope.ProviderProjectID {
		return fmt.Errorf("polling evidence scope mismatch")
	}
	u, _ := url.Parse(scope.Repository)
	base := strings.TrimPrefix(u.Path, "/")
	if (o.SourceProjectKey == o.ProviderProjectKey) != (o.SourceRepository == base) {
		return fmt.Errorf("polling source repository identity mismatch")
	}
	return nil
}

// ValidateForScope verifies target evidence and its scope-bound deduplication key.
func (t Target) ValidateForScope(scope Scope) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := t.Observation().ValidateForScope(scope); err != nil {
		return err
	}
	req, err := EventRequestID(scope, t.ObservationID)
	if err != nil || req != t.RequestID {
		return fmt.Errorf("polling target request scope mismatch")
	}
	return nil
}

// CanonicalForScope validates every captured observation against the same scope.
func (b ObservedBatch) CanonicalForScope(scope Scope) (string, error) {
	d, err := scope.Digest()
	if err != nil || d != b.ScopeDigest {
		return "", fmt.Errorf("polling observed batch scope mismatch")
	}
	raw, err := b.Canonical()
	if err != nil {
		return "", err
	}
	boundary, _ := time.Parse(time.RFC3339Nano, b.NextCursor.Boundary)
	for _, o := range b.Observations {
		if err := o.ValidateForScope(scope); err != nil {
			return "", err
		}
		updated, _ := time.Parse(time.RFC3339Nano, o.UpdatedAt)
		if scope.Version == ScopeVersion3 && updated.After(boundary) {
			return "", fmt.Errorf("polling observation exceeds snapshot boundary")
		}
	}
	return raw, nil
}

// ParseObservedBatchForScope validates a persisted observation batch for replay.
func ParseObservedBatchForScope(raw string, scope Scope) (*ObservedBatch, error) {
	var b ObservedBatch
	if err := decode(raw, &b); err != nil {
		return nil, err
	}
	_, err := b.CanonicalForScope(scope)
	return &b, err
}

// CanonicalForScope validates every frozen target against the same scope.
func (b Batch) CanonicalForScope(scope Scope) (string, error) {
	d, err := scope.Digest()
	if err != nil || d != b.ScopeDigest {
		return "", fmt.Errorf("polling batch scope mismatch")
	}
	raw, err := b.Canonical()
	if err != nil {
		return "", err
	}
	boundary, _ := time.Parse(time.RFC3339Nano, b.NextCursor.Boundary)
	for _, t := range b.Targets {
		if err := t.ValidateForScope(scope); err != nil {
			return "", err
		}
		updated, _ := time.Parse(time.RFC3339Nano, t.UpdatedAt)
		if scope.Version == ScopeVersion3 && updated.After(boundary) {
			return "", fmt.Errorf("polling target exceeds snapshot boundary")
		}
	}
	return raw, nil
}

// ParseBatchForScope validates persisted targets for replay and admission.
func ParseBatchForScope(raw string, scope Scope) (*Batch, error) {
	var b Batch
	if err := decode(raw, &b); err != nil {
		return nil, err
	}
	_, err := b.CanonicalForScope(scope)
	return &b, err
}
