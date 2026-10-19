// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot_test

import (
	"bytes"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"github.com/bytedance/sonic"
	"testing"
	"time"
)

// Frozen Version 1 field order and tags copied from accepted b76e33b4.
type legacyObservation struct {
	ID                int64      `json:"id"`
	IID               int64      `json:"iid"`
	ProviderProjectID int64      `json:"providerProjectId"`
	Title             string     `json:"title"`
	ActorName         string     `json:"actorName"`
	State             string     `json:"state"`
	SourceBranch      string     `json:"sourceBranch"`
	TargetBranch      string     `json:"targetBranch"`
	SourceSHA         string     `json:"sourceSha"`
	MergeSHA          string     `json:"mergeSha,omitempty"`
	SquashSHA         string     `json:"squashSha,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	MergedAt          *time.Time `json:"mergedAt,omitempty"`
	Fingerprint       string     `json:"fingerprint"`
	ObservationID     string     `json:"observationId"`
	MergeIdentity     string     `json:"mergeIdentity,omitempty"`
}

// DispatchEligible 明确限制首次实现可触发的状态，关闭和锁定仅作为库存观察
func (m legacyObservation) DispatchEligible() bool { return m.State == "opened" || m.State == "merged" }

// legacyResult 只在完整且两遍一致的有界读取后返回
// 两遍一致仅能发现部分分页移动，不能证明原子快照或恢复轮询间的变化
// Observations 按提供方 MR ID 排序；Digest 绑定范围、版本和完整观察数据
type legacyResult struct {
	Version           int                 `json:"version"`
	APIOrigin         string              `json:"apiOrigin"`
	Repository        string              `json:"repository"`
	ProviderProjectID int64               `json:"providerProjectId"`
	LowerBound        time.Time           `json:"lowerBound"`
	UpperBound        time.Time           `json:"upperBound"`
	Observations      []legacyObservation `json:"observations"`
	Digest            string              `json:"digest"`
}

func TestLegacyWireGoldenCompatibility(t *testing.T) {
	raw := []byte(`{"version":1,"apiOrigin":"https://gitlab.example","repository":"owner/repo","providerProjectId":11,"lowerBound":"0001-01-01T00:00:00Z","upperBound":"2026-01-03T00:00:00Z","observations":[{"id":101,"iid":7,"providerProjectId":11,"title":"legacy title <>& 中文","actorName":"Legacy","state":"merged","sourceBranch":"feature","targetBranch":"main","sourceSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mergeSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","squashSha":"cccccccccccccccccccccccccccccccccccccccc","updatedAt":"2026-01-02T00:00:00Z","mergedAt":"2026-01-02T00:00:00Z","fingerprint":"legacyfingerprint","observationId":"legacyobservation","mergeIdentity":"legacymerge"}],"digest":"legacydigest"}`)
	var old legacyResult
	var current pollsnapshot.Result
	if err := sonic.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if err := sonic.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	a, err := sonic.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sonic.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || !bytes.Equal(a, raw) {
		t.Fatalf("Version 1 bytes changed:\nold %s\nnew %s", a, b)
	}
	if current.Provider != "" || current.Observations[0].SourceProjectID != 0 || current.Observations[0].CreatedAt != nil {
		t.Fatal("legacy gained v2 fields")
	}
}

func TestVersion2WireGoldenCompatibility(t *testing.T) {
	raw := []byte(`{"version":2,"apiOrigin":"https://api.github.com","repository":"owner/repo","providerProjectId":11,"lowerBound":"0001-01-01T00:00:00Z","upperBound":"2026-01-03T00:00:00Z","observations":[{"id":101,"iid":7,"providerProjectId":11,"title":"","actorName":"","state":"merged","sourceBranch":"feature","targetBranch":"main","sourceSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mergeSha":"cccccccccccccccccccccccccccccccccccccccc","updatedAt":"2026-01-02T00:00:00Z","mergedAt":"2026-01-02T00:00:00Z","fingerprint":"v2fingerprint","observationId":"v2observation","mergeIdentity":"v2merge","sourceProjectId":22,"sourceRepository":"fork/repo","targetSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","createdAt":"2026-01-01T00:00:00Z"}],"digest":"v2digest","provider":"github"}`)
	var current pollsnapshot.Result
	if err := sonic.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	encoded, err := sonic.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, encoded) {
		t.Fatalf("Version 2 bytes changed:\n%s", encoded)
	}
	o := current.Observations[0]
	if current.ProviderProjectKey != "" || o.EvidenceVersion != 0 || o.ProviderProjectKey != "" || o.SourceProjectKey != "" || o.MergeEvidence != "" {
		t.Fatal("v2 gained v3 evidence")
	}
}
