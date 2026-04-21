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

package template

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arcentrix/arcentra/pkg/scm"
	"go.yaml.in/yaml/v3"
)

// SyncRequest holds everything needed to clone and scan a library repo.
type SyncRequest struct {
	RepoURL     string
	Ref         string // branch or tag
	Auth        scm.GitAuth
	TemplateDir string // e.g. "templates"
}

// CloneAndDiscover clones the template library repository and discovers
// all templates inside the configured template directory.
// It returns the HEAD commit SHA and a list of discovered templates.
func CloneAndDiscover(req SyncRequest) (string, *LibraryManifest, []DiscoveredTemplate, error) {
	workdir, err := os.MkdirTemp("", "arcentra-template-sync-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: req.RepoURL,
		Branch:  req.Ref,
		Auth:    req.Auth,
	}); cloneErr != nil {
		return "", nil, nil, fmt.Errorf("clone repo: %w", cloneErr)
	}

	headSha, err := scm.HeadSHA(scm.GitHeadSHARequest{Workdir: workdir})
	if err != nil {
		return "", nil, nil, fmt.Errorf("read HEAD: %w", err)
	}

	libManifest, err := parseLibraryManifest(workdir)
	if err != nil {
		return headSha, nil, nil, fmt.Errorf("parse library.yaml: %w", err)
	}

	tmplDir := filepath.Join(workdir, req.TemplateDir)
	templates, err := discoverTemplates(tmplDir)
	if err != nil {
		return headSha, libManifest, nil, fmt.Errorf("discover templates: %w", err)
	}

	return headSha, libManifest, templates, nil
}

// ListGitTags clones the repository and returns all tags matching the v* prefix.
func ListGitTags(req SyncRequest) ([]string, error) {
	workdir, err := os.MkdirTemp("", "arcentra-template-tags-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: req.RepoURL,
		Branch:  req.Ref,
		Auth:    req.Auth,
	}); cloneErr != nil {
		return nil, fmt.Errorf("clone repo: %w", cloneErr)
	}

	cmd := exec.Command("git", "tag", "-l")
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		tag := strings.TrimSpace(line)
		if tag != "" && strings.HasPrefix(tag, "v") {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

// parseLibraryManifest reads and parses the library.yaml at repo root.
func parseLibraryManifest(workdir string) (*LibraryManifest, error) {
	for _, name := range []string{"library.yaml", "library.yml"} {
		data, err := os.ReadFile(filepath.Join(workdir, name))
		if err != nil {
			continue
		}
		var m LibraryManifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal %s: %w", name, err)
		}
		return &m, nil
	}
	return &LibraryManifest{}, nil
}

// discoverTemplates walks the template directory and parses each
// sub-directory that contains a template.yaml (or template.yml).
func discoverTemplates(tmplDir string) ([]DiscoveredTemplate, error) {
	entries, err := os.ReadDir(tmplDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var result []DiscoveredTemplate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(tmplDir, entry.Name())
		manifest, mErr := parseTemplateManifest(dirPath)
		if mErr != nil {
			continue
		}
		if manifest.Name == "" {
			manifest.Name = entry.Name()
		}

		specContent, sErr := readSpecContent(dirPath)
		if sErr != nil {
			continue
		}

		readme := readOptionalFile(dirPath, "README.md")

		result = append(result, DiscoveredTemplate{
			DirName:     entry.Name(),
			Manifest:    *manifest,
			SpecContent: specContent,
			Readme:      readme,
		})
	}
	return result, nil
}

// parseTemplateManifest reads template.yaml from a template directory.
func parseTemplateManifest(dir string) (*TemplateManifest, error) {
	for _, name := range []string{"template.yaml", "template.yml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var m TemplateManifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
	return nil, fmt.Errorf("template.yaml not found in %s", dir)
}

// readSpecContent reads spec.yaml (or spec.yml / spec.json) from a template directory.
func readSpecContent(dir string) (string, error) {
	for _, name := range []string{"spec.yaml", "spec.yml", "spec.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", fmt.Errorf("spec file not found in %s", dir)
}

// readOptionalFile reads a file and returns its content, or empty string on error.
func readOptionalFile(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
