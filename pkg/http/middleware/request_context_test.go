// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"context"
	"fmt"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	httpx "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel/trace"
)

type requestTestKey struct{}

// TestRequestContextFallbackAndCompletion 验证默认请求上下文在正常和错误返回后取消
func TestRequestContextFallbackAndCompletion(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("handler_error_%t", failure), func(t *testing.T) {
			app := fiber.New()
			app.Use(RequestMiddleware())
			var captured context.Context
			app.Get("/test", func(c *fiber.Ctx) error {
				captured = c.UserContext()
				if captured.Done() == nil || captured.Err() != nil {
					t.Error("request must start with its own live cancellation signal")
				}
				if _, ok := captured.Deadline(); ok {
					t.Error("fallback unexpectedly adds a deadline")
				}
				if _, pooled := captured.(*fasthttp.RequestCtx); pooled {
					t.Error("request context aliases pooled fasthttp state")
				}
				if failure {
					return fiber.ErrForbidden
				}
				return c.SendStatus(fiber.StatusNoContent)
			})
			resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/test", nil), 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			want := fiber.StatusNoContent
			if failure {
				want = fiber.StatusForbidden
			}
			if resp.StatusCode != want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, want)
			}
			if captured == nil || captured.Err() != context.Canceled {
				t.Fatal("request completion did not cancel its context")
			}
		})
	}
}

// TestRequestContextPreservesParentAndTraceInEitherOrder 验证两种中间件顺序保留上游截止时间和值及链路
func TestRequestContextPreservesParentAndTraceInEitherOrder(t *testing.T) {
	for _, traceFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("trace_first_%t", traceFirst), func(t *testing.T) {
			deadline := time.Now().Add(time.Hour)
			parent, cancel := context.WithDeadline(context.WithValue(context.Background(), requestTestKey{}, "parent-value"), deadline)
			defer cancel()
			span := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
			parent = trace.ContextWithSpanContext(parent, span)
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() })
			if traceFirst {
				app.Use(inject.FiberMiddleware(), RequestMiddleware())
			} else {
				app.Use(RequestMiddleware(), inject.FiberMiddleware())
			}
			var captured context.Context
			app.Get("/test", func(c *fiber.Ctx) error {
				captured = c.UserContext()
				if captured.Value(requestTestKey{}) != "parent-value" {
					t.Error("upstream context value lost")
				}
				if got, ok := captured.Deadline(); !ok || !got.Equal(deadline) {
					t.Error("upstream deadline lost")
				}
				if trace.SpanContextFromContext(captured).TraceID() != span.TraceID() {
					t.Error("upstream trace lost")
				}
				return c.SendStatus(fiber.StatusNoContent)
			})
			resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/test", nil), 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if captured == nil || captured.Err() != context.Canceled {
				t.Fatal("traced request context was not canceled")
			}
			if parent.Err() != nil {
				t.Fatal("completion canceled the upstream parent")
			}
		})
	}
}

// TestRequestContextPropagatesParentCancellation 验证上游取消传递到请求上下文
func TestRequestContextPropagatesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() })
	app.Use(RequestMiddleware(), inject.FiberMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		child, release := context.WithTimeout(c.UserContext(), time.Minute)
		defer release()
		cancel()
		select {
		case <-child.Done():
			if child.Err() != context.Canceled {
				t.Error("nested context did not inherit parent cancellation")
			}
		case <-time.After(time.Second):
			t.Error("parent cancellation did not reach nested request context")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/test", nil), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
}

// TestRequestContextPreservesExpiredDeadline 验证已过期的上游截止时间不会被重置
func TestRequestContextPreservesExpiredDeadline(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() })
	app.Use(RequestMiddleware(), inject.FiberMiddleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		if c.UserContext().Err() != context.DeadlineExceeded {
			t.Error("request middleware detached an expired upstream deadline")
		}
		select {
		case <-c.UserContext().Done():
		default:
			t.Error("expired request context has a live cancellation signal")
		}
		return c.SendStatus(fiber.StatusRequestTimeout)
	})
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/test", nil), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusRequestTimeout {
		t.Fatalf("status=%d, want 408", resp.StatusCode)
	}
}

type contextCheckingCache struct {
	cache.ICache
	value string
	check func(context.Context)
}

func (s contextCheckingCache) Get(ctx context.Context, _ string) *redis.StringCmd {
	s.check(ctx)
	return redis.NewStringResult(s.value, nil)
}

type contextCheckingTeams struct{ check func(context.Context) }

func (s contextCheckingTeams) ListUserTeams(ctx context.Context, _ string) ([]model.TeamMember, error) {
	s.check(ctx)
	return []model.TeamMember{{TeamID: "team-1"}}, nil
}

type contextCheckingScope struct{ check func(context.Context) }

func (s contextCheckingScope) ResolveProjectScopeByPipelineID(ctx context.Context, _ string) (string, string, error) {
	s.check(ctx)
	return "org-1", "project-1", nil
}

func (s contextCheckingScope) ResolveProjectScopeByApprovalID(ctx context.Context, _ string) (string, string, error) {
	s.check(ctx)
	return "org-1", "project-1", nil
}

type contextCheckingAuthorizer struct {
	authz.IAuthorizer
	check func(context.Context)
}

func (a contextCheckingAuthorizer) Check(ctx context.Context, subject authz.Subject, _ string, _ authz.ResourceRef) (bool, error) {
	a.check(ctx)
	return subject.UserID == "user-1" && len(subject.TeamIDs) == 1 && subject.TeamIDs[0] == "team-1", nil
}

// TestAuthorizationChainUsesNestedUserContext 验证鉴权链路的缓存和作用域查询共享嵌套上下文
func TestAuthorizationChainUsesNestedUserContext(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(fmt.Sprintf("approval_scope_%t", approval), func(t *testing.T) {
			token, _, err := jwt.GenToken("user-1", []byte("test-secret"), time.Hour, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := sonic.MarshalString(httpx.TokenInfo{AccessToken: token})
			if err != nil {
				t.Fatal(err)
			}
			var contexts []context.Context
			var expected context.Context
			check := func(ctx context.Context) {
				contexts = append(contexts, ctx)
				if ctx != expected || ctx.Value(requestTestKey{}) != "nested-auth" || ctx.Err() != nil {
					t.Error("auth dependency did not receive the current nested UserContext")
				}
			}
			app := fiber.New()
			app.Use(RequestMiddleware(), inject.FiberMiddleware())
			app.Use(func(c *fiber.Ctx) error {
				ctx, cancel := context.WithTimeout(context.WithValue(c.UserContext(), requestTestKey{}, "nested-auth"), time.Minute)
				defer cancel()
				expected = ctx
				c.SetUserContext(ctx)
				return c.Next()
			})
			app.Use(AuthorizationMiddleware("test-secret", contextCheckingCache{value: encoded, check: check}), SubjectMiddleware(contextCheckingTeams{check: check}))
			lookup := contextCheckingScope{check: check}
			resolver := ResolveFromPathPipelineID("id", lookup)
			if approval {
				resolver = ResolveFromApprovalID("id", lookup)
			}
			app.Get("/secure/:id", RequirePermission(contextCheckingAuthorizer{check: check}, "pipeline:read", resolver), func(c *fiber.Ctx) error {
				check(c.UserContext())
				return c.SendStatus(fiber.StatusNoContent)
			})
			req := httptest.NewRequest(fiber.MethodGet, "/secure/resource-1", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := app.Test(req, 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != fiber.StatusNoContent || len(contexts) != 5 {
				t.Fatalf("status=%d context checks=%d, want 204 and 5", resp.StatusCode, len(contexts))
			}
			for _, ctx := range contexts {
				if ctx.Err() != context.Canceled {
					t.Error("auth dependency context survived request completion")
				}
			}
		})
	}
}

// Exercise the exact reuse boundary rather than depending on an HTTP pool hit:
// old downstream contexts remain readable while the same RequestCtx is reset.
// TestRequestContextConcurrentReuseIsolation 验证并发复用请求对象时上下文仍保持隔离
func TestRequestContextConcurrentReuseIsolation(t *testing.T) {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		id := string(append([]byte(nil), c.Get("X-Request-Id")...))
		c.SetUserContext(context.WithValue(context.Background(), requestTestKey{}, id))
		return c.Next()
	})
	app.Use(RequestMiddleware(), inject.FiberMiddleware())
	type snapshot struct {
		ctx context.Context
		id  string
	}
	retained := make(chan snapshot, 256)
	app.Get("/test", func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		if ctx.Err() != nil {
			t.Error("one request canceled another active request")
		}
		retained <- snapshot{ctx: ctx, id: ctx.Value(requestTestKey{}).(string)}
		return c.SendStatus(fiber.StatusNoContent)
	})
	handler := app.Handler()
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		safe.Go(func() {
			defer workers.Done()
			var raw fasthttp.RequestCtx
			var req fasthttp.Request
			req.SetRequestURI("http://localhost/test")
			raw.Init(&req, nil, nil)
			for iteration := 0; iteration < 64; iteration++ {
				raw.ResetUserValues()
				raw.Response.Reset()
				raw.Request.Header.Set("X-Request-Id", fmt.Sprintf("%d-%d", worker, iteration))
				handler(&raw)
				if raw.Response.StatusCode() != fiber.StatusNoContent {
					t.Error("unexpected response during reuse")
				}
				runtime.Gosched()
			}
			raw.ResetUserValues()
		})
	}
	done := make(chan struct{})
	safe.Go(func() { workers.Wait(); close(retained); close(done) })
	var snapshots []snapshot
	for value := range retained {
		snapshots = append(snapshots, value)
		for _, previous := range snapshots {
			if got := previous.ctx.Value(requestTestKey{}); got != previous.id {
				t.Errorf("retained context value=%v, want %s", got, previous.id)
			}
			// Missing keys traverse the entire parent chain, including trace and
			// cancellation wrappers, and must never reach pooled RequestCtx state.
			if previous.ctx.Value("missing-context-key") != nil {
				t.Error("request-local values leaked into another context")
			}
			_, _ = previous.ctx.Deadline()
			_ = previous.ctx.Err()
			runtime.Gosched()
		}
	}
	<-done
	if len(snapshots) != 256 {
		t.Fatalf("processed %d requests, want 256", len(snapshots))
	}
	for _, value := range snapshots {
		if value.ctx.Err() != context.Canceled {
			t.Error("completed request context was not canceled")
		}
	}
}
