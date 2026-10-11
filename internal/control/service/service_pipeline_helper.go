// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/scm"
)

// LoadPipelineDefinition clones the pipeline's backing repository and reads
// the YAML definition file. It returns the file content and the HEAD commit
// SHA. This is a package-level helper used by CronTriggerManager, ScmService,
// and PipelineServiceImpl to avoid duplicating the clone-and-read logic.
func LoadPipelineDefinition(
	ctx context.Context,
	pipeline *model.Pipeline,
	project *model.Project,
) (string, string, error) {
	auth, err := scmAuthForPipeline(pipeline, project)
	if err != nil {
		return "", "", err
	}
	workdir, err := os.MkdirTemp("", "arcentra-pipeline-read-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.CloneContext(ctx, scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: pipeline.RepoURL,
		Branch:  pipeline.DefaultBranch,
		Auth:    scm.NewGitAuthFromMap(auth),
	}); cloneErr != nil {
		return "", "", cloneErr
	}
	headSha, err := scm.HeadSHAContext(ctx, scm.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return "", "", err
	}
	filePath, err := validatedPipelinePath(pipeline.PipelineFilePath)
	if err != nil {
		return "", "", err
	}
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	content, err := root.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}
	return string(content), headSha, nil
}

func validatedPipelinePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !filepath.IsLocal(value) || strings.Contains(value, "\\") {
		return "", fmt.Errorf("pipeline file must be a relative repository path")
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." {
		return "", fmt.Errorf("pipeline file is required")
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".git" {
			return "", fmt.Errorf("pipeline file cannot be inside .git")
		}
	}
	return cleaned, nil
}

func scmAuthForPipeline(pipeline *model.Pipeline, project *model.Project) (map[string]string, error) {
	if pipeline == nil || project == nil {
		return nil, fmt.Errorf("pipeline and project are required")
	}
	if strings.TrimSpace(project.Credential) != "" && normalizedRepository(pipeline.RepoURL) != normalizedRepository(project.RepoURL) {
		return nil, fmt.Errorf("project SCM credentials may only be used with the project's configured repository")
	}
	return scmAuthFromProject(project), nil
}

func normalizedRepository(value string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(value), "/"), ".git")
}
