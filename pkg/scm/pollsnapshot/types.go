// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import "time"

type Request struct {
	Repository        string
	ProviderProjectID int64
	LowerBound        time.Time
	UpperBound        time.Time
}

// Observation 保留提供方原始身份和版本证据，SHA 不是作业实际检出证明
// ObservationID 对非合并状态包含更新时间；合并状态使用稳定 MergeIdentity
// Fingerprint 仅覆盖派发相关字段，排除标题、作者和处理时间
type Observation struct {
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
	SourceProjectID   int64      `json:"sourceProjectId,omitempty"`
	SourceRepository  string     `json:"sourceRepository,omitempty"`
	TargetSHA         string     `json:"targetSha,omitempty"`
	CreatedAt         *time.Time `json:"createdAt,omitempty"`
}

// DispatchEligible 明确限制首次实现可触发的状态，关闭和锁定仅作为库存观察
func (m Observation) DispatchEligible() bool { return m.State == "opened" || m.State == "merged" }

// Result 只在完整且两遍一致的有界读取后返回
// 两遍一致仅能发现部分分页移动，不能证明原子快照或恢复轮询间的变化
// Observations 按提供方 MR ID 排序；Digest 绑定范围、版本和完整观察数据
type Result struct {
	Version           int           `json:"version"`
	APIOrigin         string        `json:"apiOrigin"`
	Repository        string        `json:"repository"`
	ProviderProjectID int64         `json:"providerProjectId"`
	LowerBound        time.Time     `json:"lowerBound"`
	UpperBound        time.Time     `json:"upperBound"`
	Observations      []Observation `json:"observations"`
	Digest            string        `json:"digest"`
	Provider          string        `json:"provider,omitempty"`
}
