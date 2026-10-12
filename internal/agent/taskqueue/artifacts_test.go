package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type memoryArtifactStorage struct {
	storage.IStorage // 非流式接口故意不可用，确保执行路径始终使用流式传输
	objects          map[string][]byte
	uploads          int
	downloads        int
	download         func(context.Context, string) (io.ReadCloser, error)
	upload           func(context.Context, string, io.ReadSeeker, int64) error
}

func (s *memoryArtifactStorage) UploadStream(ctx context.Context, name string, input io.ReadSeeker, size int64, _ string) error {
	s.uploads++
	if s.upload != nil {
		return s.upload(ctx, name, input, size)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("stream size mismatch")
	}
	if s.objects == nil {
		s.objects = make(map[string][]byte)
	}
	s.objects[name] = data
	return nil
}

func (s *memoryArtifactStorage) DownloadStream(ctx context.Context, name string) (io.ReadCloser, error) {
	s.downloads++
	if s.download != nil {
		return s.download(ctx, name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := s.objects[name]
	if !ok {
		return nil, fmt.Errorf("object %s not found", name)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func artifactTestWrite(t *testing.T, root, name, value string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func artifactTestRead(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func artifactTestRequest(workspace, action string, args map[string]any) *executor.ExecutionRequest {
	return &executor.ExecutionRequest{Workspace: workspace, Step: &executor.StepInfo{Uses: "builtin:artifacts", Action: action, Args: args}}
}

func TestRemoteArtifactsUploadDownloadPreservePathsAndRunIsolation(t *testing.T) {
	ctx := context.Background()
	st := &memoryArtifactStorage{}
	upstream := map[string]string{}
	for _, run := range []string{"run-one", "run-two"} {
		workspace := t.TempDir()
		for name, value := range map[string]string{"dist/a.txt": run, "dist/b.txt": "B", "dist/nested/tool": "tool", "dist_a.txt": "flat", ".version": "version"} {
			artifactTestWrite(t, workspace, name, value)
		}
		producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: run, JobRunID: "build-run", JobName: "build"}, storage: st}
		result, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"dist/*", "dist/a.txt", "dist_a.txt", ".version"}}))
		if err != nil || !result.Success {
			t.Fatalf("upload: %+v %v", result, err)
		}
		if len(producer.uploaded) != 5 {
			t.Fatalf("duplicate or missing files: %v", producer.uploaded)
		}
		for name, key := range producer.uploaded {
			if key != "artifacts/"+run+"/build-run/"+name {
				t.Fatalf("object key is not scoped or was replaced with a URL: %s", key)
			}
			if run == "run-one" {
				upstream["build/"+name] = key
			}
		}
	}
	workspace := t.TempDir()
	consumer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{JobName: "consume", ArtifactURIs: upstream}, storage: st}
	if err := handleArtifactDownload(ctx, upstream, workspace, st); err != nil {
		t.Fatal(err)
	}
	if got := artifactTestRead(t, workspace, ".artifacts/build/dist/a.txt"); got != "run-one" {
		t.Fatalf("prefetch content: %s", got)
	}
	if got := artifactTestRead(t, workspace, ".artifacts/build/dist_a.txt"); got != "flat" {
		t.Fatalf("prefetch flattened colliding names: %s", got)
	}
	if _, err := consumer.Execute(ctx, artifactTestRequest(workspace, "download", map[string]any{"job": "build", "paths": []string{"dist/*.txt", "dist/a.txt"}})); err != nil {
		t.Fatal(err)
	}
	if got := artifactTestRead(t, workspace, "dist/a.txt"); got != "run-one" {
		t.Fatalf("download crossed run boundary: %s", got)
	}
	if _, err := os.Stat(filepath.Join(workspace, "dist", "nested", "tool")); !os.IsNotExist(err) {
		t.Fatal("download filter ignored")
	}
	if _, err := consumer.Execute(ctx, artifactTestRequest(workspace, "download", map[string]any{"job": "build", "path": "dist/nested"})); err != nil {
		t.Fatal(err)
	}
	if got := artifactTestRead(t, workspace, "dist/nested/tool"); got != "tool" {
		t.Fatal(got)
	}
	if _, err := consumer.Execute(ctx, artifactTestRequest(workspace, "download", map[string]any{"job": "build"})); err != nil {
		t.Fatal(err)
	}
	if got := artifactTestRead(t, workspace, ".version"); got != "version" {
		t.Fatal("default download omitted a hidden file")
	}
	if _, err := consumer.Execute(ctx, artifactTestRequest(workspace, "download", map[string]any{"job": "unknown"})); err == nil {
		t.Fatal("unknown upstream job was accepted")
	}
}

func TestRemoteArtifactsRejectUnsafePathsAndSymlinks(t *testing.T) {
	ctx := context.Background()
	workspace, outside := t.TempDir(), t.TempDir()
	artifactTestWrite(t, workspace, "dist/file", "inside")
	artifactTestWrite(t, outside, "secret", "outside")
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "dist", "file"), filepath.Join(workspace, "internal-link")); err != nil {
		t.Fatal(err)
	}
	st := &memoryArtifactStorage{}
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st}
	for _, pattern := range []string{"../secret", "/etc/passwd", "C:/secret", `dist\file`, "dist/../dist/file", "link", "link/*", "internal-link", "[broken", "missing*"} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{pattern}})); err == nil {
				t.Fatal("unsafe or missing artifact accepted")
			}
		})
	}
	if st.uploads != 0 {
		t.Fatalf("invalid artifacts caused %d uploads", st.uploads)
	}
	for _, id := range []string{"", "..", "../run", "run/job", `run\job`} {
		producer.job.PipelineRunID = id
		if _, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"dist/file"}})); err == nil {
			t.Fatalf("unsafe run ID %q was accepted", id)
		}
	}
	for _, name := range []string{"../secret", "/absolute", "build/../../secret", "link/secret", "internal-link"} {
		root, err := openArtifactWorkspace(ctx, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := downloadArtifactFile(ctx, root, name, "key", st); err == nil {
			t.Fatalf("unsafe download target %q accepted", name)
		}
		_ = root.Close()
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".artifacts")); err != nil {
		t.Fatal(err)
	}
	if err := handleArtifactDownload(ctx, map[string]string{"build/secret": "key"}, workspace, st); err == nil {
		t.Fatal("prefetch followed a symlink")
	}
	if err := handleArtifactDownload(ctx, map[string]string{"../secret": "key"}, workspace, st); err == nil {
		t.Fatal("prefetch accepted path traversal")
	}
	if st.downloads != 0 {
		t.Fatal("unsafe destinations caused network downloads")
	}
	if got := artifactTestRead(t, outside, "secret"); got != "outside" {
		t.Fatal("outside file modified")
	}
}

type interruptedArtifactReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *interruptedArtifactReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		if r.cancel != nil {
			r.cancel()
		}
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func (r *interruptedArtifactReader) Close() error { return nil }

func TestRemoteArtifactCancellationAndAtomicDownload(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workspace := t.TempDir()
			artifactTestWrite(t, workspace, "dist/existing", "original")
			st := &memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) {
				reader := &interruptedArtifactReader{}
				if cancelled {
					reader.cancel = cancel
				}
				return reader, nil
			}}
			root, err := openArtifactWorkspace(ctx, workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = downloadArtifactFile(ctx, root, "dist/existing", "key", st)
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("transfer error lost: %v", err)
			}
			if got := artifactTestRead(t, workspace, "dist/existing"); got != "original" {
				t.Fatalf("failed download truncated existing file: %s", got)
			}
			entries, err := os.ReadDir(filepath.Join(workspace, "dist"))
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
				t.Fatalf("temporary download file leaked: %v %v", entries, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "file", strings.Repeat("x", 64*1024))
	st := &memoryArtifactStorage{upload: func(_ context.Context, _ string, reader io.ReadSeeker, _ int64) error {
		buf := make([]byte, 1)
		if _, err := reader.Read(buf); err != nil {
			return err
		}
		cancel()
		_, err := reader.Read(buf)
		return err
	}}
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st}
	if _, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"file"}})); !errors.Is(err, context.Canceled) {
		t.Fatalf("upload ignored cancellation: %v", err)
	}
	if len(producer.uploaded) != 0 {
		t.Fatal("incomplete upload reported as a valid artifact")
	}
	before := st.uploads
	if _, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"file"}})); !errors.Is(err, context.Canceled) || st.uploads != before {
		t.Fatal("cancelled artifact operation started another transfer")
	}
}

type artifactStatusClient struct {
	agentv1.AgentServiceClient
	jobReports        []*agentv1.ReportJobRunStatusRequest
	stepReports       []*agentv1.ReportStepRunStatusRequest
	failJobTerminals  int
	failStepTerminals int
	failStepID        string
}

func (c *artifactStatusClient) ReportStepRunStatus(_ context.Context, req *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	c.stepReports = append(c.stepReports, req)
	if int(req.Status) >= 4 && (c.failStepID == "" || req.StepRunId == c.failStepID) && c.failStepTerminals > 0 {
		c.failStepTerminals--
		return nil, status.Error(codes.Unavailable, "step status unavailable")
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}
func (c *artifactStatusClient) ReportJobRunStatus(_ context.Context, req *agentv1.ReportJobRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	c.jobReports = append(c.jobReports, req)
	if req.Status >= 4 && c.failJobTerminals > 0 {
		c.failJobTerminals--
		return nil, status.Error(codes.Unavailable, "job status unavailable")
	}
	return &agentv1.ReportJobRunStatusResponse{Success: true}, nil
}

func TestJobArtifactBuiltinReportsURIsAndRetriedReportDoesNotUploadAgain(t *testing.T) {
	st := &memoryArtifactStorage{}
	SetStorage(st)
	t.Cleanup(func() { SetStorage(nil) })
	ctx := context.Background()
	client := &artifactStatusClient{failJobTerminals: 1}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	producer := &taskpayload.JobRunTaskPayload{PipelineRunID: "artifact-run", JobRunID: "artifact-producer", JobName: "build", Steps: []taskpayload.StepPayload{
		{Name: "build", StepRunID: "artifact-build", Args: map[string]any{"run": "mkdir -p dist/nested; printf artifact > dist/nested/file"}},
		{Name: "upload", StepRunID: "artifact-upload", Uses: "builtin:artifacts", Action: "upload", Args: map[string]any{"paths": []string{"dist"}}},
	}}
	if err := executeJobRun(ctx, conf, wrapper, nil, producer); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected terminal report retry: %v", err)
	}
	if err := executeJobRun(ctx, conf, wrapper, nil, producer); err != nil {
		t.Fatal(err)
	}
	if st.uploads != 1 {
		t.Fatalf("terminal retry repeated upload: %d", st.uploads)
	}
	report := client.jobReports[len(client.jobReports)-1]
	want := map[string]string{"dist/nested/file": "artifacts/artifact-run/artifact-producer/dist/nested/file"}
	if report.Status != 4 || !reflect.DeepEqual(report.ArtifactUris, want) {
		t.Fatalf("terminal artifacts missing: %+v", report)
	}
	consumer := &taskpayload.JobRunTaskPayload{PipelineRunID: "artifact-run", JobRunID: "artifact-consumer", JobName: "consume", ArtifactURIs: map[string]string{"build/dist/nested/file": want["dist/nested/file"]}, Steps: []taskpayload.StepPayload{
		{Name: "download", StepRunID: "artifact-download", Uses: "builtin:artifacts", Action: "download", Args: map[string]any{"job": "build", "path": "dist"}},
		{Name: "consume", StepRunID: "artifact-consume", Args: map[string]any{"run": `test "$(cat dist/nested/file)" = artifact; test "$(cat .artifacts/build/dist/nested/file)" = artifact`}},
	}}
	if err := executeJobRun(ctx, conf, wrapper, nil, consumer); err != nil {
		t.Fatal(err)
	}
	if report := client.jobReports[len(client.jobReports)-1]; report.Status != 4 || len(report.ArtifactUris) != 0 {
		t.Fatalf("consumer terminal status: %+v", report)
	}
}

func TestPreparationArtifactFailureRedactsJobCredentials(t *testing.T) {
	st := &memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("storage failure: env-secret")
	}}
	SetStorage(st)
	t.Cleanup(func() { SetStorage(nil) })
	client := &artifactStatusClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "redact-artifact-prep", Env: map[string]string{"secrets.TEST": "env-secret"}, ArtifactURIs: map[string]string{"build/file": "object"}}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	err := executeJobRun(context.Background(), conf, wrapper, nil, payload)
	if err == nil || strings.Contains(err.Error(), "env-secret") {
		t.Fatalf("unredacted preparation error: %v", err)
	}
	if report := client.jobReports[len(client.jobReports)-1]; report.Status != 5 || strings.Contains(report.ErrorMessage, "env-secret") || !strings.Contains(report.ErrorMessage, "[REDACTED]") {
		t.Fatalf("unredacted terminal report: %+v", report)
	}
}
