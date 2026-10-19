// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package polling 定义有界快照轮询的不可变证据与持久化结果
package polling

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Version is the unchanged cursor, selection, batch and run-context protocol version.
const Version = 1

// ScopeVersion2 namespaces GitHub/Gitea identity without changing legacy wire evidence.
// Existing scope_json stores this explicit version; no migration or rewrite is needed.
const ScopeVersion2 = 2

// ScopeVersion3 binds Gitee.com and Bitbucket Cloud evidence capabilities.
const ScopeVersion3 = 3

const (
	MergeEvidenceStateTimestamp = "provider_state_timestamp"
	MergeEvidenceStateCommit    = "provider_state_commit"
)

// MaxPayloadBytes 限制每份持久化 JSON 的最大大小
const MaxPayloadBytes = 1048576

// MaxInventory 限制完整快照可容纳的 MR 数量
const MaxInventory = 2000

// MaxPipelines 限制一次显式选择的流水线数量
const MaxPipelines = 100

// MaxTargets 限制单批冻结的派发目标数量
const MaxTargets = 2000

// Actor 标识自动轮询运行，实际授权者另存于不可变上下文
const Actor = "polling"

// StateDisabled 及相关常量定义粘性激活状态
const (
	StateDisabled  = "disabled"
	StateBaselined = "baselined"
	StateEnabled   = "enabled"
	StatePaused    = "paused"
)

// ErrConflict 表示客户端修订或冻结证据已过期
var ErrConflict = fmt.Errorf("polling revision conflict")

// ErrForbidden 表示已确认的当前权限拒绝
var ErrForbidden = fmt.Errorf("polling authorization denied")

// ErrBlocked 表示当前配置不满足轮询就绪条件
var ErrBlocked = fmt.Errorf("polling readiness blocked")

// ErrUnresolved 表示已有未确定执行记录，不允许重新提交
var ErrUnresolved = fmt.Errorf("polling run outcome unresolved")

// Scope 保存不含凭证的完整提供方作用域
type Scope struct {
	Version           int    `json:"version"`
	ProjectID         string `json:"projectId"`
	Provider          string `json:"provider"`
	APIOrigin         string `json:"apiOrigin"`
	Repository        string `json:"repository"`
	ProviderProjectID string `json:"providerProjectId"`
	Stream            string `json:"stream"`
}

// CanonicalRepository 规范化 HTTPS 仓库标识且拒绝凭证、查询和片段
func CanonicalRepository(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("unsupported polling repository")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") {
		return "", fmt.Errorf("ambiguous polling repository")
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	if u.Path == "" || u.Path == "/" || path.Clean(u.Path) != u.Path {
		return "", fmt.Errorf("invalid polling repository path")
	}
	return u.String(), nil
}

// ProviderRepository validates an already canonical repository and returns its
// scope policy. It does not discover repository identity or authorize activation.
func ProviderRepository(provider, repository string) (version int, apiOrigin, stream string, err error) {
	repo, e := CanonicalRepository(repository)
	if e != nil || repo != repository {
		return 0, "", "", fmt.Errorf("invalid polling repository")
	}
	u, _ := url.Parse(repo)
	origin := u.Scheme + "://" + u.Host
	// Preserve the exact v1 policy, including GitLab nested namespaces.
	if provider == "gitlab" {
		return Version, origin, "merge_requests", nil
	}
	if provider != "github" && provider != "gitea" && provider != "gitee" && provider != "bitbucket" {
		return 0, "", "", fmt.Errorf("unsupported polling provider")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !repositoryName(parts[0]) || !repositoryName(parts[1]) || u.Hostname() == "" || strings.HasSuffix(u.Hostname(), ".") {
		return 0, "", "", fmt.Errorf("invalid polling owner/repository")
	}
	if provider == "github" {
		if u.Host != "github.com" {
			return 0, "", "", fmt.Errorf("only public GitHub is supported")
		}
		origin = "https://api.github.com"
	}
	if provider == "gitee" || provider == "bitbucket" {
		if !v3RepositoryPart(parts[0]) || !v3RepositoryPart(parts[1]) {
			return 0, "", "", fmt.Errorf("invalid polling owner/repository")
		}
		host, api := "gitee.com", "https://gitee.com"
		if provider == "bitbucket" {
			host, api = "bitbucket.org", "https://api.bitbucket.org"
		}
		if u.Host != host {
			return 0, "", "", fmt.Errorf("unsupported polling provider host")
		}
		return ScopeVersion3, api, "pull_requests", nil
	}
	return ScopeVersion2, origin, "pull_requests", nil
}

// repositoryName deliberately accepts a bounded unambiguous ASCII subset.
func repositoryName(v string) bool {
	if v == "" || len(v) > 100 || v == "." || v == ".." || strings.HasSuffix(v, ".git") || strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// Validate checks the explicit provider/version namespace and stable repository identity.
func (s Scope) Validate() error {
	version, origin, stream, err := ProviderRepository(s.Provider, s.Repository)
	if err != nil || !validProjectKey(s.Provider, s.ProviderProjectID) || s.Version != version || !identifier(s.ProjectID, 64) || s.Stream != stream || !identifier(s.ProviderProjectID, 128) || s.APIOrigin != origin {
		return fmt.Errorf("invalid polling scope")
	}
	return nil
}

// Canonical 返回确定性 JSON 表示
func (s Scope) Canonical() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return canonical(s)
}

// Digest 返回完整作用域的 SHA256 摘要
func (s Scope) Digest() (string, error) { v, e := s.Canonical(); return digest(v), e }

// PipelineBinding 固定选择授权所覆盖的仓库、分支与定义路径
type PipelineBinding struct {
	PipelineID     string `json:"pipelineId"`
	Repository     string `json:"repository"`
	DefaultBranch  string `json:"defaultBranch"`
	DefinitionPath string `json:"definitionPath"`
}

// Selection 保存按流水线标识排序的精确允许列表
type Selection struct {
	Version   int               `json:"version"`
	Pipelines []PipelineBinding `json:"pipelines"`
}

// Canonical 校验并返回排序后的选择，不修改调用方切片
func (s Selection) Canonical() (string, error) {
	if s.Version != Version || len(s.Pipelines) == 0 || len(s.Pipelines) > MaxPipelines {
		return "", fmt.Errorf("invalid polling selection")
	}
	s.Pipelines = append([]PipelineBinding(nil), s.Pipelines...)
	sort.Slice(s.Pipelines, func(i, j int) bool { return s.Pipelines[i].PipelineID < s.Pipelines[j].PipelineID })
	for i, p := range s.Pipelines {
		r, e := CanonicalRepository(p.Repository)
		if e != nil || r != p.Repository || !identifier(p.PipelineID, 64) || !identifier(p.DefaultBranch, 256) || !definitionPath(p.DefinitionPath) || (i > 0 && s.Pipelines[i-1].PipelineID == p.PipelineID) {
			return "", fmt.Errorf("invalid polling pipeline binding")
		}
	}
	return canonical(s)
}

// Digest 返回精确选择摘要
func (s Selection) Digest() (string, error) { v, e := s.Canonical(); return digest(v), e }

// Find 查找显式选择中的流水线
func (s Selection) Find(id string) (PipelineBinding, bool) {
	for _, p := range s.Pipelines {
		if p.PipelineID == id {
			return p, true
		}
	}
	return PipelineBinding{}, false
}

// ParseSelection 校验有界选择 JSON
func ParseSelection(raw string) (*Selection, error) {
	var v Selection
	if err := decode(raw, &v); err != nil {
		return nil, err
	}
	_, err := v.Canonical()
	return &v, err
}

// Cursor 保存已确认的完整快照边界及每个 MR 的最新指纹或稳定合并身份
type Cursor struct {
	Version  int               `json:"version"`
	Boundary string            `json:"boundary"`
	Complete bool              `json:"complete"`
	Observed map[string]string `json:"observed"`
}

// Canonical 校验完整、有界且具备完整精度的游标
func (c Cursor) Canonical() (string, error) {
	if c.Version != Version || !c.Complete || len(c.Observed) > MaxInventory || c.Observed == nil {
		return "", fmt.Errorf("incomplete polling cursor")
	}
	if _, e := time.Parse(time.RFC3339Nano, c.Boundary); e != nil {
		return "", e
	}
	for k, v := range c.Observed {
		if !identifier(k, 256) || !identifier(v, 1024) {
			return "", fmt.Errorf("invalid polling inventory")
		}
	}
	return canonical(c)
}

// Digest 返回基线或游标的证据摘要
func (c Cursor) Digest() (string, error) { v, e := c.Canonical(); return digest(v), e }

// ParseCursor 解析并验证有界完整游标
func ParseCursor(raw string) (*Cursor, error) {
	var v Cursor
	if e := decode(raw, &v); e != nil {
		return nil, e
	}
	_, e := v.Canonical()
	return &v, e
}

// Target 保存已匹配流水线及精确不可变定义，不把观察 SHA 当作实际作业检出证据
type Target struct {
	MRID                     int64  `json:"mrId"`
	MRIID                    int64  `json:"mrIid"`
	State                    string `json:"state"`
	UpdatedAt                string `json:"updatedAt"`
	MergedAt                 string `json:"mergedAt,omitempty"`
	MergeSHA                 string `json:"mergeSha,omitempty"`
	SquashSHA                string `json:"squashSha,omitempty"`
	PipelineID               string `json:"pipelineId"`
	ObservationID            string `json:"observationId"`
	RequestID                string `json:"requestId"`
	SourceBranch             string `json:"sourceBranch"`
	TargetBranch             string `json:"targetBranch"`
	SourceSHA                string `json:"sourceSha"`
	DefinitionCommitSHA      string `json:"definitionCommitSha"`
	DefinitionPath           string `json:"definitionPath"`
	ResolvedDefinitionDigest string `json:"resolvedDefinitionDigest"`
	// Append-only optional fields retain v1 JSON byte compatibility.
	SourceProjectID  int64  `json:"sourceProjectId,omitempty"`
	SourceRepository string `json:"sourceRepository,omitempty"`
	TargetSHA        string `json:"targetSha,omitempty"`
	// Append-only v3 capability and string-identity lane; never mixed with numeric IDs.
	EvidenceVersion    int    `json:"evidenceVersion,omitempty"`
	ProviderProjectKey string `json:"providerProjectKey,omitempty"`
	SourceProjectKey   string `json:"sourceProjectKey,omitempty"`
	MergeEvidence      string `json:"mergeEvidence,omitempty"`
}

// Batch 保存派发前冻结的归一化批次，空目标同样必须带完整后继游标
type Batch struct {
	ObservationCount int            `json:"observationCount"`
	Version          int            `json:"version"`
	ID               string         `json:"id"`
	ScopeDigest      string         `json:"scopeDigest"`
	Generation       uint64         `json:"generation"`
	SelectionDigest  string         `json:"selectionDigest"`
	Revision         uint64         `json:"revision"`
	NextCursor       Cursor         `json:"nextCursor"`
	Targets          []Target       `json:"targets"`
	Results          []TargetResult `json:"results,omitempty"`
}

// TargetResult 保存与运行状态同事务提交的明确准入或拒绝回执
type TargetResult struct {
	PipelineID string  `json:"pipelineId"`
	RequestID  string  `json:"requestId"`
	RunID      string  `json:"runId"`
	Outcome    Outcome `json:"outcome"`
}

// Canonical 验证有界冻结批次
func (b Batch) Canonical() (string, error) {
	if b.ObservationCount < 0 || b.ObservationCount > MaxInventory || b.Version != Version || !identifier(b.ID, 64) || !validDigest(b.ScopeDigest) || !validDigest(b.SelectionDigest) || b.Generation == 0 || b.Revision == 0 || len(b.Targets) > MaxTargets {
		return "", fmt.Errorf("invalid polling batch")
	}
	if _, e := b.NextCursor.Canonical(); e != nil {
		return "", e
	}
	seen := map[string]bool{}
	for _, t := range b.Targets {
		if e := t.Validate(); e != nil {
			return "", e
		}
		key := t.PipelineID + "\x00" + t.RequestID
		if seen[key] {
			return "", fmt.Errorf("duplicate polling target")
		}
		seen[key] = true
	}
	if len(b.Results) > len(b.Targets) {
		return "", fmt.Errorf("invalid polling receipts")
	}
	receipts := map[string]bool{}
	for _, r := range b.Results {
		k := r.PipelineID + "\x00" + r.RequestID
		if !seen[k] || receipts[k] || !identifier(r.RunID, 64) || (r.Outcome != OutcomeAdmitted && r.Outcome != OutcomeRejected) {
			return "", fmt.Errorf("invalid polling receipt")
		}
		receipts[k] = true
	}
	body, err := canonical(b)
	// 为尚未写入的回执保留最坏大小，避免准入后因同一 JSON 上限永久卡住
	if len(body)+512*(len(b.Targets)-len(b.Results)) > MaxPayloadBytes {
		return "", fmt.Errorf("polling batch receipt capacity exceeds bound")
	}
	return body, err
}

// Validate checks structural target evidence. Admission requires ValidateForScope.
func (t Target) Validate() error {
	if err := t.Observation().Validate(); err != nil {
		return err
	}
	if t.State != "opened" && t.State != "merged" {
		return fmt.Errorf("invalid polling target state")
	}
	if !identifier(t.PipelineID, 64) || !identifier(t.ObservationID, 2048) || !strings.HasPrefix(t.RequestID, "poll:v1:") || len(t.RequestID) != 72 || !identifier(t.SourceBranch, 256) || !identifier(t.TargetBranch, 256) || !commit(t.SourceSHA) || !commit(t.DefinitionCommitSHA) || !definitionPath(t.DefinitionPath) || !validDigest(t.ResolvedDefinitionDigest) {
		return fmt.Errorf("invalid polling target")
	}
	return nil
}

// ParseBatch 解析并验证有界批次
func ParseBatch(raw string) (*Batch, error) {
	var v Batch
	if e := decode(raw, &v); e != nil {
		return nil, e
	}
	_, e := v.Canonical()
	return &v, e
}

// EventRequestID 为相同作用域及观察身份生成跨激活代际稳定的去重键
func EventRequestID(scope Scope, observationID string) (string, error) {
	s, e := scope.Canonical()
	if e != nil || !identifier(observationID, 2048) {
		return "", fmt.Errorf("invalid polling event identity")
	}
	return "poll:v1:" + strings.TrimPrefix(digest(s+"\x00"+observationID), "v1:"), nil
}

// RunContext 记录只能由可信创建入口写入的完整轮询运行证据
type RunContext struct {
	BaselineID           string `json:"baselineId"`
	BaselineCutoff       string `json:"baselineCutoff"`
	Version              int    `json:"version"`
	StreamID             string `json:"streamId"`
	Scope                Scope  `json:"scope"`
	ScopeDigest          string `json:"scopeDigest"`
	ActivationGeneration uint64 `json:"activationGeneration"`
	ActorID              string `json:"actorId"`
	SelectionDigest      string `json:"selectionDigest"`
	BatchID              string `json:"batchId"`
	BatchRevision        uint64 `json:"batchRevision"`
	RunID                string `json:"runId"`
	Target               Target `json:"target"`
}

// Canonical 校验不可变运行上下文及稳定事件键
func (c RunContext) Canonical() (string, error) {
	cutoff, cutoffErr := time.Parse(time.RFC3339Nano, c.BaselineCutoff)
	if cutoffErr != nil || cutoff.IsZero() || len(c.BaselineCutoff) > 35 || cutoff.UTC().Format(time.RFC3339Nano) != c.BaselineCutoff || !identifier(c.BaselineID, 64) {
		return "", fmt.Errorf("invalid polling baseline provenance")
	}
	sd, e := c.Scope.Digest()
	req, re := EventRequestID(c.Scope, c.Target.ObservationID)
	if e != nil || re != nil || c.Version != Version || !identifier(c.StreamID, 64) || sd != c.ScopeDigest || !identifier(c.ActorID, 64) || !identifier(c.RunID, 64) || !identifier(c.BatchID, 64) || !validDigest(c.SelectionDigest) || c.ActivationGeneration == 0 || c.BatchRevision == 0 || req != c.Target.RequestID {
		return "", fmt.Errorf("invalid polling run context")
	}
	if e := c.Target.ValidateForScope(c.Scope); e != nil {
		return "", e
	}
	return canonical(c)
}

// Digest 返回不可变运行上下文摘要
func (c RunContext) Digest() (string, error) { v, e := c.Canonical(); return digest(v), e }

// ParseRunContext 解析并验证持久化的可信上下文
func ParseRunContext(raw string) (*RunContext, error) {
	var v RunContext
	if e := decode(raw, &v); e != nil {
		return nil, e
	}
	_, e := v.Canonical()
	return &v, e
}

// Outcome 只把确认提交的状态作为可操作结果
type Outcome string

// OutcomeCreated 及相关值区分本次新建、持久化准入和未知结果
const (
	OutcomeCreated    Outcome = "created"
	OutcomeAdmitted   Outcome = "durable_admitted"
	OutcomeRejected   Outcome = "durable_rejected"
	OutcomeUnresolved Outcome = "unresolved"
)

// CreationOutcome 只有 CreatedNow 才允许本次调用进入提交阶段
type CreationOutcome struct {
	Outcome    Outcome `json:"outcome"`
	RunID      string  `json:"runId"`
	CreatedNow bool    `json:"createdNow"`
}

// AdmissionOutcome 只有 ClaimedNow 且 DurableAdmitted 才允许创建协调器
type AdmissionOutcome struct {
	Outcome    Outcome `json:"outcome"`
	RunID      string  `json:"runId"`
	ClaimedNow bool    `json:"claimedNow"`
}

func canonical(v any) (string, error) {
	s, e := sonic.ConfigStd.MarshalToString(v)
	if len(s) > MaxPayloadBytes {
		return "", fmt.Errorf("polling payload exceeds bound")
	}
	return s, e
}
func decode(s string, v any) error {
	if s == "" || len(s) > MaxPayloadBytes {
		return fmt.Errorf("polling payload exceeds bound")
	}
	return sonic.ConfigStd.UnmarshalFromString(s, v)
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return "v1:" + hex.EncodeToString(h[:]) }
func validDigest(v string) bool {
	if len(v) != 67 || !strings.HasPrefix(v, "v1:") {
		return false
	}
	_, e := hex.DecodeString(v[3:])
	return e == nil
}
func identifier(v string, max int) bool {
	return v != "" && len(v) <= max && v == strings.TrimSpace(v) && !strings.ContainsAny(v, "\x00\r\n")
}
func commit(v string) bool {
	if len(v) != 40 && len(v) != 64 {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil && v == strings.ToLower(v)
}
func definitionPath(v string) bool {
	return identifier(v, 512) && !strings.HasPrefix(v, "/") && path.Clean(v) == v && v != "." && v != ".." && !strings.HasPrefix(v, "../") && !strings.Contains(v, "\\")
}
