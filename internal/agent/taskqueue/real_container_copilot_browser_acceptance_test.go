//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/router"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func containerCopilotBrowserEnabled(getenv func(string) string) (bool, error) {
	enabled, required := getenv("ARCENTRA_COPILOT_EXECUTION_BROWSER"), getenv("ARCENTRA_REQUIRE_COPILOT_EXECUTION_BROWSER")
	if enabled == "" && required == "" {
		return false, nil
	}
	if enabled != "1" || required != "1" {
		return false, errors.New("browser acceptance requires both explicit opt-ins")
	}
	for _, key := range []string{"ARCENTRA_COPILOT_BROWSER_CWD", "ARCENTRA_COPILOT_BROWSER_NODE", "PLAYWRIGHT_BROWSERS_PATH", "ARCENTRA_COPILOT_BROWSER_EVIDENCE"} {
		if !filepath.IsAbs(getenv(key)) {
			return false, fmt.Errorf("required absolute browser fixture path: %s", key)
		}
	}
	return true, nil
}

// This uses the full production router and real Redis-backed login, never a
// hand-built confirmation endpoint, injected browser session, or fake approval.
// Only the whole-job case invokes Chromium; failed-step keeps service/runtime
// coverage. Both consume the existing full-job and daemon deadlines unchanged.
func (s *containerCopilotExecution) confirmBrowser(t *testing.T, f *controllerWorkerFixture, index int, policy *retryapproval.Policy) bool {
	t.Helper()
	enabled, err := containerCopilotBrowserEnabled(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		return false
	}
	cwd, node, output := os.Getenv("ARCENTRA_COPILOT_BROWSER_CWD"), os.Getenv("ARCENTRA_COPILOT_BROWSER_NODE"), os.Getenv("ARCENTRA_COPILOT_BROWSER_EVIDENCE")
	for _, path := range []string{node, filepath.Join(cwd, "tests/actual-copilot-execution-acceptance.mjs"), filepath.Join(cwd, "node_modules/playwright/package.json")} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("mandatory real browser fixture missing: %s", path)
		}
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	redisAddress := os.Getenv("ARCENTRA_TEST_REDIS_ADDRESS")
	realStackLoopback(t, redisAddress, "browser Redis")
	client := redis.NewClient(&redis.Options{Addr: redisAddress, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second})
	defer client.Close()
	if err := client.Ping(f.ctx).Err(); err != nil {
		t.Fatal("mandatory browser Redis is unavailable")
	}
	user := s.subjects[index].UserID
	password := "synthetic-browser-" + id.GetUild()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.User{}).Where("user_id = ?", user).Updates(map[string]any{"username": user, "password": string(hashed)}).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Del(ctx, consts.UserTokenKey+user, consts.UserRefreshTokenKey+user, consts.UserInfoKey+user).Err(); err != nil {
			t.Error(err)
		}
	}()
	cacheStore := cache.NewRedisCache(client)
	repositories := repo.NewRepositories(workerTestDatabase{f.db}, cacheStore)
	services := controlservice.NewServices(workerTestDatabase{f.db}, cacheStore, repositories)
	if err := services.ConfigureRetryApprovalPolicy(policy); err != nil {
		t.Fatal(err)
	}
	// The worker's original Services owns the live authorizer for this sql.DB.
	// This Redis-backed HTTP facade must not replace that closure: later suites
	// switch the owner's policy, and a browser-local closure would retain the old
	// policy after this router shuts down. Repository calls retain the original
	// transaction-local authorization, including current permissions and expiry.
	conf := &config.AppConfig{Copilot: copilot.Config{History: copilot.HistoryConfig{Enabled: true}, Execution: copilot.ExecutionConfig{RetryApprovalEnabled: true}}}
	rt := router.NewRouter(&apphttp.HTTP{ReadTimeout: 10, WriteTimeout: 10, IdleTimeout: 10, Auth: apphttp.Auth{SecretKey: "synthetic-http-" + id.GetUild(), AccessExpire: 5 * time.Minute, RefreshExpire: 5 * time.Minute}}, cacheStore, services, nil, conf)
	app := rt.Router()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- app.Listener(listener) }()
	defer func() {
		if err := app.ShutdownWithTimeout(3 * time.Second); err != nil {
			t.Error(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Error("browser HTTP router did not stop")
		}
	}()
	intent := s.intents[index]
	contract, err := json.Marshal(map[string]any{"version": "copilot-execution-browser-v1", "actorId": user, "projectId": f.projectID, "runId": intent.RunID, "jobRunId": s.jobID, "approvalId": s.approvalID, "intentId": intent.IntentID, "intentDigest": intent.IntentDigest, "intentVersion": intent.Version, "retryMode": "whole_job"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep startup, UI and terminal observation within the existing 60-second
	// approval deadline; no timeout or permission is extended for this journey.
	ctx, cancel := context.WithTimeout(f.ctx, 50*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "tests/actual-copilot-execution-acceptance.mjs")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		// Only this spawned browser's process group; no host/runtime processes.
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 3 * time.Second
	command.Dir = cwd
	command.Env = append(os.Environ(), "ARCENTRA_COPILOT_EXECUTION_CONTRACT="+string(contract), "ARCENTRA_ACCEPTANCE_BACKEND_URL=http://"+listener.Addr().String(), "ARCENTRA_ACCEPTANCE_USERNAME="+user, "ARCENTRA_ACCEPTANCE_PASSWORD="+password, "UI_TEST_OUTPUT_DIR="+output)
	// Do not echo authentication or raw diagnostic output into the Go log.
	if err := command.Run(); err != nil {
		t.Fatalf("mandatory actual browser confirmation failed (%T); inspect bounded browser evidence", err)
	}
	var receipt struct {
		Version          string `json:"version"`
		ActorID          string `json:"actorId"`
		IntentID         string `json:"intentId"`
		DecisionID       string `json:"decisionId"`
		ConfirmPosts     int    `json:"confirmPosts"`
		JobStatus        int    `json:"jobStatus"`
		Generation       int    `json:"generation"`
		RuntimeAdmission bool   `json:"runtimeAdmissionObserved"`
	}
	raw, err := os.ReadFile(filepath.Join(output, "copilot-execution-receipt.json"))
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &receipt) != nil || receipt.Version != "copilot-execution-browser-v1" || receipt.ActorID != user || receipt.IntentID != intent.IntentID || receipt.DecisionID == "" || receipt.ConfirmPosts != 1 || receipt.JobStatus != model.JobRunStatusSuccess || receipt.Generation != 2 || !receipt.RuntimeAdmission {
		t.Fatal("actual browser receipt missing or not bound to successful two-generation runtime")
	}
	var summary map[string]any
	raw, err = os.ReadFile(filepath.Join(output, "summary.json"))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &summary) != nil || summary["kind"] != "actual-backend" || summary["status"] != "passed" {
		t.Fatal("actual browser diagnostics did not pass")
	}
	for _, key := range []string{"pageErrorCount", "secretExposureCount", "consoleInspectionFailureCount", "lateConsoleEventCount", "unexpectedConsoleErrorCount", "blockedRequestCount", "unhandledRouteCount"} {
		if summary[key] != float64(0) {
			t.Fatalf("actual browser safeguard did not pass: %s", key)
		}
	}
	s.decisions[index] = receipt.DecisionID
	// Independent authoritative read, not a browser JSON assertion alone.
	var vote model.ApprovalDecision
	if err := f.db.Where("decision_id = ? AND approval_id = ? AND user_id = ?", receipt.DecisionID, s.approvalID, user).First(&vote).Error; err != nil || vote.Decision != model.ApprovalDecisionApprove {
		t.Fatal("browser confirmation lacks the exact committed candidate vote")
	}
	t.Log("actual browser Copilot whole_job: production UI and Redis-authenticated router, explicit permanent confirmation, durable decision, observed generation 2 success; runtime failure/retry/publication counts checked independently")
	return true
}

func TestContainerCopilotBrowserRequiredGate(t *testing.T) {
	base := map[string]string{"ARCENTRA_COPILOT_EXECUTION_BROWSER": "1", "ARCENTRA_REQUIRE_COPILOT_EXECUTION_BROWSER": "1", "ARCENTRA_COPILOT_BROWSER_CWD": "/tmp/frontend", "ARCENTRA_COPILOT_BROWSER_NODE": "/tmp/node", "PLAYWRIGHT_BROWSERS_PATH": "/tmp/browser", "ARCENTRA_COPILOT_BROWSER_EVIDENCE": "/tmp/evidence"}
	if enabled, err := containerCopilotBrowserEnabled(func(string) string { return "" }); enabled || err != nil {
		t.Fatal("default must remain opt-in")
	}
	if enabled, err := containerCopilotBrowserEnabled(func(k string) string { return base[k] }); !enabled || err != nil {
		t.Fatal("complete gate rejected")
	}
	for key := range base {
		t.Run(strings.ToLower(key), func(t *testing.T) {
			if _, err := containerCopilotBrowserEnabled(func(k string) string {
				if k == key {
					return ""
				}
				return base[k]
			}); err == nil {
				t.Fatal("required browser configuration silently skipped")
			}
		})
	}
}
