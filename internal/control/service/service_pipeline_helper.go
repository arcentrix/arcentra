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
	auth := scmAuthFromProject(project)
	workdir, err := os.MkdirTemp("", "arcentra-pipeline-read-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: pipeline.RepoURL,
		Branch:  pipeline.DefaultBranch,
		Auth:    scm.NewGitAuthFromMap(auth),
	}); cloneErr != nil {
		return "", "", cloneErr
	}
	headSha, err := scm.HeadSHA(scm.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return "", "", err
	}
	filePath := strings.TrimLeft(strings.TrimSpace(pipeline.PipelineFilePath), "/")
	content, err := os.ReadFile(filepath.Join(workdir, filePath))
	if err != nil {
		return "", "", err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}
	return string(content), headSha, nil
}
