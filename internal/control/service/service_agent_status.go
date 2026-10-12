package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func authenticatedAgent(ctx context.Context, requested string) (string, error) {
	info, ok := agentidentity.FromContext(ctx)
	if !ok || info == nil || info.ID == "" {
		return "", status.Error(codes.Unauthenticated, "agent authentication required")
	}
	if requested != info.ID {
		return "", status.Error(codes.PermissionDenied, "agent identity mismatch")
	}
	return info.ID, nil
}

func validateReportedStatus(current, next int, step bool) error {
	max := 7
	if step {
		max = 8
	}
	if next < 3 || next > max {
		return status.Error(codes.InvalidArgument, "invalid execution status")
	}
	if current == 3 && next == 3 {
		return status.Error(codes.FailedPrecondition, "execution already running")
	}
	if current >= 4 && current != next {
		return status.Error(codes.FailedPrecondition, "execution already completed")
	}
	return nil
}

func executionStatusUpdates(next int, message string, started, ended int64, previousStart *time.Time) map[string]any {
	updates := map[string]any{"status": next, "error_message": message}
	start := previousStart
	if started > 0 {
		value := time.Unix(started, 0)
		start = &value
		updates["start_time"] = value
	}
	if ended > 0 {
		end := time.Unix(ended, 0)
		updates["end_time"] = end
		if start != nil && !end.Before(*start) {
			updates["duration"] = end.Sub(*start).Milliseconds()
		}
	}
	return updates
}

type agentStepStatusRepository interface {
	PatchReportedStatus(context.Context, *model.StepRun, map[string]any) error
}

func patchAgentStepStatus(ctx context.Context, repository any, expected *model.StepRun, updates map[string]any) error {
	writer, ok := repository.(agentStepStatusRepository)
	if !ok {
		return status.Error(codes.Unimplemented, "atomic status repository unavailable")
	}
	return writer.PatchReportedStatus(ctx, expected, updates)
}

// terminalReportDigest 绑定完整结果与执行身份，避免把恢复生成的终态误认为已提交回执
func terminalReportDigest(request proto.Message, claim string, executionScope ...string) (string, error) {
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(proto.Clone(request))
	if err != nil {
		return "", status.Error(codes.InvalidArgument, "invalid terminal report")
	}
	digest := sha256.New()
	for _, value := range append([]string{"arcentra-terminal-report-v1", string(request.ProtoReflect().Descriptor().FullName()), claim}, executionScope...) {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	_, _ = digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateTerminalReplay(stored, reported string) error {
	if stored == "" || stored != reported {
		return status.Error(codes.FailedPrecondition, "terminal report does not match a committed receipt")
	}
	return nil
}

func reportClaim(ctx context.Context, current, next int, existing string) (string, bool, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("x-arcentra-claim-id")
	if len(values) != 1 {
		return "", false, status.Error(codes.InvalidArgument, "exactly one execution claim ID is required")
	}
	claim := strings.TrimSpace(values[0])
	if len(claim) > 64 {
		return "", false, status.Error(codes.InvalidArgument, "claim ID too long")
	}
	if claim == "" {
		return "", false, status.Error(codes.InvalidArgument, "execution claim ID is required")
	}
	if next == 3 {
		if current == 3 {
			if existing == claim {
				return claim, true, nil
			}
			return "", false, status.Error(codes.FailedPrecondition, "execution already claimed by another attempt")
		}
	}
	if existing != "" && claim != existing {
		return "", false, status.Error(codes.PermissionDenied, "execution claim does not match")
	}
	return claim, false, nil
}
