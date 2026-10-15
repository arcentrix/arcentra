// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type mysqlAuthFixture struct {
	db                *gorm.DB
	services          *service.Services
	repos             *repo.Repositories
	request           *retryapproval.Request
	approvalID, voter string
}

func mysqlAuthGeneration(t *testing.T, db *gorm.DB, prefix, pipeline string) (taskqueue.JobGenerationTransition, *taskqueue.JobInputManifest) {
	return mysqlAuthGenerationMode(t, db, prefix, pipeline, "whole_job")
}

func mysqlAuthGenerationMode(t *testing.T, db *gorm.DB, prefix, pipeline, mode string) (taskqueue.JobGenerationTransition, *taskqueue.JobInputManifest) {
	ctx := context.Background()
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.JobRunGeneration{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{&model.Agent{AgentID: prefix + "agent", IsEnabled: 1}, &model.PipelineRun{Env: "{}", RunID: prefix + "run", PipelineID: pipeline, Status: model.PipelineStatusRunning}, &model.JobRun{Env: "{}", ArtifactReferences: "{}", ArtifactURIs: "{}", JobRunID: prefix + "job", PipelineID: pipeline, PipelineRunID: prefix + "run", Status: 1}} {
		if err := db.WithContext(ctx).Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	policy := &pipelinev1.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
	policyJSON, _ := sonic.MarshalString(policy)
	stepArgs := "{}"
	if mode == "failed_step" {
		stepArgs = `{"run":"exit 9"}`
	}
	for index, id := range []string{prefix + "step-1", prefix + "step-2"} {
		previous := ""
		if index > 0 {
			previous = prefix + "step-1"
		}
		if err := db.WithContext(ctx).Create(&model.StepRun{Args: stepArgs, Env: "{}", Secrets: "{}", LabelSelector: "{}", StepRunID: id, Name: "shell", JobRunID: prefix + "job", PipelineID: pipeline, PipelineRunID: prefix + "run", Attempt: index + 1, RetryMode: mode, RetryPolicy: policyJSON, PreviousStepRunID: previous, Uses: "shell", Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = taskqueue.RuntimeBindingDigest(pipeline, prefix+"run", prefix+"job", prefix+"agent", runtime)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: pipeline, PipelineRunID: prefix + "run", JobRunID: prefix + "job", AgentID: prefix + "agent", Runtime: runtime, Retry: policy, Steps: []taskqueue.StepPayload{{Name: "shell", StepRunID: prefix + "step-1", AttemptRunIDs: []string{prefix + "step-1", prefix + "step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
	if err := jobs.CaptureRuntimeRetryExpectation(ctx, pipeline, prefix+"run", prefix+"job", prefix+"agent", nil, runtime, nil, nil, plan); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.GetByJobRunID(ctx, prefix+"job")
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "original"}); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.GetByJobRunID(ctx, prefix+"job")
	identity := taskqueue.JobInputIdentity{PipelineID: pipeline, PipelineRunID: prefix + "run", JobRunID: prefix + "job", AgentID: prefix + "agent", ClaimID: "original", RuntimeBindingDigest: runtime.BindingDigest}
	req := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	req.RequestDigest, _ = req.Digest()
	manifest, _ := taskqueue.NewJobInputManifest("", nil, nil)
	return req, manifest
}
func mysqlAuthSeed(t *testing.T, db *gorm.DB, prefix, project, pipeline, actor, voter string, timeout int, expiry time.Time) *mysqlAuthFixture {
	return mysqlAuthSeedMode(t, db, prefix, project, pipeline, actor, voter, timeout, expiry, "whole_job")
}

func mysqlAuthSeedMode(t *testing.T, db *gorm.DB, prefix, project, pipeline, actor, voter string, timeout int, expiry time.Time, mode string) *mysqlAuthFixture {
	t.Helper()
	admit, manifest := mysqlAuthGenerationMode(t, db, prefix, pipeline, mode)
	ctx := context.Background()
	if err := db.AutoMigrate(&model.Pipeline{}, &model.Project{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.Pipeline{Metadata: "{}", PipelineID: pipeline, ProjectID: project, IsEnabled: 1}, &model.Project{ProjectID: project, IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.User{UserID: actor, IsEnabled: 1}, &model.User{UserID: voter, IsEnabled: 1}, &model.Role{RoleID: prefix + "review-role", Name: "review", IsEnabled: 1},
		&model.Team{TeamID: prefix + "team", IsEnabled: 1}, &model.TeamMember{TeamID: prefix + "team", UserID: voter},
		&model.RoleGrant{GrantID: prefix + "requester-grant", SubjectType: "user", SubjectID: actor, RoleID: prefix + "review-role", ScopeType: "project", ScopeID: project, IsEnabled: 1},
		&model.RoleGrant{GrantID: prefix + "reviewer-grant", SubjectType: "team", SubjectID: prefix + "team", RoleID: prefix + "review-role", ScopeType: "project", ScopeID: project, IsEnabled: 1},
	} {
		if err := mysqlAuthCreate(db, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger", "approval:approve", "approval:reject"} {
		parts := strings.SplitN(action, ":", 2)
		for _, value := range []any{&model.Permission{PermissionID: prefix + action, ResourceType: parts[0], Action: parts[1]}, &model.RolePermissionBinding{RoleID: prefix + "review-role", PermissionID: prefix + action}} {
			if err := mysqlAuthCreate(db, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	policy := retryapproval.Policy{ID: "retry", Candidates: []string{voter}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: int64(timeout)}
	policy.Fingerprint, _ = policy.Digest()
	capture := retryapproval.RunContext{Version: 1, ProjectID: project, PipelineID: pipeline, PipelineRunID: prefix + "run", ActorID: actor, DefinitionCommitSHA: strings.Repeat("c", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("d", 64), GatedJobs: []string{"build"}, Policy: policy}
	body, err := capture.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := capture.Digest()
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", prefix+"run").Updates(map[string]any{"retry_approval_context": body, "retry_approval_context_digest": digest, "trigger_type": 1, "triggered_by": actor, "definition_commit_sha": capture.DefinitionCommitSHA, "definition_path": capture.DefinitionPath}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", prefix+"job").Updates(map[string]any{"job_name": "build", "retry_approval_required": true}).Error; err != nil {
		t.Fatal(err)
	}

	// 重建带显式门禁和固定预算的完整计划，而非复用旧无门禁计划摘要
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}, BindingDigest: admit.Identity.RuntimeBindingDigest}
	payload := &taskqueue.JobRunTaskPayload{PipelineID: pipeline, PipelineRunID: prefix + "run", JobRunID: prefix + "job", JobName: "build", AgentID: prefix + "agent", Runtime: runtime, Retry: &pipelinev1.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: policy.TimeoutSeconds, Steps: []taskqueue.StepPayload{{Name: "shell", StepRunID: prefix + "step-1", AttemptRunIDs: []string{prefix + "step-1", prefix + "step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	planBody, _ := plan.Canonical()
	planDigest, _ := plan.Digest(pipeline, prefix+"run", prefix+"job", prefix+"agent", runtime.BindingDigest)
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", prefix+"job").Updates(map[string]any{"retry_plan": planBody, "retry_plan_digest": planDigest}).Error; err != nil {
		t.Fatal(err)
	}
	admit.RetryPlanDigest = planDigest
	admit.RequestDigest, _ = admit.Digest()
	repositories := repo.NewRepositories(generationServiceDatabase{db}, nil)
	services := service.NewServices(generationServiceDatabase{db}, nil, repositories)
	if err := services.ConfigureRetryApprovalPolicy(&policy); err != nil {
		t.Fatal(err)
	}
	if err := services.ConfigureRetryApprovalAuthorization(); err != nil {
		t.Fatal(err)
	}
	jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
	if !expiry.IsZero() {
		manifest = mysqlAuthArtifact(t, db, prefix, project, pipeline, expiry)
	}
	generation, err := jobs.ReconcileJobGeneration(ctx, admit)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := jobs.GetOrRecordJobInputs(ctx, admit.Identity, manifest)
	if err != nil {
		t.Fatal(err)
	}
	pin := pinned.InputManifestDigest
	job, err := repositories.JobRun.GetByJobRunID(ctx, prefix+"job")
	if err != nil {
		t.Fatal(err)
	}
	request := &retryapproval.Request{Version: 1, Operation: "observe", Identity: admit.Identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RetryPlanDigest: admit.RetryPlanDigest, InputManifestDigest: pin}
	return &mysqlAuthFixture{db: db, services: services, request: request, approvalID: job.RetryApprovalID, voter: voter, repos: repositories}
}

func mysqlAuthCreate(db *gorm.DB, value any) error {
	var field, id string
	switch v := value.(type) {
	case *model.User:
		field, id = "user_id", v.UserID
	case *model.Project:
		field, id = "project_id", v.ProjectID
	case *model.Pipeline:
		field, id = "pipeline_id", v.PipelineID
	}
	if field != "" {
		var n int64
		if err := db.Model(value).Where(field+" = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		if n > 1 {
			return fmt.Errorf("ambiguous fixture subject")
		}
	}
	return db.Create(value).Error
}

func mysqlAuthOpen(t *testing.T) *gorm.DB {
	t.Helper()
	required := os.Getenv("ARCENTRA_REQUIRE_RETRY_APPROVAL_MYSQL")
	if required != "" && required != "1" {
		t.Fatal("ARCENTRA_REQUIRE_RETRY_APPROVAL_MYSQL must be empty or 1")
	}
	dsn := os.Getenv("ARCENTRA_RETRY_APPROVAL_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required MySQL authorization acceptance DSN missing")
		}
		t.Skip("set ARCENTRA_RETRY_APPROVAL_MYSQL_DSN for disposable real MySQL authorization acceptance")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid acceptance DSN")
	}
	if !strings.HasPrefix(cfg.DBName, "arcentra_retry_approval_test") {
		t.Fatal("acceptance requires disposable arcentra_retry_approval_test database")
	}
	if cfg.Net != "unix" {
		host, _, e := net.SplitHostPort(cfg.Addr)
		if e != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			t.Fatal("acceptance only permits explicit loopback or unix sockets")
		}
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 15 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	cfg.Params["innodb_lock_wait_timeout"] = "8"
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("acceptance connection failed", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(3)
	t.Cleanup(func() {
		_ = repo.SetRetryApprovalAuthorizer(repo.NewRepositories(generationServiceDatabase{db}, nil), nil)
		_ = pool.Close()
	})
	return db
}

func mysqlAuthDatabase(t *testing.T) (*gorm.DB, *gorm.DB, string) {
	t.Helper()
	db, other := mysqlAuthOpen(t), mysqlAuthOpen(t)
	prefix := fmt.Sprintf("ma%d-", time.Now().UnixNano())
	entities := []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}}
	for _, entity := range entities {
		if db.Migrator().HasTable(entity) {
			var n int64
			if err := db.Model(entity).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("acceptance refuses existing data")
			}
		}
	}
	if err := db.AutoMigrate(entities...); err != nil {
		t.Fatal(err)
	}
	// Cleanup only this fixture's identifiers, including generated approval IDs. Never drop schemas or tables.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		q := db.WithContext(ctx)
		var ids []string
		if err := q.Model(&model.ApprovalRequest{}).Where("retry_job_run_id LIKE ?", prefix+"%").Pluck("approval_id", &ids).Error; err != nil {
			t.Error(err)
			return
		}
		if len(ids) > 0 {
			if err := q.Where("approval_id IN ?", ids).Delete(&model.ApprovalDecision{}).Error; err != nil {
				t.Error(err)
			}
		}
		for _, entry := range []struct {
			entity any
			field  string
		}{
			{&model.ApprovalRequest{}, "retry_job_run_id"}, {&model.JobRunGeneration{}, "job_run_id"}, {&model.ProjectArtifact{}, "artifact_id"}, {&model.StepRun{}, "step_run_id"}, {&model.JobRun{}, "job_run_id"}, {&model.PipelineRun{}, "run_id"}, {&model.Pipeline{}, "pipeline_id"}, {&model.Project{}, "project_id"}, {&model.Agent{}, "agent_id"}, {&model.TeamMember{}, "team_id"}, {&model.Team{}, "team_id"}, {&model.RoleGrant{}, "grant_id"}, {&model.RolePermissionBinding{}, "role_id"}, {&model.Permission{}, "permission_id"}, {&model.Role{}, "role_id"}, {&model.User{}, "user_id"},
		} {
			if err := q.Where(entry.field+" LIKE ?", prefix+"%").Delete(entry.entity).Error; err != nil {
				t.Error("scoped cleanup", err)
			}
		}
	})
	return db, other, prefix
}

func mysqlAuthArtifact(t *testing.T, db *gorm.DB, prefix, project, pipeline string, expiry time.Time) *taskqueue.JobInputManifest {
	t.Helper()
	hash := strings.Repeat("e", 64)
	size := int64(42)
	key := "artifacts/" + prefix + "run/" + prefix + "producer/" + prefix + "producer-step/" + hash + "/result.txt"
	for _, row := range []any{&model.JobRun{Env: "{}", ArtifactReferences: "{}", ArtifactURIs: "{}", JobRunID: prefix + "producer", JobName: "producer", PipelineID: pipeline, PipelineRunID: prefix + "run", Status: model.JobRunStatusSuccess}, &model.StepRun{Args: "{}", Env: "{}", Secrets: "{}", LabelSelector: "{}", RetryPolicy: "null", StepRunID: prefix + "producer-step", JobRunID: prefix + "producer", PipelineID: pipeline, PipelineRunID: prefix + "run", Status: model.JobRunStatusSuccess}, &model.ProjectArtifact{ArtifactID: prefix + "artifact", ProjectID: project, PipelineID: pipeline, PipelineRunID: prefix + "run", JobRunID: prefix + "producer", StepRunID: prefix + "producer-step", Name: "result.txt", StorageID: "store", StorageFingerprint: strings.Repeat("b", 64), StoragePath: key, SHA256: hash, Size: size, ExpiresAt: &expiry}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := taskqueue.NewJobInputManifest("", map[string]string{"producer/result.txt": key}, map[string]taskqueue.ArtifactReference{"producer/result.txt": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: hash, Size: &size, ExpiresAt: expiry.Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", prefix+"job").Update("input_expectation", body).Error; err != nil {
		t.Fatal(err)
	}
	return manifest
}

func (f *mysqlAuthFixture) admit(ctx context.Context) (*retryapproval.Response, error) {
	request := *f.request
	request.Operation = "admit"
	return repo.NewJobRunRepo(generationServiceDatabase{f.db}).(*repo.JobRunRepo).ReconcileJobRetryApproval(ctx, request)
}
func mysqlAuthApprove(t *testing.T, f *mysqlAuthFixture) {
	t.Helper()
	if err := f.services.Approval.Approve(context.Background(), f.approvalID, f.voter, "approved"); err != nil {
		t.Fatal(err)
	}
}
func mysqlAuthAwait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		var me *drivermysql.MySQLError
		if errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205) {
			t.Fatalf("deadlock/lock timeout is not an acceptable authorization denial: %v", err)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("operation exceeded contention budget: %v", err)
		}
		return err
	case <-time.After(12 * time.Second):
		t.Fatal("authorization operation failed to finish within bound")
		return context.DeadlineExceeded
	}
}
func mysqlAuthWaiting(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatal("operation should remain blocked by row lock", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// Query auditing observes the actual locking reads without replacing the live authorizer.
// It also detects errors masked by fail-closed RBAC, so a deadlock cannot pass as a denial.
type mysqlAuthAudit struct {
	mu                 sync.Mutex
	tables             []string
	users              map[string]bool
	catalogBeforeUsers bool
	errs               []error
	expected           []string
}

func mysqlAuthAuditQueries(t *testing.T, db *gorm.DB, expected ...string) *mysqlAuthAudit {
	t.Helper()
	a := &mysqlAuthAudit{users: map[string]bool{}, expected: expected}
	name := "mysql-auth-audit"
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if tx.Error != nil {
			a.errs = append(a.errs, tx.Error)
		}
		if !strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") {
			return
		}
		table := tx.Statement.Table
		a.tables = append(a.tables, table)
		if table == "user" {
			for _, v := range tx.Statement.Vars {
				if s, ok := v.(string); ok {
					a.users[s] = true
				}
			}
		}
		if table == "permission" || table == "role_permission_binding" || table == "role" || table == "role_grant" {
			for _, id := range a.expected {
				if !a.users[id] {
					a.catalogBeforeUsers = true
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return a
}
func (a *mysqlAuthAudit) check(t *testing.T, order bool) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, err := range a.errs {
		var me *drivermysql.MySQLError
		if errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205) {
			t.Fatalf("masked or retried MySQL lock failure: %v", err)
		}
	}
	if !order {
		return
	}
	if a.catalogBeforeUsers {
		t.Fatal("global authorization catalogs locked before all requester/voter users", a.tables)
	}
	pi, pr := -1, -1
	for i, table := range a.tables {
		if table == "pipeline" && pi < 0 {
			pi = i
		}
		if table == "project" && pr < 0 {
			pr = i
		}
	}
	if pi < 0 || pr < 0 || pi > pr {
		t.Fatal("actual live reads must lock Pipeline before Project", a.tables)
	}
	for _, id := range a.expected {
		if !a.users[id] {
			t.Fatal("real authorization never locked participant", id)
		}
	}
}

func TestRetryApprovalMySQLServiceAuthorizationCrossedGraphs(t *testing.T) {
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared-pipeline-%t", shared), func(t *testing.T) {
			db, other, p := mysqlAuthDatabase(t)
			project2, pipeline2 := p+"project-b", p+"pipeline-b"
			if shared {
				project2, pipeline2 = p+"project-a", p+"pipeline-a"
			}
			a := mysqlAuthSeed(t, db, p+"a-", p+"project-a", p+"pipeline-a", p+"alice", p+"bob", 60, time.Now().Add(time.Minute))
			b := mysqlAuthSeed(t, other, p+"b-", project2, pipeline2, p+"bob", p+"alice", 60, time.Time{})
			mysqlAuthApprove(t, a)
			mysqlAuthApprove(t, b)
			aa, bb := mysqlAuthAuditQueries(t, db, p+"alice", p+"bob"), mysqlAuthAuditQueries(t, other, p+"alice", p+"bob")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// Separate pools and independent runs exercise both prior ABBA graphs. No retry loop is allowed.
			start := make(chan struct{})
			done := make(chan error, 2)
			for _, f := range []*mysqlAuthFixture{a, b} {
				go func(f *mysqlAuthFixture) {
					<-start
					r, err := f.admit(ctx)
					if err == nil && (r == nil || !r.ExecutionAllowed || r.Admission == nil) {
						err = fmt.Errorf("admission missing execution receipt")
					}
					done <- err
				}(f)
			}
			close(start)
			for i := 0; i < 2; i++ {
				if err := mysqlAuthAwait(t, done); err != nil {
					t.Fatal(err)
				}
			}
			aa.check(t, true)
			bb.check(t, true)
		})
	}
}

func TestRetryApprovalMySQLServiceCommittedRevocation(t *testing.T) {
	for _, kind := range []string{"role", "grant", "team", "binding"} {
		t.Run(kind, func(t *testing.T) {
			db, other, p := mysqlAuthDatabase(t)
			f := mysqlAuthSeed(t, db, p, p+"project", p+"pipeline", p+"alice", p+"bob", 60, time.Time{})
			mysqlAuthApprove(t, f)
			audit := mysqlAuthAuditQueries(t, db, p+"alice", p+"bob")
			// A writer owns the revocation lock before admission; admission must wait and see its commit.
			tx := other.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var err error
			switch kind {
			case "role":
				err = tx.Model(&model.Role{}).Where("role_id = ?", p+"review-role").Update("is_enabled", 0).Error
			case "grant":
				err = tx.Model(&model.RoleGrant{}).Where("grant_id = ?", p+"reviewer-grant").Update("is_enabled", 0).Error
			case "team":
				err = tx.Model(&model.Team{}).Where("team_id = ?", p+"team").Update("is_enabled", 0).Error
			case "binding":
				err = tx.Where("role_id = ? AND permission_id = ?", p+"review-role", p+"approval:approve").Delete(&model.RolePermissionBinding{}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := f.admit(ctx); done <- err }()
			mysqlAuthWaiting(t, done)
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if err := mysqlAuthAwait(t, done); err == nil {
				t.Fatal("committed revocation admitted execution")
			}
			audit.check(t, false)
			var row model.JobRunGeneration
			if err := db.Where("generation_id = ?", f.request.GenerationID).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.RetryApprovalAdmission != "" {
				t.Fatal("denied admission persisted receipt")
			}
		})
	}
}

func TestRetryApprovalMySQLServiceAdmissionSerializesRevocation(t *testing.T) {
	db, other, p := mysqlAuthDatabase(t)
	f := mysqlAuthSeed(t, db, p, p+"project", p+"pipeline", p+"alice", p+"bob", 60, time.Time{})
	mysqlAuthApprove(t, f)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	// Hold the successful transaction immediately before its durable receipt update, after real RBAC locks.
	name := "mysql-auth-receipt-barrier"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "job_run_generation" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	audit := mysqlAuthAuditQueries(t, db, p+"alice", p+"bob")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done, revoke := make(chan error, 1), make(chan error, 1)
	var receipt string
	go func() {
		r, err := f.admit(ctx)
		if err == nil {
			if r == nil || !r.ExecutionAllowed || r.Admission == nil {
				err = fmt.Errorf("missing admitted receipt")
			} else {
				receipt, err = r.Admission.Canonical()
			}
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("admission never reached receipt barrier")
	}
	go func() {
		revoke <- other.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", p+"reviewer-grant").Update("is_enabled", 0).Error
	}()
	mysqlAuthWaiting(t, revoke)
	close(release)
	if err := mysqlAuthAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if err := mysqlAuthAwait(t, revoke); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admit(ctx); err == nil {
		t.Fatal("next admission ignored committed revocation")
	}
	if err := f.services.Approval.Approve(ctx, f.approvalID, f.voter, "approved"); err != nil {
		t.Fatal("historical vote ACK replay failed", err)
	}
	var votes int64
	if err := db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", f.approvalID).Count(&votes).Error; err != nil || votes != 1 {
		t.Fatal("historical ACK replay changed vote cardinality", votes, err)
	}
	var row model.JobRunGeneration
	if err := db.Where("generation_id = ?", f.request.GenerationID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.RetryApprovalAdmission != receipt {
		t.Fatal("revocation changed historical exact receipt")
	}
	if _, err := retryapproval.ParseAdmission(row.RetryApprovalAdmission); err != nil {
		t.Fatal("historical receipt no longer valid", err)
	}
	audit.check(t, true)
}

func TestRetryApprovalMySQLServiceExpiryAfterLockWait(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		t.Run(fmt.Sprintf("artifact-%t", artifact), func(t *testing.T) {
			db, other, p := mysqlAuthDatabase(t)
			expiry := time.Time{}
			timeout := 4
			if artifact {
				expiry = time.Now().Add(4 * time.Second).Truncate(time.Second)
				timeout = 60
			}
			f := mysqlAuthSeed(t, db, p, p+"project", p+"pipeline", p+"alice", p+"bob", timeout, expiry)
			mysqlAuthApprove(t, f)
			var approval model.ApprovalRequest
			if err := db.Where("approval_id = ?", f.approvalID).First(&approval).Error; err != nil {
				t.Fatal(err)
			}
			if artifact && approval.ExpiresAt.Unix() != expiry.Unix() {
				t.Fatal("earliest input expiry was not frozen into approval deadline")
			}
			audit := mysqlAuthAuditQueries(t, db, p+"alice", p+"bob")
			tx := other.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var permission model.Permission
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("permission_id = ?", p+"approval:approve").First(&permission).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := f.admit(ctx); done <- err }()
			mysqlAuthWaiting(t, done)
			time.Sleep(time.Until(*approval.ExpiresAt) + 50*time.Millisecond)
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if err := mysqlAuthAwait(t, done); err == nil {
				t.Fatal("admission ignored deadline crossed while waiting for real authorization lock")
			}
			audit.check(t, false)
			var row model.JobRunGeneration
			if err := db.Where("generation_id = ?", f.request.GenerationID).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.RetryApprovalAdmission != "" {
				t.Fatal("expiry rollback retained admission")
			}
		})
	}
}

// This smoke test validates canonical fixture construction even when MySQL is opted out.
// Locking and concurrency assertions remain exclusively in the real-MySQL tests above.
func TestRetryApprovalAuthorizationGraphFixtureSmoke(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		t.Run(fmt.Sprintf("artifact-%t", artifact), func(t *testing.T) {
			db := generationTestDB(t, ":memory:", true)
			expiry := time.Time{}
			if artifact {
				expiry = time.Now().Add(time.Minute).Truncate(time.Second)
			}
			f := mysqlAuthSeed(t, db, "smoke-", "smoke-project", "smoke-pipeline", "smoke-alice", "smoke-bob", 60, expiry)
			t.Cleanup(func() { _ = repo.SetRetryApprovalAuthorizer(f.repos, nil) })
			mysqlAuthApprove(t, f)
			mysqlAuthAssertJSONRows(t, db)
			response, err := f.admit(context.Background())
			if err != nil || response == nil || !response.ExecutionAllowed || response.Admission == nil {
				t.Fatal("canonical real-authorizer fixture invalid", response, err)
			}
		})
	}
}

// Validate every JSON column in the real-authorizer fixture under SQLite too,
// so permissive SQLite storage cannot hide another MySQL Error 3140.
func mysqlAuthAssertJSONRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, entity := range []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}} {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(entity); err != nil {
			t.Fatal(err)
		}
		for _, field := range stmt.Schema.Fields {
			if field.TagSettings["TYPE"] != "json" {
				continue
			}
			rows, err := db.Model(entity).Select(field.DBName).Rows()
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var value *string
				if err := rows.Scan(&value); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				if value != nil && !json.Valid([]byte(*value)) {
					_ = rows.Close()
					t.Fatalf("invalid fixture JSON %s.%s: %q", stmt.Table, field.DBName, *value)
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
