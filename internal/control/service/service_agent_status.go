package service

import (
	"context"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"time"
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

type agentStatusRepository interface {
	PatchStatusIfCurrent(context.Context, string, int, map[string]any) error
}

func patchAgentStatus(ctx context.Context, repository any, runID string, current int, updates map[string]any) error {
	writer, ok := repository.(agentStatusRepository)
	if !ok {
		return status.Error(codes.Unimplemented, "atomic status repository unavailable")
	}
	return writer.PatchStatusIfCurrent(ctx, runID, current, updates)
}

func reportClaim(ctx context.Context, current, next int, existing string) (string, bool, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("x-arcentra-claim-id")
	claim := ""
	if len(values) > 0 {
		claim = strings.TrimSpace(values[0])
	}
	if len(claim) > 64 {
		return "", false, status.Error(codes.InvalidArgument, "claim ID too long")
	}
	if next == 3 {
		if claim == "" {
			return "", false, status.Error(codes.InvalidArgument, "execution claim ID is required")
		}
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
