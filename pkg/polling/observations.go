// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package polling

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Observation 保存尚未加载定义时的完整有界观察证据，不含凭证或用户文本
type Observation struct {
	MRID          int64  `json:"mrId"`
	MRIID         int64  `json:"mrIid"`
	State         string `json:"state"`
	UpdatedAt     string `json:"updatedAt"`
	MergedAt      string `json:"mergedAt,omitempty"`
	MergeSHA      string `json:"mergeSha,omitempty"`
	SquashSHA     string `json:"squashSha,omitempty"`
	SourceBranch  string `json:"sourceBranch"`
	TargetBranch  string `json:"targetBranch"`
	SourceSHA     string `json:"sourceSha"`
	ObservationID string `json:"observationId"`
	// Optional v2 provenance; omitted fields preserve legacy canonical bytes.
	SourceProjectID  int64  `json:"sourceProjectId,omitempty"`
	SourceRepository string `json:"sourceRepository,omitempty"`
	TargetSHA        string `json:"targetSha,omitempty"`
}

// Validate 校验完整观察身份及时间精度
func (o Observation) Validate() error {
	if err := validateSourceProvenance(o.SourceProjectID, o.SourceRepository, o.TargetSHA); err != nil {
		return err
	}
	updated, err := time.Parse(time.RFC3339Nano, o.UpdatedAt)
	if err != nil || updated.IsZero() || len(o.UpdatedAt) > 35 || len(o.MergedAt) > 35 || o.MRID <= 0 || o.MRIID <= 0 || !identifier(o.ObservationID, 2048) || !identifier(o.SourceBranch, 256) || !identifier(o.TargetBranch, 256) || !commit(o.SourceSHA) {
		return fmt.Errorf("invalid polling observation")
	}
	switch o.State {
	case "opened", "merged", "closed", "locked":
	default:
		return fmt.Errorf("invalid polling observation state")
	}
	if (o.MergeSHA != "" && !commit(o.MergeSHA)) || (o.SquashSHA != "" && !commit(o.SquashSHA)) {
		return fmt.Errorf("invalid polling observation merge revision")
	}
	if o.State == "merged" {
		merged, e := time.Parse(time.RFC3339Nano, o.MergedAt)
		if e != nil || merged.IsZero() || merged.After(updated) || (o.MergeSHA == "" && o.SquashSHA == "") {
			return fmt.Errorf("incomplete polling merge observation")
		}
	} else if o.MergedAt != "" || o.MergeSHA != "" || o.SquashSHA != "" {
		return fmt.Errorf("unexpected polling merge observation")
	}
	return nil
}

// Observation 返回目标中不可变的提供方观察证据
func (t Target) Observation() Observation {
	return Observation{MRID: t.MRID, MRIID: t.MRIID, State: t.State, UpdatedAt: t.UpdatedAt, MergedAt: t.MergedAt, MergeSHA: t.MergeSHA, SquashSHA: t.SquashSHA, SourceBranch: t.SourceBranch, TargetBranch: t.TargetBranch, SourceSHA: t.SourceSHA, ObservationID: t.ObservationID, SourceProjectID: t.SourceProjectID, SourceRepository: t.SourceRepository, TargetSHA: t.TargetSHA}
}

// ObservedBatch 在定义读取前持久保存完整差异及后继库存
type ObservedBatch struct {
	Version         int           `json:"version"`
	ID              string        `json:"id"`
	ScopeDigest     string        `json:"scopeDigest"`
	Generation      uint64        `json:"generation"`
	SelectionDigest string        `json:"selectionDigest"`
	Revision        uint64        `json:"revision"`
	NextCursor      Cursor        `json:"nextCursor"`
	Observations    []Observation `json:"observations"`
}

// Canonical 校验观察批次，不因定义加载失败丢失已观察变化
func (b ObservedBatch) Canonical() (string, error) {
	if b.Version != Version || !identifier(b.ID, 64) || !validDigest(b.ScopeDigest) || !validDigest(b.SelectionDigest) || b.Generation == 0 || b.Revision == 0 || len(b.Observations) > MaxInventory {
		return "", fmt.Errorf("invalid polling observed batch")
	}
	if _, err := b.NextCursor.Canonical(); err != nil {
		return "", err
	}
	seen := map[int64]bool{}
	for _, o := range b.Observations {
		if err := o.Validate(); err != nil {
			return "", err
		}
		if seen[o.MRID] || b.NextCursor.Observed[strconv.FormatInt(o.MRID, 10)] != o.ObservationID {
			return "", fmt.Errorf("inconsistent polling observed inventory")
		}
		seen[o.MRID] = true
	}
	return canonical(b)
}

// ParseObservedBatch 读取并验证持久观察阶段
func ParseObservedBatch(raw string) (*ObservedBatch, error) {
	var b ObservedBatch
	if err := decode(raw, &b); err != nil {
		return nil, err
	}
	_, err := b.Canonical()
	return &b, err
}

// validateSourceProvenance allows legacy absence, but never partial fork/base evidence.
// Repository is the provider's full owner/name identity, not an arbitrary URL.
func validateSourceProvenance(id int64, repository, targetSHA string) error {
	if id == 0 && repository == "" && targetSHA == "" {
		return nil
	}
	parts := strings.Split(repository, "/")
	if id <= 0 || len(parts) != 2 || !repositoryName(parts[0]) || !repositoryName(parts[1]) || strings.Contains(repository, "..") || !commit(targetSHA) {
		return fmt.Errorf("invalid polling source provenance")
	}
	return nil
}
