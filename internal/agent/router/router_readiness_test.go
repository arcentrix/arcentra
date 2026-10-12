package router

import (
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agenthttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/shutdown"
)

func TestHealthRequiresReadinessAndRejectsShutdown(t *testing.T) {
	manager := shutdown.NewManager()
	rt := NewRouter(&agenthttp.HTTP{}, manager)
	var ready atomic.Bool
	rt.Readiness = ready.Load
	app := rt.Router()
	assertStatus := func(want int) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", "/health", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("health status %d want %d", resp.StatusCode, want)
		}
	}
	assertStatus(503)
	ready.Store(true)
	assertStatus(200)
	ready.Store(false)
	assertStatus(503)
	ready.Store(true)
	manager.Shutdown()
	assertStatus(503)
}

func TestHealthWithoutReadinessIsNotReady(t *testing.T) {
	app := NewRouter(&agenthttp.HTTP{}, nil).Router()
	resp, err := app.Test(httptest.NewRequest("GET", "/health", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal("uninitialized agent advertised ready")
	}
}
