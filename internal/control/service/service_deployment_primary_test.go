package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

func TestDeploymentFinalApplyGuardRejectsLaggingReplicaState(t *testing.T) {
	for _, change := range []string{"release version", "release status", "environment reservation", "environment target"} {
		t.Run(change, func(t *testing.T) {
			s, primary, input, _ := releaseFixture(t)
			ctx := context.Background()
			entry, err := s.CreateRelease(ctx, "project", "user", input)
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := s.records.Transition(ctx, entry, "applying", map[string]any{"workload_uid": "workload", "baseline_generation": int64(7)}); err != nil || !changed {
				t.Fatalf("prepare applying release: changed=%v error=%v", changed, err)
			}
			entry, err = s.GetRelease(ctx, "project", entry.ReleaseID)
			if err != nil {
				t.Fatal(err)
			}
			environment, err := s.records.GetEnvironment(ctx, "project", input.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			var project model.Project
			if err := primary.Where("project_id = ?", "project").First(&project).Error; err != nil {
				t.Fatal(err)
			}
			replicaPath := filepath.Join(t.TempDir(), "stale-deployment-replica.db")
			replica, err := gorm.Open(sqlite.Open(replicaPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			replicaSQL, err := replica.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = replicaSQL.Close() })
			if err := replica.AutoMigrate(&model.Project{}, &model.DeploymentRelease{}, &model.DeploymentEnvironment{}); err != nil {
				t.Fatal(err)
			}
			for _, value := range []any{&project, entry, environment} {
				if err := replica.Create(value).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(replicaPath)}})); err != nil {
				t.Fatal(err)
			}
			reads, patches := 0, 0
			adapter, err := cluster.NewKubernetesAdapter("https://cluster.invalid", &http.Client{Transport: recoveryTestTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPatch {
					patches++
				}
				if request.Method == http.MethodGet {
					reads++
					var writeErr error
					switch change {
					case "release version":
						writeErr = primary.Clauses(database.Write()).Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Update("version", entry.Version+1).Error
					case "release status":
						writeErr = primary.Clauses(database.Write()).Model(&model.DeploymentRelease{}).Where("release_id = ?", entry.ReleaseID).Update("status", "cancelled").Error
					case "environment reservation":
						writeErr = primary.Clauses(database.Write()).Model(&model.DeploymentEnvironment{}).Where("environment_id = ?", input.EnvironmentID).Update("active_release_id", "different-release").Error
					case "environment target":
						writeErr = primary.Clauses(database.Write()).Model(&model.DeploymentEnvironment{}).Where("environment_id = ?", input.EnvironmentID).Update("namespace", "production").Error
					}
					if writeErr != nil {
						t.Fatal(writeErr)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"metadata":{"uid":"workload","generation":7,"resourceVersion":"7"},"spec":{"replicas":1,"template":{"metadata":{"annotations":{}},"spec":{"containers":[{"name":"app","image":"old:latest"}]}}}}`))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			binding := s.targets["staging"]
			binding.Adapter = adapter
			s.targets["staging"] = binding
			_, err = s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "user")
			expected := repo.ErrDeploymentConflict
			if change == "environment target" {
				expected = ErrDeploymentTargetChanged
			}
			if !errors.Is(err, expected) || reads != 1 || patches != 0 {
				t.Fatalf("lagging replica authorized PATCH: error=%v GET=%d PATCH=%d", err, reads, patches)
			}
			// Ordinary history reads intentionally still use the replica. These
			// stale values would have passed the previous final apply guard.
			stale, err := s.GetRelease(ctx, "project", entry.ReleaseID)
			if err != nil || stale.Status != "applying" || stale.Version != entry.Version {
				t.Fatalf("fixture did not preserve stale release: %+v %v", stale, err)
			}
			staleEnvironment, err := s.records.GetEnvironment(ctx, "project", input.EnvironmentID)
			if err != nil || staleEnvironment.ActiveReleaseID != entry.ReleaseID || staleEnvironment.Namespace != "staging" {
				t.Fatalf("fixture did not preserve stale environment: %+v %v", staleEnvironment, err)
			}
		})
	}
}

type noPrimaryDeploymentRecords struct{ repo.IDeploymentRepository }

type failingPrimaryDeploymentRecords struct{ repo.IDeploymentRepository }

func (f failingPrimaryDeploymentRecords) GetApplyState(context.Context, string, string) (*model.DeploymentRelease, *model.DeploymentEnvironment, error) {
	return nil, nil, errors.New("private database connection token=do-not-expose")
}

func TestDeploymentFinalApplyGuardRequiresAuthoritativeStateReader(t *testing.T) {
	for _, failure := range []string{"capability missing", "primary read failed"} {
		t.Run(failure, func(t *testing.T) {
			s, _, input, _ := releaseFixture(t)
			ctx := context.Background()
			entry, err := s.CreateRelease(ctx, "project", "user", input)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "capability missing" {
				s.records = noPrimaryDeploymentRecords{s.records}
			} else {
				s.records = failingPrimaryDeploymentRecords{s.records}
			}
			patches := 0
			adapter, err := cluster.NewKubernetesAdapter("https://cluster.invalid", &http.Client{Transport: recoveryTestTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPatch {
					patches++
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"metadata":{"uid":"workload","generation":7,"resourceVersion":"7"},"spec":{"replicas":1,"template":{"metadata":{"annotations":{}},"spec":{"containers":[{"name":"app","image":"old:latest"}]}}}}`))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			binding := s.targets["staging"]
			binding.Adapter = adapter
			s.targets["staging"] = binding
			if _, err := s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "user"); err != repo.ErrDeploymentApplyState || patches != 0 {
				t.Fatalf("missing primary state permitted fallback or leaked error: error=%v PATCH=%d", err, patches)
			}
		})
	}
}
