// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/trace/inject"
	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func serveRequestContextHTTP(t *testing.T, app *fiber.App) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	safe.Go(func() { done <- app.Listener(listener) })
	transport := &http.Transport{MaxIdleConnsPerHost: 1}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		if err := app.ShutdownWithTimeout(3 * time.Second); err != nil {
			t.Errorf("shut down Fiber HTTP server: %v", err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("serve Fiber HTTP: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("Fiber HTTP server did not stop")
		}
	})
	return "http://" + listener.Addr().String(), client
}

func awaitRequestContextCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("request context remained live after response cleanup")
	}
}

type requestContextReader struct {
	ctx    context.Context
	reader io.Reader
	closed atomic.Int32
}

func (r *requestContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (r *requestContextReader) Close() error {
	r.closed.Add(1)
	return nil
}

// TestRequestContextHTTPStreamLivesPastHandler 验证真正 HTTP 发送阶段保留上下文并在发送后取消
func TestRequestContextHTTPStreamLivesPastHandler(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked_%t", chunked), func(t *testing.T) {
			deadline := time.Now().Add(time.Minute)
			parent, cancel := context.WithDeadline(context.WithValue(context.Background(), requestTestKey{}, "upstream"), deadline)
			defer cancel()
			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() }, RequestMiddleware())
			payload := strings.Repeat("artifact-content-", 8192)
			readers := make(chan *requestContextReader, 1)
			app.Get("/artifact", func(c *fiber.Ctx) error {
				r := &requestContextReader{ctx: c.UserContext(), reader: strings.NewReader(payload)}
				readers <- r
				if r.ctx.Value(requestTestKey{}) != "upstream" {
					t.Error("stream lost upstream context value")
				}
				if got, ok := r.ctx.Deadline(); !ok || !got.Equal(deadline) {
					t.Error("stream lost upstream deadline")
				}
				if chunked {
					return c.SendStream(r)
				}
				return c.SendStream(r, len(payload))
			})
			baseURL, client := serveRequestContextHTTP(t, app)
			resp, err := client.Get(baseURL + "/artifact")
			if err != nil {
				t.Fatalf("HTTP download failed before headers: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != payload {
				t.Fatalf("HTTP stream truncated after handler return: bytes=%d want=%d error=%v", len(body), len(payload), err)
			}
			r := <-readers
			awaitRequestContextCanceled(t, r.ctx)
			if r.closed.Load() != 1 || parent.Err() != nil {
				t.Fatalf("close count=%d parent error=%v", r.closed.Load(), parent.Err())
			}
		})
	}
}

// TestRequestContextHTTPStreamWriterLivesPastHandler 验证流式写入器及 SSE 不在 handler 返回时被取消
func TestRequestContextHTTPStreamWriterLivesPastHandler(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	returned := make(chan struct{})
	app.Use(func(c *fiber.Ctx) error { err := c.Next(); close(returned); return err }, RequestMiddleware())
	captured := make(chan context.Context, 1)
	writerDone := make(chan struct{})
	const event = "event: message\ndata: still-alive\n\n"
	app.Get("/events", func(c *fiber.Ctx) error {
		ctx := c.UserContext() // Never capture pooled Fiber or fasthttp state in the writer.
		captured <- ctx
		c.Set(fiber.HeaderContentType, "text/event-stream")
		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			defer close(writerDone)
			<-returned
			if ctx.Err() != nil {
				return
			}
			if _, err := io.WriteString(w, event); err != nil {
				return
			}
			_ = w.Flush()
		})
		return nil
	})
	baseURL, client := serveRequestContextHTTP(t, app)
	resp, err := client.Get(baseURL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != event {
		t.Fatalf("SSE ended at handler return: body=%q error=%v", body, err)
	}
	awaitRequestContextCanceled(t, <-captured)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("stream writer leaked")
	}
}

// TestRequestContextHTTPStreamCleanup 验证 HEAD、提前关闭、替换和错误响应释放底层流与上下文
func TestRequestContextHTTPStreamCleanup(t *testing.T) {
	for _, mode := range []string{"success", "head", "early_close", "replace", "handler_error", "read_error"} {
		t.Run(mode, func(t *testing.T) {
			app := fiber.New(fiber.Config{DisableStartupMessage: true, WriteTimeout: time.Hour})
			app.Use(RequestMiddleware())
			capturedLifetime := make(chan *requestContextLifetime, 1)
			captured := make(chan *requestContextReader, 1)
			app.Get("/stream", func(c *fiber.Ctx) error {
				var source io.Reader = strings.NewReader(strings.Repeat("x", 64*1024))
				if mode == "read_error" {
					source = io.MultiReader(source, requestErrorReader{})
				}
				r := &requestContextReader{ctx: c.UserContext(), reader: source}
				captured <- r
				capturedLifetime <- c.Locals(requestContextLifetimeKey{}).(*requestContextLifetime)
				size := 128 * 1024
				if mode == "success" {
					size = 64 * 1024
				}
				if err := c.SendStream(r, size); err != nil {
					return err
				}
				switch mode {
				case "early_close":
					return c.Response().CloseBodyStream()
				case "replace":
					return c.SendString("replacement")
				case "handler_error":
					return fiber.ErrBadGateway
				}
				return nil
			})
			baseURL, client := serveRequestContextHTTP(t, app)
			method := http.MethodGet
			if mode == "head" {
				method = http.MethodHead
			}
			req, err := http.NewRequest(method, baseURL+"/stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if mode == "success" && (readErr != nil || len(body) != 64*1024) {
				t.Fatalf("successful stream bytes=%d error=%v", len(body), readErr)
			}
			if mode == "read_error" && readErr == nil {
				t.Fatal("upstream failure silently became complete response")
			}
			if mode == "replace" && (readErr != nil || string(body) != "replacement") {
				t.Fatalf("replacement=%q error=%v", body, readErr)
			}
			if mode == "handler_error" && resp.StatusCode != fiber.StatusBadGateway {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			r := <-captured
			awaitRequestContextCanceled(t, r.ctx)
			lifetime := <-capturedLifetime
			if lifetime.streamTimer != nil && lifetime.streamTimer.Stop() {
				t.Error("response cleanup left its long-lived timer running")
			}
			if mode == "success" && lifetime.streamTimer == nil {
				t.Error("positive write timeout did not install a stream timer")
			}
			if r.closed.Load() != 1 {
				t.Fatalf("upstream close count=%d, want 1", r.closed.Load())
			}
		})
	}
}

type requestErrorReader struct{}

func (requestErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type requestBlockedReader struct {
	ctx    context.Context
	sent   int
	closed atomic.Int32
}

func (r *requestBlockedReader) Read(p []byte) (int, error) {
	if r.sent < 64*1024 {
		n := min(len(p), 64*1024-r.sent)
		for i := range p[:n] {
			p[i] = 'x'
		}
		r.sent += n
		return n, nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *requestBlockedReader) Close() error { r.closed.Add(1); return nil }

// TestRequestContextHTTPStreamParentCancellation 验证 handler 返回后上游取消仍中断正在读取的流
func TestRequestContextHTTPStreamParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() }, RequestMiddleware())
	captured := make(chan *requestBlockedReader, 1)
	app.Get("/stream", func(c *fiber.Ctx) error {
		r := &requestBlockedReader{ctx: c.UserContext()}
		captured <- r
		return c.SendStream(r, 128*1024)
	})
	baseURL, client := serveRequestContextHTTP(t, app)
	resp, err := client.Get(baseURL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 4096)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("stream canceled before parent cancellation: %v", err)
	}
	r := <-captured
	select {
	case <-r.ctx.Done():
		t.Fatalf("zero WriteTimeout unexpectedly canceled a stalled stream: %v", r.ctx.Err())
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("canceled upstream unexpectedly completed full body")
	}
	awaitRequestContextCanceled(t, r.ctx)
	deadline := time.After(time.Second)
	for r.closed.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("canceled stream was not closed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if r.closed.Load() != 1 {
		t.Fatalf("close count=%d", r.closed.Load())
	}
}

type requestEndlessReader struct {
	ctx    context.Context
	closed atomic.Int32
}

func (r *requestEndlessReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
func (r *requestEndlessReader) Close() error { r.closed.Add(1); return nil }

// TestRequestContextHTTPClientAbortCleanup 验证实际 TCP 断开在写入报错后取消上下文且关闭流
func TestRequestContextHTTPClientAbortCleanup(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, WriteTimeout: 30 * time.Second})
	app.Use(RequestMiddleware())
	captured := make(chan *requestEndlessReader, 1)
	app.Get("/stream", func(c *fiber.Ctx) error {
		r := &requestEndlessReader{ctx: c.UserContext()}
		captured <- r
		return c.SendStream(r)
	})
	baseURL, _ := serveRequestContextHTTP(t, app)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(baseURL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatalf("stream stopped before client abort: %v", err)
	}
	r := <-captured
	if r.ctx.Err() != nil {
		t.Fatalf("request canceled before client abort: %v", r.ctx.Err())
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
	awaitRequestContextCanceled(t, r.ctx)
	if r.closed.Load() != 1 {
		t.Fatalf("aborted stream close count=%d", r.closed.Load())
	}
}

type requestContextDatabaseRow struct {
	ID    uint `gorm:"primaryKey"`
	Value string
}

type requestStreamReturnedKey struct{}

// TestRequestContextHTTPDatabaseStreamAndReuse 验证异步 SQL 结果读取及 HTTP 请求复用不会引用池化上下文
func TestRequestContextHTTPDatabaseStreamAndReuse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stream-context.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&requestContextDatabaseRow{}); err != nil {
		t.Fatal(err)
	}
	const payload = "SQL rows read after the Fiber handler returns"
	if err := db.Create(&requestContextDatabaseRow{Value: payload}).Error; err != nil {
		t.Fatal(err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		id := strings.Clone(c.Get("X-Request-Id"))
		c.SetUserContext(context.WithValue(context.Background(), requestTestKey{}, id))
		err := c.Next()
		if returned, ok := c.Locals(requestStreamReturnedKey{}).(chan struct{}); ok {
			close(returned)
		}
		return err
	}, RequestMiddleware(), inject.FiberMiddleware())
	type snapshot struct {
		ctx context.Context
		id  string
	}
	retained := make(chan snapshot, 64)
	stop := make(chan struct{})
	observerDone := make(chan struct{})
	safe.Go(func() {
		defer close(observerDone)
		var contexts []snapshot
		for {
			select {
			case <-stop:
				return
			case value := <-retained:
				contexts = append(contexts, value)
			default:
				for _, value := range contexts {
					if got := value.ctx.Value(requestTestKey{}); got != value.id {
						t.Errorf("retained DB context value=%v, want %s", got, value.id)
					}
					if value.ctx.Value("missing-context-key") != nil {
						t.Error("retained DB context leaked another request's values")
					}
					_, _ = value.ctx.Deadline()
					_ = value.ctx.Err()
				}
				runtime.Gosched()
			}
		}
	})
	defer func() { close(stop); <-observerDone }()
	completed := make(chan context.Context, 64)
	app.Get("/rows", func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		rows, err := db.WithContext(ctx).Model(&requestContextDatabaseRow{}).Select("value").Rows()
		if err != nil {
			return err
		}
		returned := make(chan struct{})
		c.Locals(requestStreamReturnedKey{}, returned)
		retained <- snapshot{ctx: ctx, id: ctx.Value(requestTestKey{}).(string)}
		completed <- ctx
		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			defer rows.Close()
			<-returned
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					t.Errorf("scan rows after handler return: %v", err)
					return
				}
				if _, err := io.WriteString(w, value); err != nil {
					return
				}
			}
			if err := rows.Err(); err != nil {
				t.Errorf("database rows were canceled before stream completion: %v", err)
				return
			}
			_ = w.Flush()
		})
		return nil
	})
	baseURL, client := serveRequestContextHTTP(t, app)
	for iteration := 0; iteration < 64; iteration++ {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/rows", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Request-Id", fmt.Sprintf("request-%d", iteration))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil || string(body) != payload {
			t.Fatalf("request %d: body=%q error=%v", iteration, body, readErr)
		}
		awaitRequestContextCanceled(t, <-completed)
	}
	if got := sqlDB.Stats().InUse; got != 0 {
		t.Fatalf("streaming leaked %d SQL connections", got)
	}
}

// TestRequestContextHTTPWriteBudgetStartsAfterHandler 验证慢 handler 不消耗流式写入预算且阻塞读取遵守该预算
func TestRequestContextHTTPWriteBudgetStartsAfterHandler(t *testing.T) {
	const writeTimeout = 100 * time.Millisecond
	app := fiber.New(fiber.Config{DisableStartupMessage: true, WriteTimeout: writeTimeout})
	app.Use(RequestMiddleware())
	entered := make(chan *requestBlockedReader, 1)
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }
	defer release()
	app.Get("/stream", func(c *fiber.Ctx) error {
		r := &requestBlockedReader{ctx: c.UserContext()}
		entered <- r
		<-releaseHandler
		return c.SendStream(r, 128*1024)
	})
	baseURL, client := serveRequestContextHTTP(t, app)
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	safe.Go(func() { response, err := client.Get(baseURL + "/stream"); resultCh <- result{response, err} })
	var r *requestBlockedReader
	select {
	case r = <-entered:
	case out := <-resultCh:
		if out.response != nil {
			_ = out.response.Body.Close()
		}
		t.Fatalf("request ended before handler entered: %v", out.err)
	case <-time.After(2 * time.Second):
		t.Fatal("request never entered handler")
	}
	select {
	case <-r.ctx.Done():
		t.Fatal("stream write budget canceled the still-running handler")
	case <-time.After(2 * writeTimeout):
	}
	release()
	var response *http.Response
	select {
	case out := <-resultCh:
		if out.err != nil {
			t.Fatal(out.err)
		}
		response = out.response
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return its streaming response")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err == nil || len(body) == 0 || len(body) >= 128*1024 {
		t.Fatalf("bounded blocked stream bytes=%d error=%v", len(body), err)
	}
	awaitRequestContextCanceled(t, r.ctx)
	deadline := time.After(time.Second)
	for r.closed.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed-out stream was not closed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if r.closed.Load() != 1 {
		t.Fatalf("timed-out stream close count=%d", r.closed.Load())
	}
}

// TestRequestContextHTTPStreamParentDeadline 验证更早的上游截止时间优先于写入预算
func TestRequestContextHTTPStreamParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	app := fiber.New(fiber.Config{DisableStartupMessage: true, WriteTimeout: time.Hour})
	app.Use(func(c *fiber.Ctx) error { c.SetUserContext(parent); return c.Next() }, RequestMiddleware())
	captured := make(chan *requestBlockedReader, 1)
	app.Get("/stream", func(c *fiber.Ctx) error {
		r := &requestBlockedReader{ctx: c.UserContext()}
		captured <- r
		return c.SendStream(r, 128*1024)
	})
	baseURL, client := serveRequestContextHTTP(t, app)
	resp, err := client.Get(baseURL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	r := <-captured
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("expired upstream unexpectedly completed stream")
	}
	awaitRequestContextCanceled(t, r.ctx)
	if r.ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("upstream deadline cause changed to %v", r.ctx.Err())
	}
}

// TestRequestContextHTTPStreamWriterClientAbort 验证真实断开使流式写入器退出而非等待写入计时器
func TestRequestContextHTTPStreamWriterClientAbort(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, WriteTimeout: 30 * time.Second})
	app.Use(RequestMiddleware())
	captured := make(chan context.Context, 1)
	writerDone := make(chan struct{})
	app.Get("/events", func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		captured <- ctx
		c.Set(fiber.HeaderContentType, "text/event-stream")
		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			defer close(writerDone)
			event := "data: " + strings.Repeat("x", 32*1024) + "\n\n"
			for ctx.Err() == nil {
				if _, err := io.WriteString(w, event); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		})
		return nil
	})
	baseURL, _ := serveRequestContextHTTP(t, app)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(baseURL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "GET /events HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	ctx := <-captured
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
	awaitRequestContextCanceled(t, ctx)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("disconnected stream writer leaked")
	}
}
