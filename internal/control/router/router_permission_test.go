// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

type scopePipelineRepository struct {
	repo.IPipelineRepository
	pipeline *model.Pipeline
	run      *model.PipelineRun
	err      error
}

func (r *scopePipelineRepository) Get(context.Context, string) (*model.Pipeline, error) {
	return r.pipeline, r.err
}
func (r *scopePipelineRepository) GetRun(context.Context, string) (*model.PipelineRun, error) {
	return r.run, r.err
}

type scopeProjectRepository struct {
	repo.IProjectRepository
	project *model.Project
	err     error
}

func (r *scopeProjectRepository) Get(context.Context, string) (*model.Project, error) {
	return r.project, r.err
}

type scopeApprovalRepository struct {
	repo.IApprovalRepository
	approval *model.ApprovalRequest
}

func (r *scopeApprovalRepository) GetByApprovalID(context.Context, string) (*model.ApprovalRequest, error) {
	return r.approval, nil
}

func TestPermissionFailsClosedWithoutAuthorizer(t *testing.T) {
	for _, rt := range []*Router{{}, {Services: &service.Services{}}} {
		app := fiber.New()
		called := false
		app.Get("/private", rt.permission("project:read", middleware.ResolvePlatformScope()), func(c *fiber.Ctx) error {
			called = true
			return c.SendString("private data")
		})
		response, err := app.Test(httptest.NewRequest("GET", "/private", nil))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if called || strings.Contains(string(body), "private data") {
			t.Fatalf("permission check failed open: %s", body)
		}
	}
}

func TestPipelineScopeRequiresResolvableProject(t *testing.T) {
	for _, services := range []*service.Services{
		nil,
		{},
		{PipelineRepo: &scopePipelineRepository{}},
		{PipelineRepo: &scopePipelineRepository{err: errors.New("database unavailable")}},
		{PipelineRepo: &scopePipelineRepository{pipeline: &model.Pipeline{PipelineID: "pipeline"}}},
		{PipelineRepo: &scopePipelineRepository{pipeline: &model.Pipeline{ProjectID: "project"}}},
		{PipelineRepo: &scopePipelineRepository{pipeline: &model.Pipeline{ProjectID: "project"}}, ProjectRepo: &scopeProjectRepository{}},
	} {
		rt := &Router{Services: services}
		if _, _, err := rt.resolvePipelineScope(context.Background(), "pipeline"); err == nil {
			t.Fatal("unresolved pipeline scope was accepted")
		}
	}
	rt := &Router{Services: &service.Services{
		PipelineRepo: &scopePipelineRepository{pipeline: &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}},
		ProjectRepo:  &scopeProjectRepository{project: &model.Project{ProjectID: "project", OrgID: "organization"}},
	}}
	orgID, projectID, err := rt.resolvePipelineScope(context.Background(), "pipeline")
	if err != nil || orgID != "organization" || projectID != "project" {
		t.Fatalf("unexpected scope: %s/%s, %v", orgID, projectID, err)
	}
}

func TestApprovalScopeRejectsMissingRecords(t *testing.T) {
	for _, approval := range []*model.ApprovalRequest{nil, {}, {PipelineRunID: "run"}} {
		rt := &Router{Services: &service.Services{
			Approval:     service.NewApprovalService(&scopeApprovalRepository{approval: approval}, nil, nil),
			PipelineRepo: &scopePipelineRepository{},
		}}
		if _, _, err := rt.ResolveProjectScopeByApprovalID(context.Background(), "approval"); err == nil {
			t.Fatal("unresolved approval scope was accepted")
		}
	}
}

func TestProjectScopeIncludesOrganization(t *testing.T) {
	rt := &Router{Services: &service.Services{Project: service.NewProjectService(&scopeProjectRepository{project: &model.Project{ProjectID: "project", OrgID: "org"}})}}
	app := fiber.New()
	var scopes []authz.ResourceRef
	app.Get("/project/:projectID", func(c *fiber.Ctx) error {
		scope, err := rt.resolveProjectScope()(c)
		if err != nil {
			return err
		}
		scopes = append(scopes, scope)
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Post("/project", func(c *fiber.Ctx) error {
		scope, err := resolveCreateProjectScope()(c)
		if err != nil {
			return err
		}
		scopes = append(scopes, scope)
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest("GET", "/project/project", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	request := httptest.NewRequest("POST", "/project", strings.NewReader(`{"orgId":"org-create"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err = app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(scopes) != 2 || scopes[0].OrgID != "org" || scopes[0].ProjectID != "project" || scopes[1].OrgID != "org-create" {
		t.Fatalf("incorrect project scopes: %#v", scopes)
	}
}
