// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm"
)

type pollingDefinitionReader func(context.Context, scm.GitDefinitionRequest) (string, string, error)

// LoadPipelineDefinitionAtCommit 仅从项目已配置的 受支持的 HTTPS 仓库读取指定完整提交的定义
// 该函数不会回退到分支最新提交；调用方仍须解析 includes 并验证已保存的解析内容摘要
func LoadPipelineDefinitionAtCommit(
	ctx context.Context,
	pipeline *model.Pipeline,
	project *model.Project,
	fullSHA string,
) (string, string, error) {
	return loadPollingDefinition(ctx, pipeline, project, fullSHA, true, scm.ReadHTTPSDefinitionContext)
}

// LoadPollingPipelineDefinition 从项目已配置的 受支持的 HTTPS 仓库读取当前可信分支的有界定义及实际提交
// 该函数供轮询的基线和观察使用，不调用旧的工作树检出或 SSH 加载路径
func LoadPollingPipelineDefinition(
	ctx context.Context,
	pipeline *model.Pipeline,
	project *model.Project,
) (string, string, error) {
	return loadPollingDefinition(ctx, pipeline, project, "", false, scm.ReadHTTPSDefinitionContext)
}

func loadPollingDefinition(
	ctx context.Context,
	pipeline *model.Pipeline,
	project *model.Project,
	fullSHA string,
	pinned bool,
	read pollingDefinitionReader,
) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if pipeline == nil || project == nil {
		return "", "", errors.New("pipeline and project are required")
	}
	repository, err := polling.CanonicalRepository(project.RepoURL)
	if err != nil {
		return "", "", errors.New("polling definitions require a configured HTTPS repository")
	}
	if _, _, _, err := polling.ProviderRepository(project.RepoType, repository); err != nil {
		return "", "", errors.New("polling definitions require a supported configured provider")
	}
	if project.ProjectID == "" || pipeline.ProjectID != project.ProjectID ||
		normalizedRepository(pipeline.RepoURL) != normalizedRepository(project.RepoURL) {
		return "", "", errors.New("polling definition must belong to the configured project repository")
	}
	if pinned && !validPinnedDefinitionSHA(fullSHA) {
		return "", "", errors.New("definition revision must be a full lowercase SHA-1 commit")
	}
	if project.AuthType != model.AuthTypeNone && project.AuthType != model.AuthTypePassword && project.AuthType != model.AuthTypeToken {
		return "", "", errors.New("polling definitions require HTTPS-compatible authentication")
	}
	if project.AuthType != model.AuthTypeNone && strings.TrimSpace(project.Credential) == "" {
		return "", "", errors.New("configured definition authentication is unavailable")
	}
	auth, err := scmAuthForPipeline(pipeline, project)
	if err != nil {
		return "", "", err
	}
	gitAuth := scm.NewGitAuthFromMap(auth)
	if project.AuthType != model.AuthTypeNone && gitAuth.Token == "" && gitAuth.Password == "" {
		return "", "", errors.New("configured definition authentication is unavailable")
	}
	content, actualSHA, err := read(ctx, scm.GitDefinitionRequest{
		// Network access always uses the server-side project's URL, never a caller override.
		RepoURL: project.RepoURL, Branch: pipeline.DefaultBranch, CommitSHA: fullSHA,
		FilePath: pipeline.PipelineFilePath, Auth: gitAuth,
	})
	if err != nil {
		return "", "", err
	}
	if !validPinnedDefinitionSHA(actualSHA) || (pinned && actualSHA != fullSHA) {
		return "", "", errors.New("definition commit verification failed")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return content, actualSHA, nil
}

func validPinnedDefinitionSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
