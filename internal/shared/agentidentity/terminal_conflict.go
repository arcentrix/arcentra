package agentidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const terminalConflictDomain = "arcentra.agent.terminal"
const terminalConflictReason = "IMMUTABLE_TERMINAL_CONFLICT"

// TerminalConflictInfo 绑定原请求和控制面已验证的不可变终态，不能视为执行成功回执
type TerminalConflictInfo struct {
	Kind                string `json:"kind"`
	AgentID             string `json:"agentId"`
	RunID               string `json:"runId"`
	ClaimID             string `json:"claimId"`
	ClaimSource         string `json:"claimSource"`
	PayloadDigest       string `json:"payloadDigest"`
	PipelineID          string `json:"pipelineId"`
	PipelineRunID       string `json:"pipelineRunId"`
	JobRunID            string `json:"jobRunId"`
	Attempt             int    `json:"attempt"`
	StepIndex           int    `json:"stepIndex"`
	PreviousStepRunID   string `json:"previousStepRunId,omitempty"`
	AuthoritativeStatus int    `json:"authoritativeStatus"`
	ReceiptDigest       string `json:"receiptDigest,omitempty"`
}

// TerminalPayloadDigest 计算完整确定性 protobuf 线载荷的摘要，包括未知字段
func TerminalPayloadDigest(request proto.Message) (string, error) {
	if request == nil || !request.ProtoReflect().IsValid() {
		return "", errors.New("terminal request is required")
	}
	switch request.(type) {
	case *agentv1.ReportJobRunStatusRequest, *agentv1.ReportStepRunStatusRequest:
	default:
		return "", errors.New("unsupported terminal request type")
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return "", errors.New("invalid terminal request")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// TerminalConflictError 仅供完成认证和存储层原认领验证后返回非重试终态冲突
func TerminalConflictError(info TerminalConflictInfo) error {
	if !validTerminalConflict(info) {
		return status.Error(codes.FailedPrecondition, "terminal conflict evidence unavailable")
	}
	result, err := status.New(codes.FailedPrecondition, "immutable terminal report conflict").WithDetails(&errdetails.ErrorInfo{
		Domain: terminalConflictDomain, Reason: terminalConflictReason, Metadata: terminalConflictMetadata(info),
	})
	if err != nil {
		return status.Error(codes.FailedPrecondition, "terminal conflict evidence unavailable")
	}
	return result.Err()
}

// ParseTerminalConflict 仅接受与完整原始终态请求及认领逐项匹配的唯一版本化冲突证据
func ParseTerminalConflict(err error, request proto.Message, claimID string) (TerminalConflictInfo, bool) {
	var zero TerminalConflictInfo
	result := status.Convert(err)
	if result.Code() != codes.FailedPrecondition || len(result.Details()) != 1 {
		return zero, false
	}
	detail, ok := result.Details()[0].(*errdetails.ErrorInfo)
	if !ok || detail.Domain != terminalConflictDomain || detail.Reason != terminalConflictReason {
		return zero, false
	}
	m := detail.Metadata
	if len(m) != 16 || m["version"] != "1" || m["nonRetryable"] != "true" {
		return zero, false
	}
	info := TerminalConflictInfo{Kind: m["kind"], AgentID: m["agentId"], RunID: m["runId"], ClaimID: m["claimId"], ClaimSource: m["claimSource"], PayloadDigest: m["payloadDigest"], PipelineID: m["pipelineId"], PipelineRunID: m["pipelineRunId"], JobRunID: m["jobRunId"], PreviousStepRunID: m["previousStepRunId"], ReceiptDigest: m["receiptDigest"]}
	for key, target := range map[string]*int{"attempt": &info.Attempt, "stepIndex": &info.StepIndex, "authoritativeStatus": &info.AuthoritativeStatus} {
		value, parseErr := strconv.Atoi(m[key])
		if parseErr != nil || strconv.Itoa(value) != m[key] {
			return zero, false
		}
		*target = value
	}
	if !validTerminalConflict(info) || info.ClaimID != claimID {
		return zero, false
	}
	expected := terminalConflictMetadata(info)
	for key, value := range expected {
		if actual, exists := m[key]; !exists || actual != value {
			return zero, false
		}
	}
	if request == nil || !request.ProtoReflect().IsValid() {
		return zero, false
	}
	switch req := request.(type) {
	case *agentv1.ReportJobRunStatusRequest:
		if info.Kind != "job" || info.AgentID != req.AgentId || info.RunID != req.JobRunId || req.Status < 4 || req.Status > 7 {
			return zero, false
		}
	case *agentv1.ReportStepRunStatusRequest:
		if info.Kind != "step" || info.AgentID != req.AgentId || info.RunID != req.StepRunId || int(req.Status) < 4 || int(req.Status) > 8 {
			return zero, false
		}
	default:
		return zero, false
	}
	digest, digestErr := TerminalPayloadDigest(request)
	if digestErr != nil || digest != info.PayloadDigest {
		return zero, false
	}
	return info, true
}

func validTerminalConflict(info TerminalConflictInfo) bool {
	for _, value := range []string{info.AgentID, info.RunID, info.ClaimID, info.PipelineID, info.PipelineRunID, info.JobRunID} {
		if !validTerminalID(value) {
			return false
		}
	}
	if info.PreviousStepRunID != "" && !validTerminalID(info.PreviousStepRunID) || !validTerminalDigest(info.PayloadDigest) || info.ReceiptDigest != "" && !validTerminalDigest(info.ReceiptDigest) {
		return false
	}
	if info.AuthoritativeStatus < 4 || info.AuthoritativeStatus > 8 || info.Attempt < 1 || info.StepIndex < 0 {
		return false
	}
	switch info.ClaimSource {
	case "execution_claim":
	case "cancelled_parent_claim":
		if info.Kind != "step" || info.AuthoritativeStatus != 6 {
			return false
		}
	case "terminal_parent_claim":
		if info.Kind != "step" || info.AuthoritativeStatus == 6 {
			return false
		}
	default:
		return false
	}
	switch info.Kind {
	case "job":
		return info.ClaimSource == "execution_claim" && info.JobRunID == info.RunID && info.Attempt == 1 && info.StepIndex == 0 && info.PreviousStepRunID == ""
	case "step":
		return info.Attempt == 1 && info.PreviousStepRunID == "" || info.Attempt > 1 && info.PreviousStepRunID != "" && info.PreviousStepRunID != info.RunID
	default:
		return false
	}
}

func validTerminalDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func terminalConflictMetadata(info TerminalConflictInfo) map[string]string {
	return map[string]string{"version": "1", "nonRetryable": "true", "kind": info.Kind, "agentId": info.AgentID, "runId": info.RunID, "claimId": info.ClaimID, "claimSource": info.ClaimSource, "payloadDigest": info.PayloadDigest, "pipelineId": info.PipelineID, "pipelineRunId": info.PipelineRunID, "jobRunId": info.JobRunID, "attempt": strconv.Itoa(info.Attempt), "stepIndex": strconv.Itoa(info.StepIndex), "previousStepRunId": info.PreviousStepRunID, "authoritativeStatus": strconv.Itoa(info.AuthoritativeStatus), "receiptDigest": info.ReceiptDigest}
}

// 标识仅使用有界可打印的业务 ID 字符，不允许远端标签携带 URL、凭据或终端控制序列
func validTerminalID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}
