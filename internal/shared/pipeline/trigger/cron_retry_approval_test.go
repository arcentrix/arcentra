// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package trigger

import (
	"context"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/cron"
)

type cronApprovalRepository struct{ deploymentCronPipelineRepo }

func (r *cronApprovalRepository) List(context.Context, *repo.PipelineQuery) ([]*model.Pipeline, int64, error) {
	return []*model.Pipeline{{PipelineID: "pipeline", ProjectID: "project", IsEnabled: 1}}, 1, nil
}
func (r *cronApprovalRepository) GetRunByRequestID(context.Context, string, string) (*model.PipelineRun, error) {
	return nil, nil
}

type cronApprovalEngine struct{ calls int }

func (e *cronApprovalEngine) Submit(*model.PipelineRun, *spec.Pipeline) error { e.calls++; return nil }

func TestCronRetryApprovalExcludedFromDiscoveryAndStaleEntry(t *testing.T) {
	repository := &cronApprovalRepository{}
	engine := &cronApprovalEngine{}
	scheduler := cron.New()
	manager := NewCronTriggerManager(scheduler, engine, repository, &deploymentCronProjectRepo{}, func(context.Context, *model.Pipeline, *model.Project) (string, string, error) {
		return `{"namespace":"test","triggers":[{"type":"cron","options":{"expression":"* * * * *"}}],"jobs":[{"name":"build","approval":{"required":true,"type":"manual","plugin":"builtin:retry-approval"},"steps":[{"name":"build","uses":"shell"}]}]}`, "definition", nil
	})
	manager.SyncAll(context.Background())
	if len(scheduler.Entries()) != 0 {
		t.Fatal("retry-approval pipeline registered automatically")
	}
	if err := manager.Register("pipeline", "* * * * *"); err != nil {
		t.Fatal(err)
	}
	scheduler.Entries()[0].Job.Run()
	if engine.calls != 0 || repository.created != 0 {
		t.Fatalf("stale cron entry bypassed exclusion: submitted=%d created=%d", engine.calls, repository.created)
	}
}
