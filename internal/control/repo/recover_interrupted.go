package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InterruptedRecoverySummary 描述显式离线恢复影响的活动记录数量
type InterruptedRecoverySummary struct{ Runs, Jobs, Steps, LinkedUnresolvedReleases int64 }

// RecoverInterruptedRuns 仅供操作员确认所有控制面和执行器已停止后的离线恢复
// 不重新执行任何命令；终态记录保持不变，活动记录转为明确失败后只能手动新建运行
func RecoverInterruptedRuns(ctx context.Context, db *gorm.DB, runIDs []string, apply bool) (InterruptedRecoverySummary, error) {
	var summary InterruptedRecoverySummary
	if db == nil {
		return summary, fmt.Errorf("database is required")
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runs []model.PipelineRun
		query := tx.Select("run_id").Where("status IN ?", []int{model.PipelineStatusPending, model.PipelineStatusRunning, model.PipelineStatusPaused})
		if len(runIDs) > 0 {
			query = query.Where("run_id IN ?", runIDs)
		}
		if apply {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&runs).Error; err != nil {
			return err
		}
		ids := make([]string, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.RunID)
		}
		summary.Runs = int64(len(ids))
		inspectedIDs := ids
		if len(runIDs) > 0 {
			inspectedIDs = runIDs
		}
		// Recovery never alters release state or its active target reservation.
		if len(inspectedIDs) > 0 && tx.Migrator().HasTable(&model.DeploymentRelease{}) {
			if err := tx.Model(&model.DeploymentRelease{}).Where("pipeline_run_id IN ? AND status IN ?", inspectedIDs, []string{"queued", "applying", "observing"}).Count(&summary.LinkedUnresolvedReleases).Error; err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			return nil
		}
		jobQuery := tx.Model(&model.JobRun{}).Where("pipeline_run_id IN ? AND status IN ?", ids, []int{1, 2, 3})
		stepQuery := tx.Model(&model.StepRun{}).Where("pipeline_run_id IN ? AND status IN ?", ids, []int{1, 2, 3})
		if err := jobQuery.Count(&summary.Jobs).Error; err != nil {
			return err
		}
		if err := stepQuery.Count(&summary.Steps).Error; err != nil {
			return err
		}
		if !apply {
			return nil
		}
		// 显式离线恢复只能撤销活动代次，保留原绑定及转换回执，不制造清理证明
		if tx.Migrator().HasTable(&model.JobRunGeneration{}) {
			activeJobs := tx.Session(&gorm.Session{NewDB: true}).Model(&model.JobRun{}).Select("job_run_id").Where("pipeline_run_id IN ? AND status IN ?", ids, []int{1, 2, 3})
			if err := tx.Model(&model.JobRunGeneration{}).Where("job_run_id IN (?) AND state IN ?", activeJobs, []string{"preparing", "active"}).Updates(map[string]any{"state": "aborted", "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		now := time.Now()
		reason := "interrupted execution reconciled offline; inspect external effects before starting a new run"
		if err := tx.Model(&model.JobRun{}).Where("pipeline_run_id IN ? AND status IN ?", ids, []int{1, 2, 3}).Updates(map[string]any{"status": model.JobRunStatusFailed, "end_time": now, "updated_at": now, "error_message": reason, "terminal_report_digest": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.StepRun{}).Where("pipeline_run_id IN ? AND status IN ?", ids, []int{1, 2, 3}).Updates(map[string]any{"status": 5, "end_time": now, "updated_at": now, "error_message": reason, "terminal_report_digest": ""}).Error; err != nil {
			return err
		}
		return tx.Model(&model.PipelineRun{}).Where("run_id IN ? AND status IN ?", ids, []int{model.PipelineStatusPending, model.PipelineStatusRunning, model.PipelineStatusPaused}).Updates(map[string]any{"status": model.PipelineStatusFailed, "end_time": now, "updated_at": now, "running_jobs": 0}).Error
	})
	return summary, err
}
