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

package builtin_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/builtin"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"go.uber.org/zap"
)

func TestArtifactBuiltinsUseRealJobExecutionContext(t *testing.T) {
	logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
	parent := pipeline.NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
	parent.PipelineRunID = "run-one"
	producer := parent.ForJob()
	buildWorkspace := producer.StepWorkspace("build", "compile")
	if err := os.MkdirAll(filepath.Join(buildWorkspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildWorkspace, "dist", "result.txt"), []byte("built output"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := producer.BuiltinManager.Execute(context.Background(), "artifacts", "upload", []byte(`{"paths":["dist"]}`), &builtin.Options{
		Workspace: producer.StepWorkspace("build", "upload"), Job: &spec.Job{Name: "build"}, ExecutionContext: producer,
	})
	if err != nil {
		t.Fatal(err)
	}

	consumer := parent.ForJob()
	consumeWorkspace := consumer.StepWorkspace("deploy", "download")
	_, err = consumer.BuiltinManager.Execute(context.Background(), "artifacts", "download", []byte(`{"job":"build"}`), &builtin.Options{
		Workspace: consumeWorkspace, Job: &spec.Job{Name: "deploy"}, ExecutionContext: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(consumer.StepWorkspace("deploy", "publish"), "dist", "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "built output" {
		t.Fatalf("unexpected downstream artifact content: %q", content)
	}
}
