package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/id"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const executionClaimMetadataKey = "x-arcentra-claim-id"

// executionClaims 仅在当前进程保留认领身份，响应丢失重试不会获得新的执行权
var executionClaims sync.Map

func executionClaimID(ctx context.Context, kind, runID string) string {
	key := kind + "/" + receiptKey(ctx, kind, runID)
	claim, ok := executionClaims.Load(key)
	if !ok {
		claim, _ = executionClaims.LoadOrStore(key, id.GetUUID())
	}
	return claim.(string)
}

func withExecutionClaim(ctx context.Context, kind, runID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, executionClaimMetadataKey, executionClaimID(ctx, kind, runID))
}

func completeExecutionClaim(ctx context.Context, kind, runID string) {
	executionClaims.Delete(kind + "/" + receiptKey(ctx, kind, runID))
}

// claimStepRun 对不确定的步骤认领响应就地重试，避免重启已开始的作业和重复前序步骤
func claimStepRun(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig, stepRunID string, start time.Time) error {
	delay := 100 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return reportCancelledStepClaim(ctx, client, conf, stepRunID, start, err)
		}
		err := reportStepRunStatus(ctx, client, conf, stepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING, 0, "", start, 0, nil)
		if err == nil {
			return nil
		}
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Internal:
			// RPC 使用独立认证上下文，下一轮在作业取消后报告终态
		default:
			return fmt.Errorf("claim step run: %w", err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return reportCancelledStepClaim(ctx, client, conf, stepRunID, start, ctx.Err())
		case <-timer.C:
			delay = min(delay*2, 5*time.Second)
		}
	}
}

func reportCancelledStepClaim(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig, stepRunID string, start time.Time, err error) error {
	terminal := steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
	if errors.Is(err, context.DeadlineExceeded) {
		terminal = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
	}
	end := time.Now().Unix()
	return retainStepReport(ctx, stepRunID, err, func(reportCtx context.Context) error {
		return reportStepRunStatus(reportCtx, client, conf, stepRunID, terminal, 1, err.Error(), start, end, nil)
	})
}
