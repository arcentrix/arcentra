package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
)

type approvalTimingArtifactRepo struct {
	repo.IArtifactRepository
	afterRead func()
}

func (r approvalTimingArtifactRepo) Get(ctx context.Context, project, id string) (*model.ProjectArtifact, error) {
	value, err := r.IArtifactRepository.Get(ctx, project, id)
	if err == nil {
		r.afterRead()
	}
	return value, err
}

func TestDeploymentApprovalFinalGuardRechecksAfterArtifactIO(t *testing.T) {
	for _, scenario := range []string{"expiry", "requester revoked", "reviewer revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, execution, _, approvals := deploymentApprovalFixture(t)
			binding := s.targets["staging"]
			binding.PipelineApproval.TimeoutSeconds = 2
			binding.Fingerprint = deploymentTargetFingerprint("staging", binding, "")
			s.targets["staging"] = binding
			if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", execution.PipelineRunID).Update("deployment_target_fingerprint", binding.Fingerprint).Error; err != nil {
				t.Fatal(err)
			}
			entry := prepareApprovedDeployment(t, s, approvals, execution)
			req, err := approvals.GetApproval(context.Background(), *entry.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			var reads, patches atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					reads.Add(1)
				}
				if r.Method == http.MethodPatch {
					patches.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"metadata":{"uid":"workload","generation":7,"resourceVersion":"7"},"spec":{"replicas":1,"template":{"metadata":{"annotations":{}},"spec":{"containers":[{"name":"app","image":"old:latest"}]}}}}`))
			}))
			defer server.Close()
			adapter, err := cluster.NewKubernetesAdapter(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			binding.Adapter = adapter
			s.targets["staging"] = binding
			requesterAllowed, reviewerAllowed := true, true
			s.pipelineAuthorize = func(context.Context, string, string, string, string) error {
				if !requesterAllowed {
					return errors.New("requester revoked during artifact read")
				}
				return nil
			}
			s.approvalAuthorize = func(context.Context, string, string, string, string) error {
				if !reviewerAllowed {
					return errors.New("reviewer revoked during artifact read")
				}
				return nil
			}
			triggered := false
			s.artifacts = approvalTimingArtifactRepo{IArtifactRepository: s.artifacts, afterRead: func() {
				if reads.Load() < 2 || triggered {
					return
				}
				triggered = true
				switch scenario {
				case "expiry":
					time.Sleep(time.Until(*req.ExpiresAt) + time.Millisecond)
				case "requester revoked":
					requesterAllowed = false
				case "reviewer revoked":
					reviewerAllowed = false
				}
			}}
			if _, err := s.advance(context.Background(), entry.ProjectID, entry.ReleaseID, execution.ActorID, true, false); err == nil {
				t.Fatal("final guard allowed stale approval/authorization after artifact IO")
			}
			if !triggered || reads.Load() != 2 || patches.Load() != 0 {
				t.Fatalf("final guard coverage failed: triggered=%v GET=%d PATCH=%d", triggered, reads.Load(), patches.Load())
			}
			current, err := s.GetRelease(context.Background(), entry.ProjectID, entry.ReleaseID)
			if err != nil || current.Status != "applying" {
				t.Fatalf("uncertainty state lost: %+v %v", current, err)
			}
		})
	}
}

func TestDeploymentApprovalDecisionRechecksDeadlineAndContextAfterAuthorization(t *testing.T) {
	for _, scenario := range []string{"expired", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, execution, _, approvals := deploymentApprovalFixture(t)
			binding := s.targets["staging"]
			binding.PipelineApproval.TimeoutSeconds = 1
			binding.Fingerprint = deploymentTargetFingerprint("staging", binding, "")
			s.targets["staging"] = binding
			if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", execution.PipelineRunID).Update("deployment_target_fingerprint", binding.Fingerprint).Error; err != nil {
				t.Fatal(err)
			}
			entry, err := s.PreparePipelineRelease(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			req, err := approvals.GetApproval(context.Background(), *entry.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approvalAuthorize = func(context.Context, string, string, string, string) error {
				if scenario == "expired" {
					time.Sleep(time.Until(*req.ExpiresAt) + time.Millisecond)
				} else {
					cancel()
				}
				return nil
			}
			if err := s.ValidateApprovalDecision(ctx, req, "reviewer", model.ApprovalDecisionApprove); err == nil {
				t.Fatal("validation outlived its deadline/context")
			}
			votes, err := s.approvalDecisions.ListByApproval(context.Background(), req.ApprovalID)
			if err != nil || len(votes) != 0 {
				t.Fatal("validation created a vote", err)
			}
		})
	}
}
