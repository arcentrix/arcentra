// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/gofiber/fiber/v2"
)

func newProbeTestRouter(manager *shutdown.Manager) *Router {
	return NewRouter(&apphttp.HTTP{}, manager)
}

func probeResponse(t *testing.T, app *fiber.App, method, path string, wantStatus int) string {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %q", method, path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

func TestProbesReflectReadinessLifecycle(t *testing.T) {
	manager := shutdown.NewManager()
	rt := newProbeTestRouter(manager)
	var ready atomic.Bool
	var readinessCalls atomic.Int32
	rt.Readiness = func() bool {
		readinessCalls.Add(1)
		return ready.Load()
	}
	app := rt.Router()
	for _, phase := range []struct {
		name     string
		ready    bool
		shutdown bool
		status   int
		body     string
	}{
		{name: "startup pending", status: http.StatusServiceUnavailable, body: "not ready"},
		{name: "ready", ready: true, status: http.StatusOK, body: "ok"},
		{name: "dependency lost", status: http.StatusServiceUnavailable, body: "not ready"},
		{name: "dependency recovered", ready: true, status: http.StatusOK, body: "ok"},
		{name: "draining", ready: true, shutdown: true, status: http.StatusServiceUnavailable, body: "shutting down"},
	} {
		t.Run(phase.name, func(t *testing.T) {
			ready.Store(phase.ready)
			if phase.shutdown {
				manager.Shutdown()
			}
			before := readinessCalls.Load()
			if body := probeResponse(t, app, http.MethodGet, "/live", http.StatusOK); body != "ok" {
				t.Fatalf("liveness body = %q, want ok", body)
			}
			probeResponse(t, app, http.MethodHead, "/live", http.StatusOK)
			if readinessCalls.Load() != before {
				t.Fatal("liveness consulted readiness dependency")
			}
			for _, path := range []string{"/ready", "/health"} {
				if body := probeResponse(t, app, http.MethodGet, path, phase.status); body != phase.body {
					t.Fatalf("%s body = %q, want %q", path, body, phase.body)
				}
				probeResponse(t, app, http.MethodHead, path, phase.status)
			}
			if phase.shutdown && readinessCalls.Load() != before {
				t.Fatal("draining readiness consulted dependency instead of failing closed")
			}
		})
	}
}

func TestProbesWithoutReadinessFailClosed(t *testing.T) {
	app := newProbeTestRouter(nil).Router()
	probeResponse(t, app, http.MethodGet, "/live", http.StatusOK)
	for _, path := range []string{"/ready", "/health"} {
		probeResponse(t, app, http.MethodGet, path, http.StatusServiceUnavailable)
	}
}

func TestProbesWithoutShutdownManagerUseReadiness(t *testing.T) {
	rt := newProbeTestRouter(nil)
	rt.Readiness = func() bool { return true }
	app := rt.Router()
	for _, path := range []string{"/live", "/ready", "/health"} {
		probeResponse(t, app, http.MethodGet, path, http.StatusOK)
	}
}

func TestPublicManagementEndpointsAreAbsent(t *testing.T) {
	manager := shutdown.NewManager()
	rt := newProbeTestRouter(manager)
	rt.Readiness = func() bool { return true }
	app := rt.Router()
	paths := []string{
		"/shutdown", "/shutdown/", "/SHUTDOWN",
		"/debug/pprof", "/debug/pprof/", "/DEBUG/PPROF/",
		"/debug/pprof/cmdline", "/debug/pprof/profile?seconds=1",
		"/debug/pprof/symbol", "/debug/pprof/trace?seconds=1",
		"/debug/pprof/allocs", "/debug/pprof/block", "/debug/pprof/goroutine",
		"/debug/pprof/heap", "/debug/pprof/mutex", "/debug/pprof/threadcreate",
	}
	for _, path := range paths {
		for _, method := range []string{
			http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
		} {
			t.Run(method+" "+path, func(t *testing.T) {
				probeResponse(t, app, method, path, http.StatusNotFound)
			})
		}
		// CORS must not turn a rejected management request into a successful preflight.
		t.Run("preflight "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, path, nil)
			req.Header.Set("Origin", "http://localhost:5173")
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("preflight %s status = %d, want 404", path, resp.StatusCode)
			}
			if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != "" {
				t.Fatalf("management preflight exposed allowed origin %q", origin)
			}
		})
	}
	if manager.IsShuttingDown() {
		t.Fatal("public request triggered shutdown")
	}
	select {
	case <-manager.Wait():
		t.Fatal("public request queued a shutdown signal")
	default:
	}
	probeResponse(t, app, http.MethodGet, "/ready", http.StatusOK)
	if !manager.Shutdown() {
		t.Fatal("public requests prevented signal-driven shutdown")
	}
	probeResponse(t, app, http.MethodGet, "/ready", http.StatusServiceUnavailable)
	probeResponse(t, app, http.MethodGet, "/live", http.StatusOK)
}
