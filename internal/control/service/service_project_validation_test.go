// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
)

func TestProjectValidation(t *testing.T) {
	base := model.CreateProjectReq{OrgID: "org", Name: "project", RepoURL: "https://example.com/team/repo.git", RepoType: model.RepoTypeGit}
	if err := validateCreateProject(&base, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.CreateProjectReq){
		func(req *model.CreateProjectReq) { req.Name = " " },
		func(req *model.CreateProjectReq) { req.OrgID = "" },
		func(req *model.CreateProjectReq) { req.RepoURL = "https://token@example.com/repo.git" },
		func(req *model.CreateProjectReq) { req.RepoURL = "file:///etc/passwd" },
		func(req *model.CreateProjectReq) { req.AccessLevel = "everyone" },
		func(req *model.CreateProjectReq) { req.Visibility = 3 },
		func(req *model.CreateProjectReq) { req.AuthType = -1 },
		func(req *model.CreateProjectReq) { req.TriggerMode = 128 },
	} {
		req := base
		mutate(&req)
		if err := validateCreateProject(&req, "owner"); err == nil {
			t.Fatalf("invalid project accepted: %#v", req)
		}
	}
	if err := validateCreateProject(nil, "owner"); err == nil {
		t.Fatal("nil create request accepted")
	}
	if err := validateUpdateProject(nil); err == nil {
		t.Fatal("nil update request accepted")
	}
	for _, value := range []string{"git@github.com:team/repo.git", "ssh://git@example.com/team/repo.git", "https://example.com/team/repo.git"} {
		if err := validateProjectRepoURL(value); err != nil {
			t.Fatalf("valid URL %q: %v", value, err)
		}
	}
}
