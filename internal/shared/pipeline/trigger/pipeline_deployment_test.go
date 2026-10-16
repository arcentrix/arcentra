package trigger

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"strings"
	"testing"
)

type deploymentCronPipelineRepo struct {
	repo.IPipelineRepository
	created int
}

func (r *deploymentCronPipelineRepo) Get(context.Context, string) (*model.Pipeline, error) {
	return &model.Pipeline{PipelineID: "pipeline", ProjectID: "project", IsEnabled: 1}, nil
}
func (r *deploymentCronPipelineRepo) CreateRun(context.Context, *model.PipelineRun) error {
	r.created++
	return nil
}

type deploymentCronProjectRepo struct{ repo.IProjectRepository }

func (*deploymentCronProjectRepo) Get(context.Context, string) (*model.Project, error) {
	return &model.Project{ProjectID: "project", IsEnabled: 1, Status: model.ProjectStatusActive}, nil
}
func TestCronRefusesDeploymentBeforeCreatingPendingRun(t *testing.T) {
	for _, triggers := range []string{`[]`, `[{"type":"cron","options":{"expression":"* * * * *"}}]`} {
		repository := &deploymentCronPipelineRepo{}
		manager := NewCronTriggerManager(nil, nil, repository, &deploymentCronProjectRepo{}, func(context.Context, *model.Pipeline, *model.Project) (string, string, error) {
			return `{"namespace":"test","triggers":` + triggers + `,"jobs":[{"name":"build","steps":[{"name":"upload","uses":"builtin:artifacts","action":"upload"}]},{"name":"deploy","timeout":"10s","dependsOn":["build"],"steps":[{"name":"release","uses":"builtin:kubernetes","action":"deploy","args":{"environmentId":"env","artifact":{"job":"build","step":"upload","name":"manifest.json"}}}]}]}`, strings.Repeat("a", 40), nil
		})
		manager.makeTriggerFunc("pipeline")()
		if repository.created != 0 {
			t.Fatal("cron created a phantom pending deployment run")
		}
		if expression := manager.extractCronExpression(context.Background(), &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}); expression != "" {
			t.Fatal("deployment scheduled automatically")
		}
	}
}
