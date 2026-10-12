// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
)

const (
	terminalConflictDisposition   = "terminal_conflict"
	terminalDependencyDisposition = "dependency_quarantined"
)

// terminalQuarantine 与原始收据原子保存，原始收据校验和不因隔离或人工复核改变
// Checksum 绑定不可变拒绝证据；Reviews 仅追加一条独立校验的本地复核事件
// 本地校验和用于检测损坏，并非抵抗可改写整个目录的本地账户的密码学签名
type terminalQuarantine struct {
	Version         int                                 `json:"version"`
	Disposition     string                              `json:"disposition"`
	ReceiptChecksum string                              `json:"receiptChecksum"`
	QuarantinedAt   string                              `json:"quarantinedAt"`
	Conflict        *agentidentity.TerminalConflictInfo `json:"conflict,omitempty"`
	Dependency      *terminalQuarantineDependency       `json:"dependency,omitempty"`
	Checksum        string                              `json:"checksum"`
	Reviews         []TerminalQuarantineReview          `json:"reviews,omitempty"`
}

type terminalQuarantineDependency struct {
	RunID              string `json:"runId"`
	ReceiptChecksum    string `json:"receiptChecksum"`
	QuarantineChecksum string `json:"quarantineChecksum"`
}

// TerminalQuarantineReview 记录本机操作员的固定复核结论，不代表应用账户认证或控制面确认
// 复核不会删除隔离记录、重新上报或者允许同一执行身份再次运行
type TerminalQuarantineReview struct {
	ReviewedAt         string `json:"reviewedAt"`
	Disposition        string `json:"disposition"`
	QuarantineChecksum string `json:"quarantineChecksum"`
	Checksum           string `json:"checksum"`
}

// TerminalQuarantineSummary 提供不含原始请求、执行输出或远端错误消息的本地安全摘要
type TerminalQuarantineSummary struct {
	Kind                         string                              `json:"kind"`
	RunID                        string                              `json:"runId"`
	ClaimID                      string                              `json:"claimId"`
	Binding                      string                              `json:"binding"`
	ReceiptChecksum              string                              `json:"receiptChecksum"`
	RequestedStatus              int32                               `json:"requestedStatus"`
	Reason                       string                              `json:"reason"`
	QuarantinedAt                string                              `json:"quarantinedAt"`
	QuarantineChecksum           string                              `json:"quarantineChecksum"`
	Rejection                    *agentidentity.TerminalConflictInfo `json:"rejection,omitempty"`
	DependencyRunID              string                              `json:"dependencyRunId,omitempty"`
	DependencyReceiptChecksum    string                              `json:"dependencyReceiptChecksum,omitempty"`
	DependencyQuarantineChecksum string                              `json:"dependencyQuarantineChecksum,omitempty"`
	Reviews                      []TerminalQuarantineReview          `json:"reviews,omitempty"`
}

// TerminalQuarantine 通过与 Agent 相同的独占锁提供停机后的本地操作员审查
type TerminalQuarantine struct{ outbox *terminalOutbox }

// OpenTerminalQuarantine 打开已有且绑定正确 Agent 和控制面的隔离目录，运行中的 Agent 必须先停止
func OpenTerminalQuarantine(ctx context.Context, directory, agentID, controller string) (*TerminalQuarantine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 仅打开已有目录，权限和身份检查均不修改原有文件
	outbox, err := openTerminalOutbox(ctx, directory, agentID, controller, true)
	if err != nil {
		return nil, err
	}
	return &TerminalQuarantine{outbox: outbox}, nil
}

// Close 释放本地隔离目录的独占锁
func (q *TerminalQuarantine) Close() error {
	if q == nil || q.outbox == nil {
		return nil
	}
	return q.outbox.close()
}

// List 返回按种类和运行身份排序的安全隔离摘要，不返回原始执行负载
func (q *TerminalQuarantine) List(ctx context.Context) ([]TerminalQuarantineSummary, error) {
	if q == nil || q.outbox == nil {
		return nil, errTerminalOutboxClosed
	}
	o := q.outbox
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.quarantineReady(ctx); err != nil {
		return nil, err
	}
	summaries := make([]TerminalQuarantineSummary, 0)
	for _, entry := range o.entries {
		if entry.record.Quarantine != nil {
			summaries = append(summaries, summarizeTerminalQuarantine(entry.record))
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Kind != summaries[j].Kind {
			return summaries[i].Kind < summaries[j].Kind
		}
		return summaries[i].RunID < summaries[j].RunID
	})
	return summaries, nil
}

// Inspect 查询单个隔离身份的安全证据，不返回原始终态请求
func (q *TerminalQuarantine) Inspect(ctx context.Context, kind, runID string) (TerminalQuarantineSummary, error) {
	if q == nil || q.outbox == nil {
		return TerminalQuarantineSummary{}, errTerminalOutboxClosed
	}
	o := q.outbox
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.quarantineReady(ctx); err != nil {
		return TerminalQuarantineSummary{}, err
	}
	entry, exists := o.entries[terminalRecordName(kind, runID)]
	if !exists || entry.record.Quarantine == nil {
		return TerminalQuarantineSummary{}, errors.New("terminal quarantine entry not found")
	}
	return summarizeTerminalQuarantine(entry.record), nil
}

// MarkReviewed 追加一次本地复核审计，仅接受 investigated 或 verified_external_effects 且永久保留去重身份
func (q *TerminalQuarantine) MarkReviewed(ctx context.Context, kind, runID, disposition string) (TerminalQuarantineSummary, error) {
	if q == nil || q.outbox == nil {
		return TerminalQuarantineSummary{}, errTerminalOutboxClosed
	}
	o := q.outbox
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.quarantineReady(ctx); err != nil {
		return TerminalQuarantineSummary{}, err
	}
	if !validTerminalReviewDisposition(disposition) {
		return TerminalQuarantineSummary{}, errors.New("invalid terminal quarantine review disposition")
	}
	name := terminalRecordName(kind, runID)
	entry, exists := o.entries[name]
	if !exists || entry.record.Quarantine == nil {
		return TerminalQuarantineSummary{}, errors.New("terminal quarantine entry not found")
	}
	if len(entry.record.Quarantine.Reviews) > 0 {
		if entry.record.Quarantine.Reviews[0].Disposition != disposition {
			return TerminalQuarantineSummary{}, errors.New("terminal quarantine review is immutable")
		}
		return summarizeTerminalQuarantine(entry.record), nil
	}
	quarantine := *entry.record.Quarantine
	at := time.Now().UTC().Format(time.RFC3339Nano)
	// 时钟回拨不允许生成违反已保存时间顺序的审计
	if before, _ := time.Parse(time.RFC3339Nano, at); before.Before(mustTerminalTime(quarantine.QuarantinedAt)) {
		at = quarantine.QuarantinedAt
	}
	review := TerminalQuarantineReview{ReviewedAt: at, Disposition: disposition, QuarantineChecksum: quarantine.Checksum}
	var err error
	review.Checksum, err = terminalJSONChecksum(review)
	if err != nil {
		return TerminalQuarantineSummary{}, err
	}
	quarantine.Reviews = []TerminalQuarantineReview{review}
	record := entry.record
	record.Quarantine = &quarantine
	if err := o.replaceQuarantineLocked(ctx, name, record); err != nil {
		return TerminalQuarantineSummary{}, err
	}
	return summarizeTerminalQuarantine(record), nil
}

func (o *terminalOutbox) quarantineReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.closed {
		return errTerminalOutboxClosed
	}
	return o.fatalErr
}

func authenticatedTerminalController(client *agentgrpc.ClientWrapper) bool {
	return client != nil && client.ControllerAuthenticated()
}

func terminalRequest(r terminalRecord) (proto.Message, error) {
	var request proto.Message
	switch r.Kind {
	case "step":
		request = &agentv1.ReportStepRunStatusRequest{}
	case "job":
		request = &agentv1.ReportJobRunStatusRequest{}
	default:
		return nil, errors.New("invalid terminal report kind")
	}
	if err := proto.Unmarshal(r.Payload, request); err != nil {
		return nil, errors.New("invalid terminal quarantine payload")
	}
	return request, nil
}

func (o *terminalOutbox) quarantineConflictLocked(ctx context.Context, name string, cause error) error {
	entry := o.entries[name]
	request, err := terminalRequest(entry.record)
	if err != nil {
		return terminalPersistenceFailure(err)
	}
	conflict, valid := agentidentity.ParseTerminalConflict(cause, request, entry.record.ClaimID)
	if !valid {
		return cause
	}
	quarantine := &terminalQuarantine{
		Version: 1, Disposition: terminalConflictDisposition,
		ReceiptChecksum: entry.record.Checksum, QuarantinedAt: time.Now().UTC().Format(time.RFC3339Nano), Conflict: &conflict,
	}
	return o.persistQuarantineLocked(ctx, name, quarantine)
}

func (o *terminalOutbox) quarantineDependencyLocked(ctx context.Context, name, dependencyName string) error {
	entry, exists := o.entries[name]
	dependency, found := o.entries[dependencyName]
	if !exists || !found || dependency.record.Quarantine == nil {
		return terminalPersistenceFailure(errors.New("terminal quarantine dependency evidence missing"))
	}
	quarantine := &terminalQuarantine{
		Version: 1, Disposition: terminalDependencyDisposition,
		ReceiptChecksum: entry.record.Checksum, QuarantinedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Dependency: &terminalQuarantineDependency{RunID: dependency.record.RunID, ReceiptChecksum: dependency.record.Checksum, QuarantineChecksum: dependency.record.Quarantine.Checksum},
	}
	return o.persistQuarantineLocked(ctx, name, quarantine)
}

func (o *terminalOutbox) persistQuarantineLocked(ctx context.Context, name string, quarantine *terminalQuarantine) error {
	var err error
	quarantine.Checksum, err = terminalJSONChecksum(quarantine)
	if err != nil {
		return terminalPersistenceFailure(err)
	}
	record := o.entries[name].record
	record.Quarantine = quarantine
	if err := o.replaceQuarantineLocked(ctx, name, record); err != nil {
		return terminalPersistenceFailure(err)
	}
	return &terminalQuarantinedError{}
}

func (o *terminalOutbox) replaceQuarantineLocked(ctx context.Context, name string, record terminalRecord) error {
	if err := o.quarantineReady(ctx); err != nil {
		return err
	}
	old, found := o.entries[name]
	if !found || old.record.Checksum != record.Checksum || record.Quarantine == nil {
		return errors.New("terminal quarantine cannot change original receipt")
	}
	if previous := old.record.Quarantine; previous != nil {
		if previous.Checksum != record.Quarantine.Checksum {
			return errors.New("terminal quarantine evidence is immutable")
		}
		if len(record.Quarantine.Reviews) < len(previous.Reviews) {
			return errors.New("terminal quarantine review cannot be removed")
		}
		for i, review := range previous.Reviews {
			if record.Quarantine.Reviews[i] != review {
				return errors.New("terminal quarantine review is immutable")
			}
		}
	}
	if err := o.validateRecord(record); err != nil {
		return err
	}
	data, err := sonic.Marshal(record)
	if err != nil {
		return errors.New("cannot encode terminal quarantine")
	}
	if len(data) > terminalOutboxMaxRecordBytes {
		return errors.New("terminal quarantine exceeds record size limit")
	}
	if int64(len(data))-old.size > o.maxBytes-o.bytes {
		return errTerminalOutboxFull
	}

	if err := o.writeAtomic(ctx, name, data); err != nil {
		return err
	}
	o.entries[name] = terminalEntry{record: record, size: int64(len(data))}
	o.bytes += int64(len(data)) - old.size
	return nil
}

func terminalJSONChecksum(value any) (string, error) {
	data, err := sonic.Marshal(value)
	if err != nil {
		return "", errors.New("cannot checksum terminal quarantine")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validTerminalDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func mustTerminalTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func validTerminalTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero() && parsed.UTC().Format(time.RFC3339Nano) == value
}

func validTerminalReviewDisposition(value string) bool {
	return value == "investigated" || value == "verified_external_effects"
}

func validateTerminalQuarantine(record terminalRecord) error {
	q := record.Quarantine
	if q == nil {
		return nil
	}
	invalid := errors.New("invalid terminal quarantine evidence")
	if q.Version != 1 || q.ReceiptChecksum != record.Checksum || !validTerminalTime(q.QuarantinedAt) || !validTerminalDigest(q.Checksum) {
		return invalid
	}
	switch q.Disposition {
	case terminalConflictDisposition:
		if q.Conflict == nil || q.Dependency != nil {
			return invalid
		}
		request, err := terminalRequest(record)
		if err != nil {
			return invalid
		}
		if _, valid := agentidentity.ParseTerminalConflict(agentidentity.TerminalConflictError(*q.Conflict), request, record.ClaimID); !valid {
			return invalid
		}
	case terminalDependencyDisposition:
		if q.Conflict != nil || q.Dependency == nil || record.Kind != "job" {
			return invalid
		}
		d := q.Dependency
		if !validTerminalDigest(d.ReceiptChecksum) || !validTerminalDigest(d.QuarantineChecksum) {
			return invalid
		}
		found := false
		for _, runID := range record.Dependencies {
			if runID == d.RunID {
				found = true
				break
			}
		}
		if !found {
			return invalid
		}
	default:
		return invalid
	}
	immutable := *q
	immutable.Checksum, immutable.Reviews = "", nil
	checksum, err := terminalJSONChecksum(immutable)
	if err != nil || checksum != q.Checksum {
		return invalid
	}
	if len(q.Reviews) > 1 {
		return invalid
	}
	for _, review := range q.Reviews {
		if !validTerminalTime(review.ReviewedAt) || mustTerminalTime(review.ReviewedAt).Before(mustTerminalTime(q.QuarantinedAt)) || !validTerminalReviewDisposition(review.Disposition) || review.QuarantineChecksum != q.Checksum {
			return invalid
		}
		expected := review.Checksum
		review.Checksum = ""
		actual, err := terminalJSONChecksum(review)
		if err != nil || actual != expected {
			return invalid
		}
	}
	return nil
}

func (o *terminalOutbox) validateQuarantineDependencies() error {
	for _, entry := range o.entries {
		q := entry.record.Quarantine
		if q == nil || q.Dependency == nil {
			continue
		}
		dependency, found := o.entries[terminalRecordName("step", q.Dependency.RunID)]
		if !found || dependency.record.Quarantine == nil || dependency.record.Checksum != q.Dependency.ReceiptChecksum || dependency.record.Quarantine.Checksum != q.Dependency.QuarantineChecksum {
			return errors.New("terminal quarantine dependency evidence missing or changed")
		}
	}
	return nil
}

func summarizeTerminalQuarantine(record terminalRecord) TerminalQuarantineSummary {
	q := record.Quarantine
	result := TerminalQuarantineSummary{
		Kind: record.Kind, RunID: record.RunID, ClaimID: record.ClaimID, Binding: record.Binding, ReceiptChecksum: record.Checksum,
		Reason: q.Disposition, QuarantinedAt: q.QuarantinedAt, QuarantineChecksum: q.Checksum,
		Reviews: append([]TerminalQuarantineReview(nil), q.Reviews...),
	}
	request, _ := terminalRequest(record)
	switch request := request.(type) {
	case *agentv1.ReportStepRunStatusRequest:
		result.RequestedStatus = int32(request.Status)
	case *agentv1.ReportJobRunStatusRequest:
		result.RequestedStatus = request.Status
	}
	if q.Conflict != nil {
		copied := *q.Conflict
		result.Rejection = &copied
	}
	if q.Dependency != nil {
		result.DependencyRunID = q.Dependency.RunID
		result.DependencyReceiptChecksum = q.Dependency.ReceiptChecksum
		result.DependencyQuarantineChecksum = q.Dependency.QuarantineChecksum
	}
	return result
}
