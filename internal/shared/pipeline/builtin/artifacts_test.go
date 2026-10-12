// Copyright 2025 Arcentra Authors.
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

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/bytedance/sonic"
)

type artifactTestContext struct {
	pipeline *spec.Pipeline
	root     string
	runID    string
}

func (c *artifactTestContext) GetPipeline() *spec.Pipeline { return c.pipeline }
func (c *artifactTestContext) GetWorkspaceRoot() string    { return c.root }
func (c *artifactTestContext) GetPipelineRunID() string    { return c.runID }

func artifactOptions(t *testing.T, root, runID, job string) *Options {
	t.Helper()
	workspace := filepath.Join(root, "workspaces", runID, job)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Options{
		Workspace: workspace,
		Job:       &spec.Job{Name: job},
		ExecutionContext: &artifactTestContext{
			pipeline: &spec.Pipeline{Namespace: "test"}, root: root, runID: runID,
		},
	}
}

func writeArtifactTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readArtifactTestFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func artifactJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := sonic.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestArtifactsAreIsolatedByRunAndJob(t *testing.T) {
	root := t.TempDir()
	manager := &Manager{}
	ctx := context.Background()
	for _, test := range []struct{ run, job, content string }{
		{"run-one", "build", "first build"},
		{"run-one", "package", "package output"},
		{"run-two", "build", "second build"},
	} {
		opts := artifactOptions(t, root, test.run, test.job)
		writeArtifactTestFile(t, opts.Workspace, "dist/nested/result.txt", test.content)
		result, err := manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"dist"}}), opts)
		if err != nil {
			t.Fatal(err)
		}
		var uploaded struct {
			Directory string `json:"artifacts_dir"`
		}
		if err := sonic.Unmarshal(result, &uploaded); err != nil {
			t.Fatal(err)
		}
		wantDirectory := filepath.Join(root, "test", test.run, "artifacts", test.job)
		if uploaded.Directory != wantDirectory {
			t.Fatalf("artifact directory = %q, want %q", uploaded.Directory, wantDirectory)
		}
	}

	for _, test := range []struct{ run, sourceJob, want string }{
		{"run-one", "build", "first build"},
		{"run-one", "package", "package output"},
		{"run-two", "build", "second build"},
	} {
		opts := artifactOptions(t, root, test.run, "consume-"+test.sourceJob)
		_, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{Job: test.sourceJob}), opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := readArtifactTestFile(t, opts.Workspace, "dist/nested/result.txt"); got != test.want {
			t.Fatalf("downloaded %q, want %q", got, test.want)
		}
	}
	missingRun := artifactOptions(t, root, "run-three", "consume")
	if _, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{Job: "build"}), missingRun); err == nil {
		t.Fatal("download unexpectedly used another run's artifacts")
	}
}

func TestArtifactsExpandGlobsAndPreserveNestedPaths(t *testing.T) {
	root := t.TempDir()
	producer := artifactOptions(t, root, "run", "build")
	for name, content := range map[string]string{
		"dist/a.txt": "A", "dist/b.txt": "B", "dist/bin/tool": "binary", ".version": "1",
	} {
		writeArtifactTestFile(t, producer.Workspace, name, content)
	}
	manager := &Manager{}
	ctx := context.Background()
	if _, err := manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"dist/*", ".version"}}), producer); err != nil {
		t.Fatal(err)
	}

	consumer := artifactOptions(t, root, "run", "consume")
	result, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{
		Job: "build", Paths: []string{"dist/*.txt", "dist/a.txt"},
	}), consumer)
	if err != nil {
		t.Fatal(err)
	}
	var downloaded struct {
		Paths []string `json:"downloaded_files"`
	}
	if err := sonic.Unmarshal(result, &downloaded); err != nil {
		t.Fatal(err)
	}
	if want := []string{"dist/a.txt", "dist/b.txt"}; !reflect.DeepEqual(downloaded.Paths, want) {
		t.Fatalf("download paths = %v, want %v", downloaded.Paths, want)
	}
	if got := readArtifactTestFile(t, consumer.Workspace, "dist/a.txt"); got != "A" {
		t.Fatalf("downloaded %q", got)
	}
	if _, err := os.Stat(filepath.Join(consumer.Workspace, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("download flattened the source path")
	}
	if _, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{Job: "build", Path: "dist/bin/tool"}), consumer); err != nil {
		t.Fatal(err)
	}
	if got := readArtifactTestFile(t, consumer.Workspace, "dist/bin/tool"); got != "binary" {
		t.Fatalf("downloaded %q", got)
	}
	all := artifactOptions(t, root, "run", "all")
	if _, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{Job: "build", Paths: []string{"*"}}), all); err != nil {
		t.Fatal(err)
	}
	if got := readArtifactTestFile(t, all.Workspace, ".version"); got != "1" {
		t.Fatalf("download omitted hidden file: %q", got)
	}
}

func TestReportsPreserveNamesAndRunJobScope(t *testing.T) {
	root := t.TempDir()
	manager := &Manager{}
	for _, run := range []string{"run-one", "run-two"} {
		for _, job := range []string{"build", "deploy"} {
			opts := artifactOptions(t, root, run, job)
			writeArtifactTestFile(t, opts.Workspace, "one/result.env", run+"/"+job+"/one")
			writeArtifactTestFile(t, opts.Workspace, "two/result.env", run+"/"+job+"/two")
			_, err := manager.handleReportsDotenv(context.Background(), artifactJSON(t, ReportsDotenvArgs{Dotenv: []string{"*/result.env"}}), opts)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, run := range []string{"run-one", "run-two"} {
		for _, job := range []string{"build", "deploy"} {
			for _, name := range []string{"one", "two"} {
				got := readArtifactTestFile(t, filepath.Join(root, "test", run, "reports", job), name+"/result.env")
				if want := run + "/" + job + "/" + name; got != want {
					t.Fatalf("report = %q, want %q", got, want)
				}
			}
		}
	}
}

func TestArtifactTransfersRejectUnsafePaths(t *testing.T) {
	root := t.TempDir()
	opts := artifactOptions(t, root, "run", "build")
	writeArtifactTestFile(t, opts.Workspace, "safe.txt", "safe")
	manager := &Manager{}
	if _, err := manager.handleArtifactsUpload(context.Background(), artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"safe.txt"}}), opts); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "dir/../../outside", "/tmp/outside", `C:\outside`, `..\outside`, "bad\x00name", ""} {
		t.Run(name, func(t *testing.T) {
			for action, call := range map[string]func() error{
				"upload": func() error {
					_, err := manager.handleArtifactsUpload(context.Background(), artifactJSON(t, ArtifactsUploadArgs{Paths: []string{name}}), opts)
					return err
				},
				"download": func() error {
					_, err := manager.handleArtifactsDownload(context.Background(), artifactJSON(t, ArtifactsDownloadArgs{Paths: []string{name}}), opts)
					return err
				},
				"report": func() error {
					_, err := manager.handleReportsDotenv(context.Background(), artifactJSON(t, ReportsDotenvArgs{Dotenv: []string{name}}), opts)
					return err
				},
			} {
				if err := call(); err == nil {
					t.Errorf("%s accepted unsafe path %q", action, name)
				}
			}
		})
	}
}

func TestArtifactTransfersRejectSymlinks(t *testing.T) {
	for _, test := range []string{"upload-file", "upload-directory", "upload-recursive", "report", "download-file", "download-directory", "store", "workspace"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			opts := artifactOptions(t, root, "run", "build")
			outside := t.TempDir()
			writeArtifactTestFile(t, outside, "secret", "do not read or overwrite")
			writeArtifactTestFile(t, opts.Workspace, "nested/result.env", "new result")
			manager := &Manager{}
			ctx := context.Background()
			if _, err := manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"nested"}}), opts); err != nil {
				t.Fatal(err)
			}
			link := func(target, name string) {
				t.Helper()
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch test {
			case "upload-file", "report":
				link(filepath.Join(outside, "secret"), filepath.Join(opts.Workspace, "link.env"))
				if test == "report" {
					_, err = manager.handleReportsDotenv(ctx, artifactJSON(t, ReportsDotenvArgs{Dotenv: []string{"link.env"}}), opts)
				} else {
					_, err = manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"link.env"}}), opts)
				}
			case "upload-directory":
				link(outside, filepath.Join(opts.Workspace, "linked"))
				_, err = manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"linked/secret"}}), opts)
			case "upload-recursive":
				link(outside, filepath.Join(opts.Workspace, "nested", "linked"))
				_, err = manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"nested"}}), opts)
			case "download-file", "download-directory":
				consumer := artifactOptions(t, root, "run", "consume")
				if test == "download-file" {
					if err := os.MkdirAll(filepath.Join(consumer.Workspace, "nested"), 0o755); err != nil {
						t.Fatal(err)
					}
					link(filepath.Join(outside, "secret"), filepath.Join(consumer.Workspace, "nested", "result.env"))
				} else {
					link(outside, filepath.Join(consumer.Workspace, "nested"))
				}
				_, err = manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{Job: "build"}), consumer)
			case "store":
				store := filepath.Join(root, "test", "run", "artifacts", "build")
				if err := os.RemoveAll(store); err != nil {
					t.Fatal(err)
				}
				link(outside, store)
				_, err = manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"nested"}}), opts)
			case "workspace":
				alias := filepath.Join(root, "alias")
				link(outside, alias)
				opts.Workspace = alias
				_, err = manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"secret"}}), opts)
			}
			if err == nil {
				t.Fatal("transfer accepted a symlink")
			}
			if got := readArtifactTestFile(t, outside, "secret"); got != "do not read or overwrite" {
				t.Fatalf("outside file modified: %q", got)
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("transfer wrote outside workspace: %v", entries)
			}
		})
	}
}

func TestArtifactTransfersValidateContextAndIdentity(t *testing.T) {
	for _, test := range []string{"nil-options", "nil-context", "nil-pipeline", "missing-run", "missing-job", "namespace-traversal", "run-traversal", "job-traversal", "external-workspace", "overlapping-workspace"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			opts := artifactOptions(t, root, "run", "build")
			c := opts.ExecutionContext.(*artifactTestContext)
			switch test {
			case "nil-options":
				opts = nil
			case "nil-context":
				opts.ExecutionContext = nil
			case "nil-pipeline":
				c.pipeline = nil
			case "missing-run":
				c.runID = ""
			case "missing-job":
				opts.Job = nil
			case "namespace-traversal":
				c.pipeline.Namespace = "../bad"
			case "run-traversal":
				c.runID = "../bad"
			case "job-traversal":
				opts.Job.Name = "../bad"
			case "external-workspace":
				opts.Workspace = t.TempDir()
			case "overlapping-workspace":
				opts.Workspace = root
			}
			manager := &Manager{}
			if _, err := manager.handleArtifactsUpload(context.Background(), artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"*"}}), opts); err == nil {
				t.Fatal("invalid execution context accepted")
			}
		})
	}
}

func TestArtifactTransfersRejectMissingAndMalformedPatterns(t *testing.T) {
	opts := artifactOptions(t, t.TempDir(), "run", "build")
	manager := &Manager{}
	for _, pattern := range []string{"missing.txt", "missing-*", "[bad"} {
		if _, err := manager.handleArtifactsUpload(context.Background(), artifactJSON(t, ArtifactsUploadArgs{Paths: []string{pattern}}), opts); err == nil {
			t.Errorf("pattern %q unexpectedly succeeded", pattern)
		}
	}
	if _, err := manager.handleReportsDotenv(context.Background(), artifactJSON(t, ReportsDotenvArgs{Dotenv: []string{"."}}), opts); err == nil {
		t.Fatal("dotenv report accepted a directory")
	}
}

func TestArtifactTransfersHonorCancellation(t *testing.T) {
	opts := artifactOptions(t, t.TempDir(), "run", "build")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &Manager{}
	for name, call := range map[string]func() error{
		"upload": func() error {
			_, err := manager.handleArtifactsUpload(ctx, artifactJSON(t, ArtifactsUploadArgs{Paths: []string{"*"}}), opts)
			return err
		},
		"download": func() error {
			_, err := manager.handleArtifactsDownload(ctx, artifactJSON(t, ArtifactsDownloadArgs{}), opts)
			return err
		},
		"report": func() error {
			_, err := manager.handleReportsDotenv(ctx, artifactJSON(t, ReportsDotenvArgs{Dotenv: []string{"*"}}), opts)
			return err
		},
	} {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s error = %v, want canceled", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(opts.ExecutionContext.GetWorkspaceRoot(), "test")); !os.IsNotExist(err) {
		t.Fatal("canceled operation created storage directories")
	}
}

type cancelAfterChecksContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelAfterChecksContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCopyCancellationPreservesDestinationAndRemovesTemporaryFile(t *testing.T) {
	sourcePath, destinationPath := t.TempDir(), t.TempDir()
	writeArtifactTestFile(t, sourcePath, "result", strings.Repeat("payload", 100000))
	writeArtifactTestFile(t, destinationPath, "result", "previous version")
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenRoot(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = destination.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 两次路径检查和第一次读取之后取消，确保中断发生在复制过程中
	interrupted := &cancelAfterChecksContext{Context: ctx, cancel: cancel, remaining: 4}
	if err := copyRootFile(interrupted, source, destination, "result"); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v, want canceled", err)
	}
	if got := readArtifactTestFile(t, destinationPath, "result"); got != "previous version" {
		t.Fatalf("canceled copy replaced destination with %q", got)
	}
	entries, err := os.ReadDir(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "result" {
		t.Fatalf("copy leaked temporary files: %v", entries)
	}
}
