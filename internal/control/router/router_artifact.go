// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package router

import (
	"bufio"
	"errors"
	"io"
	"mime"
	"strconv"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) artifactRouter(r fiber.Router, authMW, subjectMW fiber.Handler) {
	group := r.Group("/projects/:projectID/artifacts", artifactHTTPStatus, authMW, subjectMW, rt.artifactAccess())
	group.Get("/", rt.listProjectArtifacts)
	group.Get("/:artifactID/download", rt.downloadProjectArtifact)
}

// 旧鉴权中间件使用 HTTP200 错误信封，下载客户端必须收到真实错误状态
func artifactHTTPStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	err := c.Next()
	if err != nil {
		return err
	}
	if c.Response().IsBodyStream() || c.Response().StatusCode() != fiber.StatusOK {
		return nil
	}
	if c.Locals("artifactResponse") != nil {
		return nil
	}
	// 鉴权在 handler 前短路时没有 artifactResponse，不能将错误 JSON 当成文件
	c.Status(fiber.StatusUnauthorized)
	return nil
}

func artifactError(c *fiber.Ctx, status int, message string) error {
	c.Locals("artifactResponse", true)
	c.Status(status)
	return http.Err(c, status, message)
}

func (rt *Router) artifactAccess() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if rt.Services == nil || rt.Services.Artifact == nil || rt.Services.Authorizer == nil {
			return artifactError(c, 503, "artifact service is unavailable")
		}
		if _, ok := middleware.SubjectFromLocals(c); !ok {
			if _, ok := collectionSubject(c); !ok {
				return artifactError(c, 401, "authentication is required")
			}
		}
		scope, err := rt.projectResource(c.UserContext(), strings.TrimSpace(c.Params("projectID")))
		if err != nil {
			return artifactError(c, 404, "project not found")
		}
		c.Locals("artifactScope", scope)
		// 逐流水线授权在读取/下载前执行，避免列表分页泄露无权资源
		return c.Next()
	}
}

func (rt *Router) artifactAllowed(c *fiber.Ctx, pipelineID string) (bool, error) {
	scope, ok := c.Locals("artifactScope").(authz.ResourceRef)
	if !ok {
		return false, errors.New("artifact scope is unavailable")
	}
	scope.Type = "pipeline"
	scope.ID = pipelineID
	return rt.allowed(c, "pipeline:read", scope)
}

func (rt *Router) listProjectArtifacts(c *fiber.Ctx) error {
	page, size := 1, 20
	var err error
	if v := c.Query("page"); v != "" {
		page, err = strconv.Atoi(v)
		if err != nil || page < 1 || page > 1000000 {
			return artifactError(c, 400, "invalid page")
		}
	}
	if v := c.Query("pageSize"); v != "" {
		size, err = strconv.Atoi(v)
		if err != nil || size < 1 || size > 100 {
			return artifactError(c, 400, "pageSize must be between 1 and 100")
		}
	}
	status := c.Query("status")
	if status != "" && status != "available" && status != "expired" && status != "unavailable" {
		return artifactError(c, 400, "invalid artifact status")
	}
	if len(c.Query("name")) > 1024 || len(c.Query("runId")) > 64 {
		return artifactError(c, 400, "invalid artifact filter")
	}
	result, err := rt.Services.Artifact.List(c.UserContext(), c.Params("projectID"), service.ArtifactListQuery{Page: page, PageSize: size, Name: c.Query("name"), RunID: c.Query("runId"), Status: status}, func(pipelineID string) (bool, error) { return rt.artifactAllowed(c, pipelineID) })
	if err != nil {
		return artifactError(c, 503, "unable to list artifacts")
	}
	c.Locals("artifactResponse", true)
	return http.Detail(c, result)
}

func (rt *Router) downloadProjectArtifact(c *fiber.Ctx) error {
	value, err := rt.Services.Artifact.Get(c.UserContext(), c.Params("projectID"), c.Params("artifactID"))
	if errors.Is(err, service.ErrArtifactNotFound) {
		return artifactError(c, 404, "artifact not found")
	}
	if err != nil {
		return artifactError(c, 503, "artifact service is unavailable")
	}
	allowed, err := rt.artifactAllowed(c, value.PipelineID)
	if err != nil {
		return artifactError(c, 503, "unable to authorize artifact")
	}
	if !allowed {
		return artifactError(c, 403, "artifact access denied")
	}
	download, err := rt.Services.Artifact.Open(c.UserContext(), c.Params("projectID"), c.Params("artifactID"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrArtifactNotFound):
			return artifactError(c, 404, "artifact not found")
		case errors.Is(err, service.ErrArtifactExpired):
			return artifactError(c, 410, "artifact has expired")
		default:
			return artifactError(c, 409, "artifact content is unavailable")
		}
	}
	if download.Size < 0 || download.Size > int64(^uint(0)>>1) {
		_ = download.Reader.Close()
		return artifactError(c, 409, "artifact is too large for this server")
	}
	// 一字节预读让惰性远端 SDK 的不存在/权限错误在响应头之前返回
	buffered := bufio.NewReaderSize(download.Reader, 32*1024)
	_, err = buffered.Peek(1)
	if (download.Size > 0 && err != nil) || (download.Size == 0 && !errors.Is(err, io.EOF)) {
		_ = download.Reader.Close()
		return artifactError(c, 409, "artifact content is unavailable")
	}
	c.Set(fiber.HeaderContentType, download.MediaType)
	c.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": download.Name}))
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Set("Content-Security-Policy", "sandbox")
	c.Locals("artifactResponse", true)
	// fasthttp 在发送结束或客户端断开时关闭底层流
	stream := &artifactReadCloser{Reader: buffered, Closer: download.Reader}

	return c.SendStream(stream, int(download.Size))
}

type artifactReadCloser struct {
	io.Reader
	io.Closer
}
