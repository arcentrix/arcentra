package repo

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"time"
)

// PatchStatusIfCurrent 防止迟到或并发状态报告覆盖步骤终态
func (r *StepRunRepo) PatchStatusIfCurrent(ctx context.Context, runID string, current int, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	result := r.Database().WithContext(ctx).Model(&model.StepRun{}).
		Where("step_run_id = ? AND status = ?", runID, current).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution state changed")
	}
	return nil
}

// PatchStatusIfCurrent 防止迟到或并发状态报告覆盖作业终态
func (r *JobRunRepo) PatchStatusIfCurrent(ctx context.Context, runID string, current int, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	result := r.Database().WithContext(ctx).Model(&model.JobRun{}).
		Where("job_run_id = ? AND status = ?", runID, current).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution state changed")
	}
	return nil
}
