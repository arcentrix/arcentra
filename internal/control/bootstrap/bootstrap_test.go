package bootstrap

import (
	"context"
	"errors"
	"io"
	"net"
	nethttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/shutdown"
	"github.com/gofiber/fiber/v2"
)

func testController(t *testing.T, withGRPC bool) *App {
	t.Helper()
	app := &App{
		HTTPApp:     fiber.New(fiber.Config{DisableStartupMessage: true}),
		AppConf:     &config.AppConfig{},
		ShutdownMgr: shutdown.NewManager(),
	}
	app.AppConf.HTTP.Host = "127.0.0.1"
	if withGRPC {
		server, err := grpc.NewGrpcServer(grpc.Conf{Host: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		app.GrpcServer = server
		// 测试包装器使用临时端口，控制器仍把 gRPC 视为必需依赖
		app.AppConf.Grpc.Port = 9090
	}
	app.HTTPApp.Get("/ready", func(c *fiber.Ctx) error {
		if !app.Ready() {
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

type controllerRun struct {
	finished chan struct{}
	cancel   context.CancelFunc
	err      error
	address  string
}

func launchController(t *testing.T, app *App, cleanup func(), timeout time.Duration) *controllerRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &controllerRun{finished: make(chan struct{}), cancel: cancel}
	address := make(chan string, 1)
	app.HTTPApp.Hooks().OnListen(func(data fiber.ListenData) error { address <- net.JoinHostPort(data.Host, data.Port); return nil })
	safe.Go(func() { run.err = runContext(ctx, app, cleanup, timeout); close(run.finished) })
	t.Cleanup(func() { cancel(); run.wait(t) })
	select {
	case run.address = <-address:
	case <-run.finished:
		t.Fatalf("controller did not start: %v", run.err)
	case <-time.After(time.Second):
		t.Fatal("controller did not bind HTTP")
	}
	waitFor(t, app.Ready, "controller did not become ready")
	return run
}

func (r *controllerRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.finished:
		return r.err
	case <-time.After(2 * time.Second):
		t.Fatal("controller failed to exit within test bound")
		return nil
	}
}

func waitFor(t *testing.T, check func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerGRPCBindFailureNeverStartsHTTP(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	app := testController(t, false)
	app.AppConf.Grpc = grpc.Conf{Host: "127.0.0.1", Port: occupied.Addr().(*net.TCPAddr).Port}
	app.GrpcServer, err = grpc.NewGrpcServer(app.AppConf.Grpc)
	if err != nil {
		t.Fatal(err)
	}
	var listened, cleaned atomic.Bool
	app.HTTPApp.Hooks().OnListen(func(fiber.ListenData) error { listened.Store(true); return nil })
	err = runContext(context.Background(), app, func() { cleaned.Store(true) }, time.Second)
	if err == nil || !strings.Contains(err.Error(), "gRPC listen failed") {
		t.Fatalf("bind error lost: %v", err)
	}
	if listened.Load() || app.Ready() || app.GrpcServer.Ready() || !cleaned.Load() {
		t.Fatal("failed gRPC startup exposed HTTP readiness or missed cleanup")
	}
}

func TestControllerHTTPBindFailureStopsGRPC(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	app := testController(t, true)
	app.AppConf.HTTP.Port = occupied.Addr().(*net.TCPAddr).Port
	var cleaned atomic.Bool
	err = runContext(context.Background(), app, func() { cleaned.Store(true) }, time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP listen failed") {
		t.Fatalf("bind error lost: %v", err)
	}
	if app.Ready() || app.GrpcServer.Ready() || !cleaned.Load() {
		t.Fatal("HTTP startup failure left a ready gRPC listener")
	}
	select {
	case <-app.GrpcServer.Done():
	case <-time.After(time.Second):
		t.Fatal("gRPC listener did not exit")
	}
}

func TestControllerRuntimeListenerExitPropagates(t *testing.T) {
	for _, component := range []string{"HTTP", "gRPC"} {
		t.Run(component, func(t *testing.T) {
			app := testController(t, true)
			if app.Ready() {
				t.Fatal("controller ready before startup")
			}
			var cleaned atomic.Bool
			run := launchController(t, app, func() { cleaned.Store(true) }, time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if component == "HTTP" {
				err = app.HTTPApp.ShutdownWithContext(ctx)
			} else {
				err = app.GrpcServer.Stop(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := run.wait(t); err == nil || !strings.Contains(err.Error(), component+" server failed") {
				t.Fatalf("listener exit hidden: %v", err)
			}
			if app.Ready() || app.GrpcServer.Ready() || !cleaned.Load() || !app.ShutdownMgr.IsShuttingDown() {
				t.Fatal("runtime failure left controller healthy or failed cleanup")
			}
		})
	}
}

func TestControllerDrainsRequestsBeforeDependencies(t *testing.T) {
	app := testController(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	app.HTTPApp.Get("/hold", func(c *fiber.Ctx) error { close(entered); <-release; return c.SendStatus(fiber.StatusOK) })
	cleaned := make(chan struct{})
	run := launchController(t, app, func() { close(cleaned) }, time.Second)
	client := &nethttp.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	requestDone := make(chan error, 1)
	safe.Go(func() {
		response, err := client.Get("http://" + run.address + "/hold")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		requestDone <- err
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter")
	}
	run.cancel()
	waitFor(t, func() bool { return !app.Ready() }, "draining controller remained ready")
	select {
	case <-cleaned:
		t.Fatal("dependencies were torn down before active HTTP request drained")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := run.wait(t); err != nil {
		t.Fatalf("normal shutdown failed: %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("dependencies not cleaned after drain")
	}
}

func TestControllerCleanupCannotHangProcess(t *testing.T) {
	app := testController(t, false)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	run := launchController(t, app, func() { close(entered); <-release }, 60*time.Millisecond)
	start := time.Now()
	run.cancel()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	if err := run.wait(t); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup deadline failure not surfaced: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond || app.Ready() {
		t.Fatal("cleanup exceeded deadline or remained ready")
	}
}

func TestControllerMissingGRPCAndCanceledStartupFailClosed(t *testing.T) {
	app := testController(t, false)
	app.AppConf.Grpc.Port = 9090
	if err := runContext(context.Background(), app, nil, time.Second); err == nil {
		t.Fatal("required gRPC dependency missing without failure")
	}
	app = testController(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runContext(ctx, app, nil, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup error lost: %v", err)
	}
	if app.Ready() || app.GrpcServer.Ready() {
		t.Fatal("canceled startup ready")
	}
}
