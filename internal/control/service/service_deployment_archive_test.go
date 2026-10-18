package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"gorm.io/gorm"
)

func liveManualReleaseFixture(t *testing.T) (*DeploymentService, *gorm.DB, DeploymentReleaseInput, *releaseFakeAdapter) {
	t.Helper()
	s, db, input, adapter := releaseFixture(t)
	repositories := liveDeploymentRepositories(t, db, false)
	s.ConfigurePipelineDeployments(nil, nil, NewPipelineDeploymentAuthorizer(repositories, s))
	return s, db, input, adapter
}

func archiveReleaseProject(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentManualPreparationRejectsArchivedProject(t *testing.T) {
	for _, timing := range []string{"before preparation", "during artifact read"} {
		t.Run(timing, func(t *testing.T) {
			s, db, input, adapter := liveManualReleaseFixture(t)
			if timing == "before preparation" {
				archiveReleaseProject(t, db)
			} else {
				s.artifacts = approvalTimingArtifactRepo{IArtifactRepository: s.artifacts, afterRead: func() { archiveReleaseProject(t, db) }}
			}
			if release, err := s.CreateRelease(context.Background(), "project", "actor", input); release != nil || !errors.Is(err, ErrPipelineDeploymentDenied) {
				t.Fatalf("archived project prepared release: %+v %v", release, err)
			}
			var count int64
			if err := db.Model(&model.DeploymentRelease{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("denied preparation persisted release: count=%d error=%v", count, err)
			}
			env, err := s.records.GetEnvironment(context.Background(), "project", input.EnvironmentID)
			if err != nil || env.ActiveReleaseID != "" || adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatalf("denied preparation reserved or accessed target: %+v %v", env, err)
			}
		})
	}
}

func TestDeploymentManualAdvanceRejectsArchivedProjectWithoutCancellation(t *testing.T) {
	for _, state := range []string{"queued", "applying"} {
		t.Run(state, func(t *testing.T) {
			s, db, input, adapter := liveManualReleaseFixture(t)
			ctx := context.Background()
			entry, err := s.CreateRelease(ctx, "project", "actor", input)
			if err != nil {
				t.Fatal(err)
			}
			if state == "applying" {
				if changed, err := s.records.Transition(ctx, entry, "applying", map[string]any{"workload_uid": "workload", "baseline_generation": int64(7)}); err != nil || !changed {
					t.Fatalf("prepare uncertain release: changed=%v error=%v", changed, err)
				}
			}
			archiveReleaseProject(t, db)
			if _, err := s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor"); !errors.Is(err, ErrPipelineDeploymentDenied) {
				t.Fatalf("archived project advanced %s release: %v", state, err)
			}
			if _, err := s.CreateRelease(ctx, "project", "actor", input); !errors.Is(err, ErrPipelineDeploymentDenied) {
				t.Fatalf("duplicate preparation bypassed current authorization: %v", err)
			}
			current, err := s.GetRelease(ctx, "project", entry.ReleaseID)
			if err != nil || current.Status != state {
				t.Fatalf("archival cancelled or changed release: %+v %v", current, err)
			}
			env, err := s.records.GetEnvironment(ctx, "project", input.EnvironmentID)
			if err != nil || env.ActiveReleaseID != entry.ReleaseID || adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatalf("archival lost reservation or accessed cluster: %+v %v", env, err)
			}
			if state == "queued" {
				cancelled, err := s.Cancel(ctx, "project", entry.ReleaseID)
				if err != nil || cancelled.Status != "cancelled" {
					t.Fatalf("safe explicit cancellation unavailable: %+v %v", cancelled, err)
				}
			} else if _, err := s.Cancel(ctx, "project", entry.ReleaseID); !errors.Is(err, ErrDeploymentCancel) {
				t.Fatalf("uncertain release cancelled: %v", err)
			}
		})
	}
}

func TestDeploymentManualArchiveAfterInspectPreventsApply(t *testing.T) {
	s, db, input, adapter := liveManualReleaseFixture(t)
	ctx := context.Background()
	entry, err := s.CreateRelease(ctx, "project", "actor", input)
	if err != nil {
		t.Fatal(err)
	}
	adapter.afterInspect = func() { archiveReleaseProject(t, db) }
	if _, err := s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor"); !errors.Is(err, ErrPipelineDeploymentDenied) {
		t.Fatalf("archival during inspection allowed apply: %v", err)
	}
	if adapter.inspect != 1 || adapter.apply != 0 || adapter.observe != 0 {
		t.Fatalf("archived project accessed target: %+v", adapter)
	}
}

func TestDeploymentManualFinalApplyGuardRechecksAfterLastKubernetesRead(t *testing.T) {
	for _, revoke := range []string{"archived project", "revoked permission", "authorization unavailable"} {
		t.Run(revoke, func(t *testing.T) {
			s, db, input, _ := liveManualReleaseFixture(t)
			reads, patches := 0, 0
			adapter, err := cluster.NewKubernetesAdapter("https://private-cluster.invalid", &http.Client{Transport: recoveryTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPatch {
					patches++
				}
				if r.Method == http.MethodGet {
					reads++
					if reads == 2 {
						switch revoke {
						case "archived project":
							archiveReleaseProject(t, db)
						case "revoked permission":
							if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error; err != nil {
								t.Fatal(err)
							}
						case "authorization unavailable":
							s.pipelineAuthorize = func(context.Context, string, string, string, string) error {
								return errors.New("private-cluster.invalid token=secret-authorizer-error")
							}
						}
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
			ctx := context.Background()
			entry, err := s.CreateRelease(ctx, "project", "actor", input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor"); err != ErrPipelineDeploymentDenied {
				t.Fatalf("final guard failed or leaked authorization detail: %v", err)
			}
			if reads != 2 || patches != 0 {
				t.Fatalf("final guard must run after last GET and before PATCH: GET=%d PATCH=%d", reads, patches)
			}
			current, err := s.GetRelease(ctx, "project", entry.ReleaseID)
			if err != nil || current.Status != "applying" || current.WorkloadUID != "workload" || current.BaselineGeneration != 7 {
				t.Fatalf("denial lost durable baseline: %+v %v", current, err)
			}
			env, err := s.records.GetEnvironment(ctx, "project", input.EnvironmentID)
			if err != nil || env.ActiveReleaseID != entry.ReleaseID {
				t.Fatalf("denial lost environment reservation: %+v %v", env, err)
			}
		})
	}
}

func TestDeploymentManualArchivedProjectMayObserveAdmittedRollout(t *testing.T) {
	s, db, input, adapter := liveManualReleaseFixture(t)
	ctx := context.Background()
	entry, err := s.CreateRelease(ctx, "project", "actor", input)
	if err != nil {
		t.Fatal(err)
	}
	entry, err = s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor")
	if err != nil || entry.Status != "observing" {
		t.Fatalf("admit release: %+v %v", entry, err)
	}
	archiveReleaseProject(t, db)
	entry, err = s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor")
	if err != nil || entry.Status != "succeeded" {
		t.Fatalf("archival prevented existing rollout observation: %+v %v", entry, err)
	}
	if _, err := s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, "actor"); err != nil || adapter.apply != 1 || adapter.observe != 1 {
		t.Fatalf("terminal observation re-executed release: %v", err)
	}
}

func TestDeploymentManualWritesRequireConfiguredCurrentAuthorizer(t *testing.T) {
	for _, stage := range []string{"prepare", "queued", "applying"} {
		for _, reason := range []string{"missing authorizer", "authorizer error", "anonymous actor"} {
			t.Run(stage+"/"+reason, func(t *testing.T) {
				s, _, input, adapter := releaseFixture(t)
				ctx := context.Background()
				var entry *model.DeploymentRelease
				if stage != "prepare" {
					var err error
					entry, err = s.CreateRelease(ctx, "project", "user", input)
					if err != nil {
						t.Fatal(err)
					}
					if stage == "applying" {
						if changed, err := s.records.Transition(ctx, entry, "applying", map[string]any{"workload_uid": "workload", "baseline_generation": int64(7)}); err != nil || !changed {
							t.Fatalf("prepare uncertain release: changed=%v error=%v", changed, err)
						}
					}
				}
				actor := "user"
				switch reason {
				case "missing authorizer":
					s.pipelineAuthorize = nil
				case "authorizer error":
					s.pipelineAuthorize = func(context.Context, string, string, string, string) error {
						return errors.New("token=secret-authorizer-error")
					}
				case "anonymous actor":
					actor = ""
				}
				var err error
				if stage == "prepare" {
					_, err = s.CreateRelease(ctx, "project", actor, input)
				} else if actor == "" {
					_, err = s.Advance(ctx, "project", entry.ReleaseID)
				} else {
					_, err = s.AdvanceAuthorized(ctx, "project", entry.ReleaseID, actor)
				}
				expected := ErrPipelineDeploymentDenied
				if stage == "prepare" && actor == "" {
					expected = ErrDeploymentInput
				}
				if err != expected || adapter.inspect+adapter.apply+adapter.observe != 0 {
					t.Fatalf("unauthorized write or leaked error: %v, adapter=%+v", err, adapter)
				}
			})
		}
	}
}
