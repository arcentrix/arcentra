// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
)

// SourceCheckoutExpectation is immutable dispatch intent, never checkout proof.
// SealID is allocated before sealing the payload; embedding SealDigest here
// would introduce a payload/seal digest cycle. Claim identity is bound separately.
type SourceCheckoutExpectation struct {
	Version              int    `json:"version"`
	ScheduleID           string `json:"scheduleId"`
	SealID               string `json:"sealId"`
	ExpectedSourceCommit string `json:"expectedSourceCommit"`
}

func (e *SourceCheckoutExpectation) Canonical() (string, error) {
	if e == nil || e.Version != 1 || !canonicalUUID(e.ScheduleID) || !canonicalUUID(e.SealID) || e.ScheduleID == e.SealID || !sourceprovenance.ValidCommit(e.ExpectedSourceCommit) || strings.ToLower(e.ExpectedSourceCommit) != e.ExpectedSourceCommit {
		return "", fmt.Errorf("invalid source checkout expectation")
	}
	b, err := json.Marshal(e)
	return string(b), err
}
func ParseSourceCheckoutExpectation(body string) (*SourceCheckoutExpectation, error) {
	if len(body) > 1024 {
		return nil, fmt.Errorf("oversized source checkout expectation")
	}
	var e SourceCheckoutExpectation
	if json.Unmarshal([]byte(body), &e) != nil {
		return nil, fmt.Errorf("invalid source checkout expectation")
	}
	canonical, err := e.Canonical()
	if err != nil || canonical != body {
		return nil, fmt.Errorf("noncanonical source checkout expectation")
	}
	return &e, nil
}

// Digest includes the authenticated original claim, not the replaceable lease.
func (e *SourceCheckoutExpectation) Digest(identity JobInputIdentity) (string, error) {
	body, err := e.Canonical()
	if err != nil {
		return "", err
	}
	if identity.Validate() != nil || identity.SourceBindingDigest == "" {
		return "", fmt.Errorf("source expectation requires complete execution identity")
	}
	encoded, err := json.Marshal(struct {
		Identity    JobInputIdentity `json:"identity"`
		Expectation string           `json:"expectation"`
	}{identity, body})
	if err != nil {
		return "", err
	}
	return retryDigest("arcentra-source-checkout-expectation-v1", string(encoded)), nil
}

// ValidateObservation checks an actual checkout proposal without producing one.
func (e *SourceCheckoutExpectation) ValidateObservation(proposed *JobInputManifest) error {
	if _, err := e.Canonical(); err != nil {
		return err
	}
	if proposed == nil {
		return nil
	} // nil reads must remain unobserved
	if _, err := proposed.Canonical(); err != nil {
		return err
	}
	if proposed.SourceCommit != e.ExpectedSourceCommit || len(proposed.Artifacts) != 0 {
		return fmt.Errorf("checkout observation differs from expected source")
	}
	return nil
}
