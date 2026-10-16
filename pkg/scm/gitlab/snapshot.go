// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
)

// SnapshotVersion 标识 GitLab MR 快照语义，不表示无损事件历史
const SnapshotVersion = 1

// ErrSnapshotIncomplete 表示读取不完整，调用方不得推进持久进度或使用部分结果
var ErrSnapshotIncomplete = errors.New("gitlab merge request snapshot is incomplete")

// SnapshotError 提供固定原因及重试信息，禁止包含令牌、响应正文或网络地址
// RetryAt 和 RateLimitResetAt 仅用于调度，不属于已确认的观察进度
type SnapshotError struct {
	Reason             string
	StatusCode         int
	RetryAt            time.Time
	RateLimitRemaining *int64
	RateLimitResetAt   time.Time
}

// Error 返回可安全展示的固定错误原因
func (e *SnapshotError) Error() string { return ErrSnapshotIncomplete.Error() + ": " + e.Reason }

// Unwrap 支持按不完整快照错误分类
func (e *SnapshotError) Unwrap() error { return ErrSnapshotIncomplete }

// SnapshotLimits 限制整次读取的请求、页数、对象数、字节数和时间
// MaxPages 是两次扫描合计的列表页预算；MaxItems 是单次库存上限
// 非零值只能缩小默认上限；零值使用默认配置
type SnapshotLimits struct {
	PageSize         int
	MaxPages         int
	MaxItems         int
	MaxResponseBytes int64
	MaxTotalBytes    int64
	RequestTimeout   time.Duration
	ScanTimeout      time.Duration
}

// DefaultSnapshotLimits 返回有界的默认读取配置
func DefaultSnapshotLimits() SnapshotLimits {
	return SnapshotLimits{
		PageSize: 100, MaxPages: 64, MaxItems: 2000,
		MaxResponseBytes: 2 << 20, MaxTotalBytes: 16 << 20,
		RequestTimeout: 10 * time.Second, ScanTimeout: 45 * time.Second,
	}
}

// SnapshotRequest 固定一个扫描范围，不能指定 URL、凭据或执行权限
// LowerBound 为零表示全量库存基线或完整对账，任何读取均可使用；否则查询含等号的更新时间区间
// UpperBound 必填且在整个读取过程中不变；已有范围应传入 ProviderProjectID
// 此接口不读取时钟来补齐缺失边界，也不使用旧版 PollEvents 游标
type SnapshotRequest struct {
	Repository        string
	ProviderProjectID int64
	LowerBound        time.Time
	UpperBound        time.Time
}

// MRSnapshot 保留提供方原始身份和版本证据，SHA 不是作业实际检出证明
// ObservationID 对非合并状态包含更新时间；合并状态使用稳定 MergeIdentity
// Fingerprint 仅覆盖派发相关字段，排除标题、作者和处理时间
type MRSnapshot struct {
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
func (m MRSnapshot) DispatchEligible() bool { return m.State == "opened" || m.State == "merged" }

// SnapshotResult 只在完整且两遍一致的有界读取后返回
// 两遍一致仅能发现部分分页移动，不能证明原子快照或恢复轮询间的变化
// Observations 按提供方 MR ID 排序；Digest 绑定范围、版本和完整观察数据
type SnapshotResult struct {
	Version           int          `json:"version"`
	APIOrigin         string       `json:"apiOrigin"`
	Repository        string       `json:"repository"`
	ProviderProjectID int64        `json:"providerProjectId"`
	LowerBound        time.Time    `json:"lowerBound"`
	UpperBound        time.Time    `json:"upperBound"`
	Observations      []MRSnapshot `json:"observations"`
	Digest            string       `json:"digest"`
}

// SnapshotReader 只读取显式 GitLab 项目配置，不具备启用、派发或游标写权限
// 实例可并发复用，每次 Read 的预算和库存独立
// 官方契约：https://docs.gitlab.com/api/merge_requests/#list-project-merge-requests
// 分页契约：https://docs.gitlab.com/api/rest/#pagination
type SnapshotReader struct {
	base   *url.URL
	token  string
	client *http.Client
	limits SnapshotLimits
}

var snapshotPathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
var snapshotTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
var snapshotSHA = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// NewSnapshotReader 使用服务端现有配置构造只读客户端，禁止隐式提供方推断
// 必须提供显式 HTTPS BaseURL；公开仓库可不带令牌，APIBaseURL 必须同范围
// 可注入受信 HTTP 客户端供测试使用；会复制客户端并强制禁止全部重定向
func NewSnapshotReader(cfg scm.ProviderConfig, client *http.Client, limits SnapshotLimits) (*SnapshotReader, error) {
	if cfg.Kind != scm.ProviderKindGitLab || len(cfg.Token) > 8192 || strings.ContainsAny(cfg.Token, "\r\n\x00") {
		return nil, snapshotFailure("explicit valid GitLab configuration is required")
	}
	base, err := snapshotBaseURL(cfg)
	if err != nil {
		return nil, err
	}
	limits, err = normalizeSnapshotLimits(limits)
	if err != nil {
		return nil, err
	}
	var copied http.Client
	if client != nil {
		copied = *client
	}
	transport := copied.Transport
	if transport == nil {
		transport = &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
			MaxResponseHeaderBytes: 32 << 10, DisableCompression: true,
			MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		}
	} else if t, ok := transport.(*http.Transport); ok {
		t = t.Clone()
		if t.TLSClientConfig != nil && t.TLSClientConfig.InsecureSkipVerify {
			return nil, snapshotFailure("TLS verification must remain enabled")
		}
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			t.TLSClientConfig = t.TLSClientConfig.Clone()
			if t.TLSClientConfig.MinVersion < tls.VersionTLS12 {
				t.TLSClientConfig.MinVersion = tls.VersionTLS12
			}
		}
		t.Proxy = nil
		t.DisableCompression = true
		t.MaxResponseHeaderBytes = 32 << 10
		transport = t
	}
	copied.Transport, copied.Jar = transport, nil
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copied.Timeout <= 0 || copied.Timeout > limits.RequestTimeout {
		copied.Timeout = limits.RequestTimeout
	}
	return &SnapshotReader{base: base, token: strings.TrimSpace(cfg.Token), client: &copied, limits: limits}, nil
}

func snapshotBaseURL(cfg scm.ProviderConfig) (*url.URL, error) {
	raw := strings.TrimRight(cfg.BaseURL, "/")
	base, err := url.Parse(raw)
	if err != nil || raw == "" || raw != strings.TrimSpace(raw) || base.Scheme != "https" || base.Host == "" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawPath != "" || strings.ContainsAny(raw, "\\%\r\n\x00") {
		return nil, snapshotFailure("invalid configured GitLab origin")
	}
	if base.Path != "" && (path.Clean(base.Path) != base.Path || strings.HasSuffix(base.Path, "/")) {
		return nil, snapshotFailure("invalid configured GitLab base path")
	}
	base.Host = strings.ToLower(base.Host)
	base.Path += "/api/v4"
	if cfg.APIBaseURL != "" && strings.TrimRight(cfg.APIBaseURL, "/") != base.String() {
		return nil, snapshotFailure("GitLab API origin or base path does not match project configuration")
	}
	return base, nil
}

func normalizeSnapshotLimits(v SnapshotLimits) (SnapshotLimits, error) {
	d := DefaultSnapshotLimits()
	ints := [][2]*int{{&v.PageSize, &d.PageSize}, {&v.MaxPages, &d.MaxPages}, {&v.MaxItems, &d.MaxItems}}
	for _, p := range ints {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] < 1 || *p[0] > *p[1] {
			return v, snapshotFailure("invalid snapshot limits")
		}
	}
	for _, p := range [][2]*int64{{&v.MaxResponseBytes, &d.MaxResponseBytes}, {&v.MaxTotalBytes, &d.MaxTotalBytes}} {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] < 1 || *p[0] > *p[1] {
			return v, snapshotFailure("invalid snapshot limits")
		}
	}
	for _, p := range [][2]*time.Duration{{&v.RequestTimeout, &d.RequestTimeout}, {&v.ScanTimeout, &d.ScanTimeout}} {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] <= 0 || *p[0] > *p[1] {
			return v, snapshotFailure("invalid snapshot limits")
		}
	}
	return v, nil
}

func snapshotFailure(reason string) error { return &SnapshotError{Reason: reason} }

// Read 完整读取固定区间两次并核对项目身份及库存，任何失败均返回零值结果
func (r *SnapshotReader) Read(ctx context.Context, req SnapshotRequest) (SnapshotResult, error) {
	zero := SnapshotResult{}
	if r == nil || r.client == nil || !validSnapshotRepository(req.Repository) || req.ProviderProjectID < 0 || !validSnapshotTime(req.UpperBound) || (!req.LowerBound.IsZero() && (!validSnapshotTime(req.LowerBound) || req.LowerBound.After(req.UpperBound))) {
		return zero, snapshotFailure("invalid snapshot request")
	}
	ctx, cancel := context.WithTimeout(ctx, r.limits.ScanTimeout)
	defer cancel()
	req.LowerBound, req.UpperBound = req.LowerBound.UTC(), req.UpperBound.UTC()
	scan := snapshotScan{reader: r}
	projectURL := r.base.String() + "/projects/" + url.PathEscape(req.Repository)
	projectID, err := scan.project(ctx, projectURL, req)
	if err != nil {
		return zero, err
	}
	req.ProviderProjectID = projectID
	var first []MRSnapshot
	var firstDigest string
	for pass := 0; pass < 2; pass++ {
		observations, err := scan.list(ctx, projectURL+"/merge_requests", req)
		if err != nil {
			return zero, err
		}
		encoded, err := sonic.Marshal(observations)
		if err != nil {
			return zero, snapshotFailure("snapshot normalization failed")
		}
		digest := snapshotDigest(string(encoded))
		if pass == 0 {
			first, firstDigest = observations, digest
		} else if firstDigest != digest {
			return zero, snapshotFailure("snapshot changed during pagination")
		}
	}
	// 再次验证空库存、重命名和路径复用，避免只有 MR 载荷才能核对项目身份
	if _, err := scan.project(ctx, projectURL, req); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, snapshotFailure("snapshot deadline or cancellation")
	}
	out := SnapshotResult{Version: SnapshotVersion, APIOrigin: r.base.Scheme + "://" + r.base.Host, Repository: req.Repository, ProviderProjectID: projectID, LowerBound: req.LowerBound, UpperBound: req.UpperBound, Observations: first}
	encoded, err := sonic.Marshal(out)
	if err != nil {
		return zero, snapshotFailure("snapshot normalization failed")
	}
	out.Digest = snapshotDigest(string(encoded))
	return out, nil
}

func validSnapshotRepository(name string) bool {
	parts := strings.Split(name, "/")
	if len(name) > 1024 || len(parts) < 2 || len(parts) > 20 {
		return false
	}
	for _, p := range parts {
		if !snapshotPathPart.MatchString(p) || strings.HasSuffix(p, ".git") || strings.HasSuffix(p, ".") {
			return false
		}
	}
	return true
}

func validSnapshotTime(t time.Time) bool {
	year := t.UTC().Year()
	return !t.IsZero() && year >= 1 && year <= 9999
}

type snapshotScan struct {
	reader *SnapshotReader
	pages  int
	bytes  int64
}

func (s *snapshotScan) get(ctx context.Context, endpoint string) ([]byte, http.Header, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.reader.limits.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, snapshotFailure("snapshot request could not be prepared")
	}
	if s.reader.token != "" {
		req.Header.Set("PRIVATE-TOKEN", s.reader.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.reader.client.Do(req)
	if err != nil {
		return nil, nil, snapshotFailure("snapshot transport failed or timed out")
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, snapshotFailure("empty snapshot transport response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		failure := &SnapshotError{Reason: "provider returned a non-success status", StatusCode: resp.StatusCode}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			failure.Reason = "provider authentication is required or was rejected"
		case http.StatusForbidden:
			failure.Reason = "provider access is forbidden"
		case http.StatusTooManyRequests:
			failure.Reason = "provider rate limit exceeded"
		}
		failure.RetryAt = snapshotRetryAt(resp.Header.Get("Retry-After"), time.Now())
		if n, err := strconv.ParseInt(resp.Header.Get("RateLimit-Remaining"), 10, 64); err == nil && n >= 0 {
			failure.RateLimitRemaining = &n
		}
		if n, err := strconv.ParseInt(resp.Header.Get("RateLimit-Reset"), 10, 64); err == nil && n > 0 {
			failure.RateLimitResetAt = time.Unix(n, 0).UTC()
		}
		return nil, nil, failure
	}
	headerBytes := 0
	for key, values := range resp.Header {
		headerBytes += len(key)
		for _, value := range values {
			headerBytes += len(value)
		}
	}
	if headerBytes > 32<<10 || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return nil, nil, snapshotFailure("unsupported or oversized response headers")
	}
	remaining := s.reader.limits.MaxTotalBytes - s.bytes
	limit := s.reader.limits.MaxResponseBytes
	if remaining < limit {
		limit = remaining
	}
	if limit <= 0 || resp.ContentLength > limit {
		return nil, nil, snapshotFailure("snapshot response byte budget exceeded")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || reqCtx.Err() != nil {
		return nil, nil, snapshotFailure("snapshot body incomplete or timed out")
	}
	if int64(len(body)) > limit {
		return nil, nil, snapshotFailure("snapshot response byte budget exceeded")
	}
	s.bytes += int64(len(body))
	if !utf8.Valid(body) {
		return nil, nil, snapshotFailure("snapshot response is not valid UTF-8")
	}
	return body, resp.Header, nil
}

func snapshotRetryAt(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= 365*24*3600 {
		return now.Add(time.Duration(seconds) * time.Second).UTC()
	}
	if at, err := http.ParseTime(value); err == nil {
		return at.UTC()
	}
	return time.Time{}
}

func (s *snapshotScan) project(ctx context.Context, endpoint string, req SnapshotRequest) (int64, error) {
	body, _, err := s.get(ctx, endpoint)
	if err != nil {
		return 0, err
	}
	var project struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := sonic.Unmarshal(body, &project); err != nil || project.ID <= 0 || project.PathWithNamespace != req.Repository || (req.ProviderProjectID != 0 && req.ProviderProjectID != project.ID) {
		return 0, snapshotFailure("provider project identity does not match configured repository")
	}
	return project.ID, nil
}

type snapshotWireMR struct {
	ID        int64   `json:"id"`
	IID       int64   `json:"iid"`
	ProjectID int64   `json:"project_id"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	UpdatedAt string  `json:"updated_at"`
	MergedAt  *string `json:"merged_at"`
	SHA       string  `json:"sha"`
	MergeSHA  string  `json:"merge_commit_sha"`
	SquashSHA string  `json:"squash_commit_sha"`
	Source    string  `json:"source_branch"`
	Target    string  `json:"target_branch"`
	Author    struct {
		Name string `json:"name"`
	} `json:"author"`
}

func (s *snapshotScan) list(ctx context.Context, endpoint string, req SnapshotRequest) ([]MRSnapshot, error) {
	base, _ := url.Parse(endpoint)
	query := url.Values{"scope": {"all"}, "state": {"all"}, "order_by": {"updated_at"}, "sort": {"asc"}, "per_page": {strconv.Itoa(s.reader.limits.PageSize)}, "updated_before": {req.UpperBound.Format(time.RFC3339Nano)}}
	if !req.LowerBound.IsZero() {
		query.Set("updated_after", req.LowerBound.Format(time.RFC3339Nano))
	}
	observations := make([]MRSnapshot, 0)
	seenIDs, seenIIDs := map[int64]bool{}, map[int64]bool{}
	var lastUpdated time.Time
	meta := snapshotPagination{total: -1, totalPages: -1, perPage: -1}
	for page := 1; ; page++ {
		if s.pages >= s.reader.limits.MaxPages {
			return nil, snapshotFailure("snapshot page budget exceeded")
		}
		s.pages++
		query.Set("page", strconv.Itoa(page))
		base.RawQuery = query.Encode()
		body, headers, err := s.get(ctx, base.String())
		if err != nil {
			return nil, err
		}
		var wire []snapshotWireMR
		if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '[' || sonic.Unmarshal(body, &wire) != nil || wire == nil || len(wire) > s.reader.limits.PageSize {
			return nil, snapshotFailure("invalid merge request list response")
		}
		for _, mr := range wire {
			observation, err := s.reader.normalize(req, mr)
			if err != nil {
				return nil, err
			}
			if seenIDs[mr.ID] || seenIIDs[mr.IID] || observation.UpdatedAt.Before(lastUpdated) {
				return nil, snapshotFailure("duplicate or moved merge request during pagination")
			}
			seenIDs[mr.ID], seenIIDs[mr.IID], lastUpdated = true, true, observation.UpdatedAt
			observations = append(observations, observation)
			if len(observations) > s.reader.limits.MaxItems {
				return nil, snapshotFailure("snapshot item budget exceeded")
			}
		}
		more, err := meta.next(headers, base, page, len(wire), len(observations), s.reader.limits.PageSize)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		if meta.perPage > 0 {
			query.Set("per_page", strconv.Itoa(meta.perPage))
		}
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ID < observations[j].ID })
	return observations, nil
}

func (r *SnapshotReader) normalize(req SnapshotRequest, wire snapshotWireMR) (MRSnapshot, error) {
	zero := MRSnapshot{}
	updated, err := snapshotParseTime(wire.UpdatedAt)
	if err != nil || wire.ID <= 0 || wire.IID <= 0 || wire.ProjectID != req.ProviderProjectID || !snapshotSHA.MatchString(wire.SHA) || (wire.MergeSHA != "" && !snapshotSHA.MatchString(wire.MergeSHA)) || (wire.SquashSHA != "" && !snapshotSHA.MatchString(wire.SquashSHA)) || !validSnapshotBranch(wire.Source) || !validSnapshotBranch(wire.Target) || len(wire.Title) > 16384 || len(wire.Author.Name) > 1024 {
		return zero, snapshotFailure("merge request identity or revision is missing or malformed")
	}
	if updated.Before(req.LowerBound) || updated.After(req.UpperBound) {
		return zero, snapshotFailure("merge request lies outside frozen update bounds")
	}
	if wire.State != "opened" && wire.State != "closed" && wire.State != "merged" && wire.State != "locked" {
		return zero, snapshotFailure("unsupported merge request state")
	}
	out := MRSnapshot{ID: wire.ID, IID: wire.IID, ProviderProjectID: wire.ProjectID, Title: wire.Title, ActorName: wire.Author.Name, State: wire.State, SourceBranch: wire.Source, TargetBranch: wire.Target, SourceSHA: wire.SHA, MergeSHA: wire.MergeSHA, SquashSHA: wire.SquashSHA, UpdatedAt: updated}
	if wire.MergedAt != nil {
		merged, err := snapshotParseTime(*wire.MergedAt)
		if err != nil || merged.After(updated) || wire.State != "merged" {
			return zero, snapshotFailure("inconsistent merge request merge time")
		}
		out.MergedAt = &merged
	}
	if wire.State == "merged" && (out.MergedAt == nil || (wire.MergeSHA == "" && wire.SquashSHA == "")) {
		return zero, snapshotFailure("merged request has no durable merge result")
	}
	scope := []string{"gitlab-mr-snapshot-v1", r.base.String(), req.Repository, strconv.FormatInt(wire.ProjectID, 10), strconv.FormatInt(wire.ID, 10), strconv.FormatInt(wire.IID, 10)}
	out.Fingerprint = snapshotDigest(wire.State, wire.SHA, wire.Source, wire.Target, wire.MergeSHA, wire.SquashSHA)
	out.ObservationID = "gitlab:mr:v1:" + snapshotDigest(append(scope, updated.Format(time.RFC3339Nano), out.Fingerprint)...)
	if out.State == "merged" {
		out.MergeIdentity = "gitlab:merge:v1:" + snapshotDigest(append(scope, out.MergedAt.Format(time.RFC3339Nano), wire.MergeSHA, wire.SquashSHA)...)
		out.ObservationID = out.MergeIdentity
	}
	return out, nil
}

func snapshotParseTime(raw string) (time.Time, error) {
	if !snapshotTimestamp.MatchString(raw) {
		return time.Time{}, snapshotFailure("invalid or unsupported provider timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !validSnapshotTime(parsed) || strings.Contains(raw, ",") {
		return time.Time{}, snapshotFailure("invalid provider timestamp")
	}
	return parsed.UTC(), nil
}

func validSnapshotBranch(name string) bool {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") {
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

func snapshotDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
