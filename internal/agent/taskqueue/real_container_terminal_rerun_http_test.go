//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/router"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/id"
	"golang.org/x/crypto/bcrypt"
)

type terminalRerunHTTP struct {
	base      string
	client    *http.Client
	passwords map[string]string
	tokens    map[string]string
}

// The same configured Services is used by HTTP, gRPC and the dispatcher. Login
// uses disposable fixture users and the production Redis-backed auth routes.
func newTerminalRerunHTTP(t *testing.T, f *controllerWorkerFixture, services *controlservice.Services, store cache.ICache, conf *controlconfig.AppConfig, actors []string) *terminalRerunHTTP {
	t.Helper()
	// Direct fixture seeding bypasses the production role-management reload.
	// Refresh the existing shared snapshot after seeding, without replacing the
	// runtime owner's authorizer or adding permissions to any actor.
	if services == nil || services.Authorizer == nil {
		t.Fatal("terminal HTTP fixture requires the shared production authorizer")
	}
	if err := services.Authorizer.Reload(f.ctx); err != nil {
		t.Fatal("terminal HTTP fixture authorization reload failed")
	}
	h := &terminalRerunHTTP{client: &http.Client{Timeout: 10 * time.Second}, passwords: map[string]string{}, tokens: map[string]string{}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, actor := range actors {
			if err := store.Del(ctx, consts.UserTokenKey+actor, consts.UserRefreshTokenKey+actor, consts.UserInfoKey+actor).Err(); err != nil {
				t.Error("disposable terminal login cleanup failed")
			}
		}
	})
	for _, actor := range actors {
		password := "synthetic-browser-" + id.GetUild()
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatal(err)
		}
		updated := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", actor).Updates(map[string]any{"username": actor, "password": string(hash)})
		if updated.Error != nil || updated.RowsAffected != 1 {
			t.Fatal("fixture login requires exactly one disposable actor identity")
		}
		h.passwords[actor] = password
	}
	rt := router.NewRouter(&apphttp.HTTP{ReadTimeout: 10, WriteTimeout: 10, IdleTimeout: 10, Auth: apphttp.Auth{SecretKey: "synthetic-http-" + id.GetUild(), AccessExpire: 5 * time.Minute, RefreshExpire: 5 * time.Minute}}, store, services, nil, conf)
	app := rt.Router()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + listener.Addr().String()
	finished := make(chan error, 1)
	go func() { finished <- app.Listener(listener) }()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(3 * time.Second); err != nil {
			t.Error(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Error("terminal rerun HTTP router did not stop")
		}
	})
	for _, actor := range actors {
		h.login(t, f.ctx, actor)
	}
	return h
}

func (h *terminalRerunHTTP) login(t *testing.T, ctx context.Context, actor string) {
	t.Helper()
	var login struct {
		Token struct {
			AccessToken string `json:"accessToken"`
		} `json:"token"`
	}
	h.request(t, ctx, "", "POST", "/users/login", map[string]string{"username": actor, "password": base64.StdEncoding.EncodeToString([]byte(h.passwords[actor]))}, &login)
	if login.Token.AccessToken == "" {
		t.Fatal("real login returned no access token")
	}
	h.tokens[actor] = login.Token.AccessToken
}

func (h *terminalRerunHTTP) request(t *testing.T, ctx context.Context, actor, method, path string, body, out any) {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal("fixture JSON encoding failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("fixture HTTP request construction failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if actor != "" {
		token := h.tokens[actor]
		if token == "" {
			t.Fatal("fixture actor has no authenticated session")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("production fixture HTTP call failed (%T)", err)
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		t.Fatal("unbounded fixture HTTP response")
	}
	detail, diagnostic := terminalRerunHTTPResult(response.StatusCode, raw)
	if diagnostic != "" {
		t.Fatalf("production fixture HTTP %s %s rejected: %s", method, path, diagnostic)
	}
	if out != nil && json.Unmarshal(detail, out) != nil {
		t.Fatal("production fixture HTTP detail malformed")
	}
}

// Decode failed HTTP responses too. Never log raw bodies, arbitrary server
// messages, request headers, passwords or tokens. Only fixed error categories
// and actual decoded numeric status survive into public fixture diagnostics.
func terminalRerunHTTPResult(status int, raw []byte) (json.RawMessage, string) {
	var envelope struct {
		Code   *int            `json:"code"`
		Detail json.RawMessage `json:"detail"`
		Error  json.RawMessage `json:"errMsg"`
	}
	if len(raw) > 1024*1024 || json.Unmarshal(raw, &envelope) != nil {
		return nil, fmt.Sprintf("status=%d envelope=malformed bytes=%d", status, len(raw))
	}
	if status == http.StatusOK && envelope.Code != nil && *envelope.Code == 200 {
		return envelope.Detail, ""
	}
	code := "missing"
	if envelope.Code != nil {
		code = strconv.Itoa(*envelope.Code)
	}
	category := "redacted"
	var message string
	if json.Unmarshal(envelope.Error, &message) == nil {
		switch message {
		case "not eligible to decide this approval":
			category = "approval_forbidden"
		case "approval decision conflicts with current state":
			category = "approval_conflict"
		case "approval has expired":
			category = "approval_expired"
		case "approval not found":
			category = "approval_not_found"
		case "invalid approval request":
			category = "approval_invalid"
		case "approval is currently unavailable":
			category = "approval_unavailable"
		case "access denied":
			category = "access_denied"
		}
	}
	return nil, fmt.Sprintf("status=%d code=%s error=%s bytes=%d", status, code, category, len(raw))
}

// Child ACK/receipt means only this UI phase completed. Authoritative runtime,
// database, original-row equality and object storage checks remain in Go.
func (h *terminalRerunHTTP) browser(t *testing.T, f *controllerWorkerFixture, phase, actor string, contract any, out any) {
	t.Helper()
	enabled, err := containerCopilotBrowserEnabled(os.Getenv)
	if err != nil || !enabled {
		t.Fatal("terminal rerun requires the existing explicit browser opt-ins")
	}
	name, validPhase := terminalRerunBrowserReceiptName(phase)
	if !validPhase {
		t.Fatal("invalid terminal rerun browser phase")
	}
	deadline, ok := f.ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		t.Fatal("original fixture deadline unavailable")
	}
	raw, err := json.Marshal(contract)
	if err != nil || len(raw) > 16384 {
		t.Fatal("terminal rerun browser contract exceeds bound")
	}
	expectedFrontend := os.Getenv("ARCENTRA_COPILOT_BROWSER_FRONTEND_COMMIT")
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(expectedFrontend) {
		t.Fatal("exact trusted frontend commit is required")
	}
	cwd, node := os.Getenv("ARCENTRA_COPILOT_BROWSER_CWD"), os.Getenv("ARCENTRA_COPILOT_BROWSER_NODE")
	entry := filepath.Join(cwd, "tests/actual-rerun-browser.mjs")
	if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
		t.Fatal("reviewed terminal rerun browser entry point is required")
	}
	output := filepath.Join(os.Getenv("ARCENTRA_COPILOT_BROWSER_EVIDENCE"), "terminal-rerun-"+phase)
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal("terminal rerun phase evidence must be a fresh private directory")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, entry)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "ARCENTRA_COPILOT_RERUN_PHASE="+phase, "ARCENTRA_COPILOT_RERUN_CONTRACT="+string(raw), "ARCENTRA_COPILOT_RERUN_DEADLINE_UNIX_MILLI="+strconv.FormatInt(deadline.UnixMilli(), 10), "ARCENTRA_ACCEPTANCE_BACKEND_URL="+h.base, "ARCENTRA_ACCEPTANCE_USERNAME="+actor, "ARCENTRA_ACCEPTANCE_PASSWORD="+h.passwords[actor], "UI_TEST_OUTPUT_DIR="+output)
	if err := cmd.Run(); err != nil {
		t.Fatalf("terminal rerun %s browser failed (%T); inspect private bounded diagnostics", phase, err)
	}
	raw, err = os.ReadFile(filepath.Join(output, "copilot-rerun-"+name+"-receipt.json"))
	if err != nil || len(raw) > 16384 {
		t.Fatal("terminal rerun phase receipt unavailable or oversized")
	}
	var boundary struct {
		RuntimeAcceptanceComplete *bool  `json:"runtimeAcceptanceComplete"`
		FrontendCommit            string `json:"frontendCommit"`
	}
	if json.Unmarshal(raw, &boundary) != nil || boundary.RuntimeAcceptanceComplete == nil || *boundary.RuntimeAcceptanceComplete || boundary.FrontendCommit != expectedFrontend {
		t.Fatal("browser receipt misrepresented runtime acceptance")
	}
	if json.Unmarshal(raw, out) != nil {
		t.Fatal("terminal rerun phase receipt malformed")
	}
	var summary map[string]any
	raw, err = os.ReadFile(filepath.Join(output, "summary.json"))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &summary) != nil || summary["kind"] != "actual-backend" || summary["status"] != "passed" {
		t.Fatal("terminal rerun browser diagnostics failed")
	}
	for _, key := range []string{"pageErrorCount", "secretExposureCount", "consoleInspectionFailureCount", "lateConsoleEventCount", "unexpectedConsoleErrorCount", "blockedRequestCount", "unhandledRouteCount"} {
		if summary[key] != float64(0) {
			t.Fatal(fmt.Sprintf("terminal rerun browser safeguard failed: %s", key))
		}
	}
}

func (h *terminalRerunHTTP) reject(t *testing.T, ctx context.Context, actor, path string, body any, wantStatus int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("negative fixture request malformed")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", h.base+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("negative fixture request construction failed")
	}
	token := h.tokens[actor]
	if token == "" {
		t.Fatal("negative fixture requires a real authenticated actor")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("negative fixture request uncertain (%T)", err)
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 16385))
	var envelope struct {
		Code int `json:"code"`
	}
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &envelope) != nil || response.StatusCode != wantStatus || envelope.Code == 200 {
		t.Fatal("production negative route did not reject exactly")
	}
}
