// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/scm"
)

func pinnedDefinitionModels() (*model.Pipeline, *model.Project) {
	return &model.Pipeline{
			PipelineID: "pipeline-fixture", ProjectID: "project-fixture", RepoURL: "https://gitlab.invalid/team/app.git",
			DefaultBranch: "main", PipelineFilePath: ".arcentra/pipeline.yml",
		}, &model.Project{
			ProjectID: "project-fixture", RepoType: model.RepoTypeGitLab, RepoURL: "https://gitlab.invalid/team/app.git",
			AuthType: model.AuthTypeToken, Credential: "synthetic-token",
		}
}

func TestPollingDefinitionBindsProjectRepositoryAndAuthentication(t *testing.T) {
	pipeline, project := pinnedDefinitionModels()
	pipeline.RepoURL = strings.TrimSuffix(project.RepoURL, ".git")
	sha := strings.Repeat("a", 40)
	content, actual, err := loadPollingDefinition(context.Background(), pipeline, project, sha, true,
		func(_ context.Context, req scm.GitDefinitionRequest) (string, string, error) {
			if req.RepoURL != project.RepoURL || req.Branch != "main" || req.CommitSHA != sha ||
				req.FilePath != pipeline.PipelineFilePath || req.Auth.Token != "synthetic-token" || req.Auth.SSHKey != "" {
				t.Fatalf("configured binding changed: %+v", req)
			}
			return "name: pinned\n", sha, nil
		})
	if err != nil || actual != sha || content != "name: pinned\n" {
		t.Fatalf("bound pinned definition failed: %q %q %v", content, actual, err)
	}
	_, actual, err = loadPollingDefinition(context.Background(), pipeline, project, "", false,
		func(_ context.Context, req scm.GitDefinitionRequest) (string, string, error) {
			if req.CommitSHA != "" || req.Branch != "main" {
				t.Fatal("current definition did not request the configured branch")
			}
			return "name: current\n", sha, nil
		})
	if err != nil || actual != sha {
		t.Fatalf("bound current definition failed: %q %v", actual, err)
	}
}

func TestPollingDefinitionRejectsInvalidBindingBeforeLoading(t *testing.T) {
	cases := map[string]func(*model.Pipeline, *model.Project){
		"generic-git":      func(_ *model.Pipeline, p *model.Project) { p.RepoType = "git" },
		"other-provider":   func(_ *model.Pipeline, p *model.Project) { p.RepoType = "github" },
		"other-project":    func(p *model.Pipeline, _ *model.Project) { p.ProjectID = "other-project" },
		"other-origin":     func(p *model.Pipeline, _ *model.Project) { p.RepoURL = "https://other.invalid/team/app.git" },
		"other-repository": func(p *model.Pipeline, _ *model.Project) { p.RepoURL = "https://gitlab.invalid/other/app.git" },
		"public-repository-override": func(p *model.Pipeline, project *model.Project) {
			p.RepoURL = "https://gitlab.invalid/other/app.git"
			project.Credential, project.AuthType = "", model.AuthTypeNone
		},
		"ssh-auth":     func(_ *model.Pipeline, p *model.Project) { p.AuthType = model.AuthTypeSSHKey },
		"unknown-auth": func(_ *model.Pipeline, p *model.Project) { p.AuthType = 99 },
		"missing-auth": func(_ *model.Pipeline, p *model.Project) { p.Credential = "" },
		"empty-password": func(_ *model.Pipeline, p *model.Project) {
			p.AuthType, p.Credential = model.AuthTypePassword, "fixture:"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pipeline, project := pinnedDefinitionModels()
			mutate(pipeline, project)
			called := false
			_, _, err := loadPollingDefinition(context.Background(), pipeline, project, strings.Repeat("a", 40), true,
				func(context.Context, scm.GitDefinitionRequest) (string, string, error) {
					called = true
					return "", "", nil
				})
			if err == nil || called {
				t.Fatalf("invalid scope reached loader: %v, called=%v", err, called)
			}
		})
	}
}

func TestPollingDefinitionRejectsEmptyOrAbbreviatedPinnedRevision(t *testing.T) {
	pipeline, project := pinnedDefinitionModels()
	for _, sha := range []string{"", "main", "deadbeef", strings.Repeat("A", 40), strings.Repeat("a", 40) + "~1"} {
		called := false
		_, _, err := loadPollingDefinition(context.Background(), pipeline, project, sha, true,
			func(context.Context, scm.GitDefinitionRequest) (string, string, error) {
				called = true
				return "", "", nil
			})
		if err == nil || called {
			t.Fatalf("unsafe pinned revision reached loader: %q %v", sha, err)
		}
	}
}

func TestPollingDefinitionVerifiesReloadedRevision(t *testing.T) {
	pipeline, project := pinnedDefinitionModels()
	for _, actual := range []string{"", "deadbeef", strings.Repeat("b", 40)} {
		_, _, err := loadPollingDefinition(context.Background(), pipeline, project, strings.Repeat("a", 40), true,
			func(context.Context, scm.GitDefinitionRequest) (string, string, error) {
				return "name: wrong\n", actual, nil
			})
		if err == nil {
			t.Fatalf("mismatched revision accepted: %q", actual)
		}
	}
}

func TestPollingDefinitionPublicEntryPointsEnforceHTTPS(t *testing.T) {
	for _, repository := range []string{"http://gitlab.invalid/team/app.git", "https://user:secret@gitlab.invalid/team/app.git", "git@gitlab.invalid:team/app.git"} {
		pipeline, project := pinnedDefinitionModels()
		pipeline.RepoURL, project.RepoURL = repository, repository
		if _, _, err := LoadPollingPipelineDefinition(context.Background(), pipeline, project); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe current repository accepted or secret exposed: %v", err)
		}
		if _, _, err := LoadPipelineDefinitionAtCommit(context.Background(), pipeline, project, strings.Repeat("a", 40)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe pinned repository accepted or secret exposed: %v", err)
		}
	}
}

func TestPollingDefinitionPreservesCancellation(t *testing.T) {
	pipeline, project := pinnedDefinitionModels()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := loadPollingDefinition(ctx, pipeline, project, strings.Repeat("a", 40), true,
		func(context.Context, scm.GitDefinitionRequest) (string, string, error) {
			cancel()
			return "name: cancelled\n", strings.Repeat("a", 40), nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled definition was returned: %v", err)
	}
}
