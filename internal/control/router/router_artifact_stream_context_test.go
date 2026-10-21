// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

// TestProjectArtifactRemoteHTTPStreamOwnsContext 验证真实远端下载在处理器返回后保持上下文并在流关闭后取消
func TestProjectArtifactRemoteHTTPStreamOwnsContext(t *testing.T) {
	testProjectArtifactRemoteHTTPStream(t, false)
}

// TestProjectArtifactRemoteHTTPStreamWriteTimeout 验证写入超时取消真实 S3 阻塞读取而无需释放远端数据
func TestProjectArtifactRemoteHTTPStreamWriteTimeout(t *testing.T) {
	testProjectArtifactRemoteHTTPStream(t, true)
}

func testProjectArtifactRemoteHTTPStream(t *testing.T, stall bool) {
	t.Helper()
	const (
		prefixSize = 32 * 1024
		bucket     = "artifact-stream-test"
		basePath   = "managed"
	)
	testCtx, testCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer testCancel()
	payload := make([]byte, 1024*1024+123)
	for i := range payload {
		payload[i] = byte(i*31 + i/251)
	}
	digest := sha256.Sum256(payload)
	objectKey, err := artifactutil.RemotePath("remote-run", "remote-job", "remote-step", hex.EncodeToString(digest[:]), "dist/result.bin")
	if err != nil {
		t.Fatal(err)
	}

	// The first read in downloadProjectArtifact may prefetch 32 KiB. Never
	// release the tail until the middleware has returned and the test has
	// inspected its owned context. A fully buffered remote response would hide
	// the original premature-cancellation bug.
	releaseTail := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseTail) }) }
	defer release()
	prefixSent := make(chan struct{})
	var prefixOnce sync.Once
	upstreamResult := make(chan error, 1)
	reportUpstream := func(err error) {
		// Retain the first result without blocking cleanup if a future SDK
		// version retries. The request-count assertion rejects any retry.
		select {
		case upstreamResult <- err:
		default:
		}
	}
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/"+bucket+"/"+basePath+"/"+objectKey {
			http.Error(w, "unexpected object request", http.StatusNotFound)
			reportUpstream(fmt.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		if _, err := w.Write(payload[:prefixSize]); err != nil {
			reportUpstream(fmt.Errorf("write upstream prefix: %w", err))
			return
		}
		w.(http.Flusher).Flush()
		prefixOnce.Do(func() { close(prefixSent) })
		select {
		case <-releaseTail:
		case <-r.Context().Done():
			reportUpstream(fmt.Errorf("upstream canceled before tail release: %w", r.Context().Err()))
			return
		case <-testCtx.Done():
			reportUpstream(testCtx.Err())
			return
		}
		_, err := w.Write(payload[prefixSize:])
		reportUpstream(err)
	}))
	t.Cleanup(func() {
		release()
		testCancel()
		upstream.CloseClientConnections()
		upstream.Close()
	})

	// Reuse the real migrated repositories, ownership chain, and scoped RBAC.
	// Only authentication token parsing is the existing router-fixture shim.
	unusedApp, rt := collectionRouterFixture(t)
	defer func() { _ = unusedApp.Shutdown() }()
	db := rt.Services.ProjectRepo.(*repo.ProjectRepo).Database()
	for _, value := range []any{
		&model.PipelineRun{RunID: "remote-run", PipelineID: "pipe-a"},
		&model.JobRun{JobRunID: "remote-job", PipelineRunID: "remote-run", PipelineID: "pipe-a"},
		&model.StepRun{StepRunID: "remote-step", JobRunID: "remote-job", PipelineRunID: "remote-run", PipelineID: "pipe-a"},
	} {
		if err := db.WithContext(testCtx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	config := model.StorageConfigDetail{
		Endpoint: upstream.URL, Bucket: bucket, Region: "us-east-1",
		BasePath: basePath, AccessKey: "test-access-key", SecretKey: "test-secret-key",
	}
	configJSON, err := sonic.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	storageRepo := repo.NewStorageRepo(collectionTestDB{db}, nil)
	if err := storageRepo.Create(testCtx, &model.StorageConfig{
		StorageID: "remote-stream-storage", Name: "HTTP regression fixture",
		StorageType: "s3", Config: configJSON, IsEnabled: 1,
	}); err != nil {
		t.Fatal(err)
	}
	artifacts := repo.NewArtifactRepo(collectionTestDB{db})
	record := &model.ProjectArtifact{
		ProjectID: "a", PipelineID: "pipe-a", PipelineRunID: "remote-run", JobRunID: "remote-job", StepRunID: "remote-step",
		Name: "dist/result.bin", StorageType: "s3", StorageID: "remote-stream-storage",
		StorageFingerprint: artifactutil.StorageFingerprint("s3", config.Endpoint, config.Bucket, config.Region, config.BasePath, config.UseTLS),
		StoragePath:        objectKey, Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), MediaType: "application/octet-stream",
	}
	if err := artifacts.Upsert(testCtx, record); err != nil {
		t.Fatal(err)
	}
	// This constructor uses storage.NewStorage, the real S3 SDK, and its real
	// net/http response body. No storage or context-aware reader is substituted.
	rt.Services.Artifact = service.NewArtifactService(artifacts, storageRepo, t.TempDir())

	type contextAtHandlerReturn struct {
		ctx context.Context
		err error
	}
	handlerReturned := make(chan contextAtHandlerReturn, 1)
	writeTimeout := 10 * time.Second
	if stall {
		writeTimeout = 150 * time.Millisecond
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, ReadTimeout: 10 * time.Second, WriteTimeout: writeTimeout})
	app.Use(func(c *fiber.Ctx) error {
		err := c.Next()
		if c.Response().IsBodyStream() {
			ctx := c.UserContext()
			handlerReturned <- contextAtHandlerReturn{ctx: ctx, err: ctx.Err()}
		}
		return err
	})
	app.Use(middleware.RequestMiddleware(), middleware.UnifiedResponseMiddleware())
	authMW := func(c *fiber.Ctx) error {
		token := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		claims, err := jwt.ParseToken(token, "test-secret")
		if err != nil {
			return apphttp.Err(c, apphttp.Unauthorized.Code, apphttp.Unauthorized.Msg)
		}
		c.Locals("claims", claims)
		return c.Next()
	}
	rt.artifactRouter(app, authMW, middleware.SubjectMiddleware(nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverStopped := make(chan error, 1)
	safe.Go(func() { serverStopped <- app.Listener(listener) })
	t.Cleanup(func() {
		release()
		testCancel()
		if err := app.ShutdownWithTimeout(3 * time.Second); err != nil {
			t.Errorf("shut down Fiber listener: %v", err)
		}
		_ = listener.Close()
		select {
		case err := <-serverStopped:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("Fiber listener: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Fiber listener did not stop")
		}
	})
	transport := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	downloadURL := "http://" + listener.Addr().String() + "/projects/a/artifacts/" + record.ArtifactID + "/download"
	request := func(user string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(testCtx, http.MethodGet, downloadURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			token, _, err := jwt.GenToken(user, []byte("test-secret"), time.Hour, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("download via real Fiber HTTP listener: %v", err)
		}
		return response
	}
	for _, denied := range []struct {
		user string
		code int
	}{{"", http.StatusUnauthorized}, {"outsider", http.StatusForbidden}} {
		response := request(denied.user)
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != denied.code {
			t.Fatalf("user %q status = %d, want %d", denied.user, response.StatusCode, denied.code)
		}
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("unauthorized requests reached remote storage %d times", got)
	}

	response := request("project-user")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != int64(len(payload)) {
		t.Fatalf("download headers: status = %d, length = %d", response.StatusCode, response.ContentLength)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" ||
		response.Header.Get("Content-Disposition") != "attachment; filename=result.bin" {
		t.Fatalf("download lost binary attachment headers: %v", response.Header)
	}
	var owned context.Context
	select {
	case returned := <-handlerReturned:
		owned = returned.ctx
		if returned.err != nil {
			t.Errorf("request context canceled when handler returned, before remote streaming finished: %v", returned.err)
		}
	case <-testCtx.Done():
		t.Fatal("artifact handler did not return while remote tail was paused")
	}
	select {
	case <-prefixSent:
	default:
		t.Fatal("artifact response started before the remote prefix")
	}
	if owned.Done() == nil {
		t.Error("stream did not retain a cancellable owned request context")
	}
	if stall {
		// Keep the upstream tail paused throughout these assertions. Waiting
		// for cancellation before reading the body excludes client-close or
		// client-timeout cleanup as the reason that the SDK read unblocked.
		cancelWait, stopWait := context.WithTimeout(testCtx, 3*time.Second)
		defer stopWait()
		select {
		case <-owned.Done():
			if !errors.Is(owned.Err(), context.Canceled) {
				t.Errorf("write timeout context error = %v, want context.Canceled", owned.Err())
			}
		case <-cancelWait.Done():
			t.Fatal("WriteTimeout did not cancel the request context while the S3 response body was stalled")
		}
		select {
		case err := <-upstreamResult:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("stalled upstream request error = %v, want context.Canceled", err)
			}
		case <-cancelWait.Done():
			t.Fatal("WriteTimeout did not interrupt the real S3 HTTP request before tail release")
		}
		select {
		case <-releaseTail:
			t.Error("remote tail was released before cancellation was established")
		default:
		}
		if err := testCtx.Err(); err != nil {
			t.Fatalf("test deadline, rather than WriteTimeout, ended the stalled request: %v", err)
		}
		body, readErr := io.ReadAll(response.Body)
		if readErr == nil || len(body) > prefixSize || !bytes.Equal(body, payload[:len(body)]) {
			t.Errorf("stalled stream should terminate with only the original prefix: bytes=%d, error=%v", len(body), readErr)
		}
		if got := upstreamRequests.Load(); got != 1 {
			t.Errorf("remote object requests = %d, want exactly one", got)
		}
		return
	}
	if err := owned.Err(); err != nil {
		t.Errorf("request context canceled before remote tail release: %v", err)
	}
	release()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Errorf("read remote artifact: got %d/%d bytes: %v", len(body), len(payload), readErr)
	}
	if got := sha256.Sum256(body); got != digest || !bytes.Equal(body, payload) {
		t.Errorf("remote artifact truncated or altered: got %d/%d bytes, SHA-256 %x, want %x", len(body), len(payload), got, digest)
	}
	select {
	case err := <-upstreamResult:
		if err != nil {
			t.Errorf("remote object stream failed: %v", err)
		}
	case <-testCtx.Done():
		t.Error("remote object stream did not finish")
	}
	select {
	case <-owned.Done():
		if !errors.Is(owned.Err(), context.Canceled) {
			t.Errorf("stream completion context error = %v, want context.Canceled", owned.Err())
		}
	case <-testCtx.Done():
		t.Error("completed artifact stream leaked its request context")
	}
	if got := upstreamRequests.Load(); got != 1 {
		t.Errorf("remote object requests = %d, want exactly one", got)
	}
}
