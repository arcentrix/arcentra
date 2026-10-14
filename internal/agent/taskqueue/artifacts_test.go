package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	return &executor.ExecutionRequest{Workspace: workspace, StepRunID: "upload-step", Step: &executor.StepInfo{Uses: "builtin:artifacts", Action: action, Args: args}}
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
		producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: run, JobRunID: "build-run", JobName: "build"}, storage: st, reference: artifactTestReference()}
		result, err := producer.Execute(ctx, artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"dist/*", "dist/a.txt", "dist_a.txt", ".version"}}))
		if err != nil || !result.Success {
			t.Fatalf("upload: %+v %v", result, err)
		}
		if len(producer.uploaded) != 5 {
			t.Fatalf("duplicate or missing files: %v", producer.uploaded)
		}
		for name, key := range producer.uploaded {
			if !strings.HasPrefix(key, "artifacts/"+run+"/build-run/upload-step/") || !strings.HasSuffix(key, "/"+name) {
				t.Fatalf("object key is not scoped or was replaced with a URL: %s", key)
			}
			if run == "run-one" {
				upstream["build/"+name] = key
			}
		}
	}
	workspace := t.TempDir()
	consumer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{JobName: "consume", ArtifactURIs: upstream, ArtifactReferences: artifactTestReferences(upstream, st.objects)}, storage: st, reference: artifactTestReference()}
	if err := handleArtifactDownload(ctx, upstream, consumer.job.ArtifactReferences, workspace, st); err != nil {
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
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st, reference: artifactTestReference()}
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
		if err := downloadArtifactFile(ctx, root, name, "key", artifactTestContentReference("bytes"), st); err == nil {
			t.Fatalf("unsafe download target %q accepted", name)
		}
		_ = root.Close()
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".artifacts")); err != nil {
		t.Fatal(err)
	}
	if err := handleArtifactDownload(ctx, map[string]string{"build/secret": "key"}, map[string]taskpayload.ArtifactReference{"build/secret": artifactTestContentReference("bytes")}, workspace, st); err == nil {
		t.Fatal("prefetch followed a symlink")
	}
	if err := handleArtifactDownload(ctx, map[string]string{"../secret": "key"}, map[string]taskpayload.ArtifactReference{"../secret": artifactTestContentReference("bytes")}, workspace, st); err == nil {
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
			err = downloadArtifactFile(ctx, root, "dist/existing", "key", artifactTestContentReference("partial content"), st)
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
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st, reference: artifactTestReference()}
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
	SetStorage(st, artifactTestReference())
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
	want := map[string]string{"dist/nested/file": fmt.Sprintf("artifacts/artifact-run/artifact-producer/artifact-upload/%x/dist/nested/file", sha256.Sum256([]byte("artifact")))}
	if report.Status != 4 || !reflect.DeepEqual(report.ArtifactUris, want) {
		t.Fatalf("terminal artifacts missing: %+v", report)
	}
	consumer := &taskpayload.JobRunTaskPayload{PipelineRunID: "artifact-run", JobRunID: "artifact-consumer", JobName: "consume", ArtifactURIs: map[string]string{"build/dist/nested/file": want["dist/nested/file"]}, ArtifactReferences: artifactTestReferences(map[string]string{"build/dist/nested/file": want["dist/nested/file"]}, st.objects), Steps: []taskpayload.StepPayload{
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
	SetStorage(st, artifactTestReference())
	t.Cleanup(func() { SetStorage(nil) })
	client := &artifactStatusClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "redact-artifact-prep", Env: map[string]string{"secrets.TEST": "env-secret"}, ArtifactURIs: map[string]string{"build/file": "object"}, ArtifactReferences: map[string]taskpayload.ArtifactReference{"build/file": artifactTestContentReference("bytes")}}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
	err := executeJobRun(context.Background(), conf, wrapper, nil, payload)
	if err == nil || strings.Contains(err.Error(), "env-secret") {
		t.Fatalf("unredacted preparation error: %v", err)
	}
	if report := client.jobReports[len(client.jobReports)-1]; report.Status != 5 || strings.Contains(report.ErrorMessage, "env-secret") || !strings.Contains(report.ErrorMessage, "object storage operation failed") {
		t.Fatalf("unredacted terminal report: %+v", report)
	}
}

func artifactTestReference() taskpayload.ArtifactReference {
	return taskpayload.ArtifactReference{StorageID: "test-store", Fingerprint: strings.Repeat("a", 64)}
}
func artifactTestContentReference(content string) taskpayload.ArtifactReference {
	ref := artifactTestReference()
	size := int64(len(content))
	ref.Size, ref.SHA256 = &size, fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	return ref
}

func artifactTestReferences(uris map[string]string, objects ...map[string][]byte) map[string]taskpayload.ArtifactReference {
	refs := make(map[string]taskpayload.ArtifactReference, len(uris))
	for name, key := range uris {
		refs[name] = artifactTestReference()
		if len(objects) != 0 {
			if content, exists := objects[0][key]; exists {
				refs[name] = artifactTestContentReference(string(content))
			}
		}
	}
	return refs
}

func TestArtifactReferencesFailClosedAndExpiry(t *testing.T) {
	now := time.Now()
	uris := map[string]string{"build/file": "artifacts/run/job/step/hash/file"}
	reference := artifactTestReference()
	for _, test := range []struct {
		name    string
		refs    map[string]taskpayload.ArtifactReference
		current taskpayload.ArtifactReference
	}{
		{name: "legacy_missing", current: reference},
		{name: "current_missing", refs: artifactTestReferences(uris)},
		{name: "different_store", refs: map[string]taskpayload.ArtifactReference{"build/file": {StorageID: "other", Fingerprint: reference.Fingerprint}}, current: reference},
		{name: "changed_location", refs: map[string]taskpayload.ArtifactReference{"build/file": {StorageID: reference.StorageID, Fingerprint: strings.Repeat("b", 64)}}, current: reference},
		{name: "expired", refs: map[string]taskpayload.ArtifactReference{"build/file": {StorageID: reference.StorageID, Fingerprint: reference.Fingerprint, ExpiresAt: now.Unix()}}, current: reference},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateArtifactReferences(uris, test.refs, test.current, now); err == nil {
				t.Fatal("unsafe artifact reference accepted")
			}
		})
	}
	if err := validateArtifactReferences(uris, map[string]taskpayload.ArtifactReference{"build/file": artifactTestContentReference("bytes")}, reference, now); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteArtifactSnapshotsAndPerStepIsolation(t *testing.T) {
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "output.txt", "first")
	st := &memoryArtifactStorage{}
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st, reference: artifactTestReference()}
	first := artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"output.txt"}, "expireIn": "1d"})
	first.StepRunID = "step-one"
	if _, err := producer.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstKey := producer.uploaded["output.txt"]
	artifactTestWrite(t, workspace, "output.txt", "second")
	second := artifactTestRequest(workspace, "upload", map[string]any{"path": "output.txt"})
	second.StepRunID = "step-two"
	if _, err := producer.Execute(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(producer.results) != 2 || producer.results[0].ExpiresAt <= time.Now().Unix() || producer.results[1].ExpiresAt != 0 {
		t.Fatalf("file metadata missing: %+v", producer.results)
	}
	if firstKey == producer.uploaded["output.txt"] || string(st.objects[firstKey]) != "first" {
		t.Fatal("later step overwrote immutable earlier artifact")
	}
	if producer.results[0].Size != 5 || producer.results[0].Sha256 != fmt.Sprintf("%x", sha256.Sum256([]byte("first"))) {
		t.Fatal("snapshot metadata does not match uploaded bytes")
	}
	// 上传源被执行器更改时，已经固定的快照仍与摘要一致
	st.upload = func(ctx context.Context, key string, reader io.ReadSeeker, size int64) error {
		artifactTestWrite(t, workspace, "output.txt", "mutated-after-snapshot")
		content, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if string(content) != "second" || size != 6 || !strings.Contains(key, fmt.Sprintf("%x", sha256.Sum256(content))) {
			t.Fatal("upload read a mutable source")
		}
		return nil
	}
	second.StepRunID = "step-three"
	if _, err := producer.Execute(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteArtifactStorageErrorsDoNotExposeCredentials(t *testing.T) {
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "file", "bytes")
	secretError := fmt.Errorf("https://key:secret@storage.invalid/object?X-Amz-Credential=private")
	st := &memoryArtifactStorage{upload: func(context.Context, string, io.ReadSeeker, int64) error { return secretError }}
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job"}, storage: st, reference: artifactTestReference()}
	result, err := producer.Execute(context.Background(), artifactTestRequest(workspace, "upload", map[string]any{"path": "file", "when": "always"}))
	if err == nil || result.Success || strings.Contains(err.Error(), "secret") || strings.Contains(result.Error, "private") {
		t.Fatalf("storage credentials leaked: %+v %v", result, err)
	}
	if len(producer.results) != 0 {
		t.Fatal("failed upload registered")
	}
}

type countedArtifactReader struct {
	io.Reader
	bytesRead int
	closeErr  error
}

func (r *countedArtifactReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += n
	return n, err
}
func (r *countedArtifactReader) Close() error { return r.closeErr }

func TestArtifactDownloadVerifiesRegisteredContentBeforeReplacement(t *testing.T) {
	for _, test := range []struct {
		name, content string
		mutate        func(*taskpayload.ArtifactReference)
		closeErr      error
		wantError     string
		wantRead      int
	}{
		{name: "valid", content: "replacement", wantRead: len("replacement")},
		{name: "empty", content: "", wantRead: 0},
		{name: "wrong_digest", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { ref.SHA256 = strings.Repeat("0", 64) }, wantError: "SHA256 does not match", wantRead: len("replacement")},
		{name: "short", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { *ref.Size += 1 }, wantError: "size does not match", wantRead: len("replacement")},
		{name: "long", content: strings.Repeat("x", 1<<20), mutate: func(ref *taskpayload.ArtifactReference) { *ref.Size = 5 }, wantError: "exceeds registered size", wantRead: 6},
		{name: "unexpected_nonempty", content: "bytes", mutate: func(ref *taskpayload.ArtifactReference) { *ref.Size = 0 }, wantError: "exceeds registered size", wantRead: 1},
		{name: "missing_size", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { ref.Size = nil }, wantError: "explicit non-negative size", wantRead: 0},
		{name: "missing_digest", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { ref.SHA256 = "" }, wantError: "SHA256", wantRead: 0},
		{name: "invalid_digest", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { ref.SHA256 = strings.Repeat("Z", 64) }, wantError: "SHA256", wantRead: 0},
		{name: "negative_size", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { *ref.Size = -1 }, wantError: "explicit non-negative size", wantRead: 0},
		{name: "max_size_no_overflow", content: "replacement", mutate: func(ref *taskpayload.ArtifactReference) { *ref.Size = 1<<63 - 1 }, wantError: "size does not match", wantRead: len("replacement")},
		{name: "close_error", content: "replacement", closeErr: errors.New("secret provider error"), wantError: "object storage operation failed", wantRead: len("replacement")},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			artifactTestWrite(t, workspace, "existing", "original")
			root, err := openArtifactWorkspace(context.Background(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			reference := artifactTestContentReference(test.content)
			if test.mutate != nil {
				test.mutate(&reference)
			}
			reader := &countedArtifactReader{Reader: strings.NewReader(test.content), closeErr: test.closeErr}
			st := &memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) { return reader, nil }}
			err = downloadArtifactFile(context.Background(), root, "existing", "registered-object", reference, st)
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("download error = %v; want %q", err, test.wantError)
			}
			want := test.content
			if test.wantError != "" {
				want = "original"
			}
			if got := artifactTestRead(t, workspace, "existing"); got != want {
				t.Fatalf("destination = %q; want %q", got, want)
			}
			if reader.bytesRead != test.wantRead {
				t.Fatalf("read %d bytes; want %d", reader.bytesRead, test.wantRead)
			}
			if reference.ValidateIntegrity() != nil && st.downloads != 0 {
				t.Fatal("invalid metadata started a storage download")
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
				t.Fatalf("temporary file leaked: %v %v", entries, err)
			}
		})
	}
}

func TestArtifactPrefetchAndBuiltinRejectLegacyMetadata(t *testing.T) {
	uris := map[string]string{"build/file": "registered-object"}
	for _, reference := range []taskpayload.ArtifactReference{{}, artifactTestReference()} {
		for _, builtin := range []bool{false, true} {
			workspace := t.TempDir()
			artifactTestWrite(t, workspace, "file", "original")
			artifactTestWrite(t, workspace, ".artifacts/build/file", "prefetched-original")
			st := &memoryArtifactStorage{objects: map[string][]byte{"registered-object": []byte("unverified")}}
			refs := map[string]taskpayload.ArtifactReference{"build/file": reference}
			var err error
			if builtin {
				consumer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{JobName: "consume", ArtifactURIs: uris, ArtifactReferences: refs}, storage: st, reference: artifactTestReference()}
				_, err = consumer.Execute(context.Background(), artifactTestRequest(workspace, "download", map[string]any{"job": "build"}))
			} else {
				err = handleArtifactDownload(context.Background(), uris, refs, workspace, st)
			}
			if err == nil || st.downloads != 0 {
				t.Fatalf("legacy metadata allowed a download: builtin=%v error=%v downloads=%d", builtin, err, st.downloads)
			}
			if artifactTestRead(t, workspace, "file") != "original" || artifactTestRead(t, workspace, ".artifacts/build/file") != "prefetched-original" {
				t.Fatal("legacy metadata changed existing files")
			}
		}
	}
}

func TestArtifactBuiltinOwnUploadsRetainIntegrityMetadata(t *testing.T) {
	workspace := t.TempDir()
	artifactTestWrite(t, workspace, "empty", "")
	artifactTestWrite(t, workspace, "file", "uploaded")
	st := &memoryArtifactStorage{}
	producer := &artifactExecutor{job: &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "job", JobName: "build"}, storage: st, reference: artifactTestReference()}
	if _, err := producer.Execute(context.Background(), artifactTestRequest(workspace, "upload", map[string]any{"paths": []string{"empty", "file"}})); err != nil {
		t.Fatal(err)
	}
	artifactTestWrite(t, workspace, "file", "changed")
	if _, err := producer.Execute(context.Background(), artifactTestRequest(workspace, "download", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if artifactTestRead(t, workspace, "file") != "uploaded" || artifactTestRead(t, workspace, "empty") != "" {
		t.Fatal("own uploaded content was not restored")
	}
	st.objects[producer.uploaded["file"]] = []byte("modified")
	if _, err := producer.Execute(context.Background(), artifactTestRequest(workspace, "download", map[string]any{"path": "file"})); err == nil {
		t.Fatal("own uploaded metadata did not detect a same-length object replacement")
	}
	if artifactTestRead(t, workspace, "file") != "uploaded" {
		t.Fatal("corrupt uploaded object replaced existing content")
	}
}

type artifactCloseCallbackReader struct {
	io.Reader
	close func() error
}

func (r *artifactCloseCallbackReader) Close() error { return r.close() }

func TestArtifactDownloadRejectsStagingSubstitutionOnStreamClose(t *testing.T) {
	for _, replacement := range []string{"regular_file", "symlink", "same_inode_content", "same_inode_truncate", "same_inode_append", "directory", "directory_symlink"} {
		t.Run(replacement, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			artifactTestWrite(t, workspace, "destination", "original destination")
			artifactTestWrite(t, outside, "content", "outside content")
			root, err := openArtifactWorkspace(context.Background(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			var stagingPath string
			closes := 0
			reader := &artifactCloseCallbackReader{Reader: strings.NewReader("verified"), close: func() error {
				closes++
				entries, err := os.ReadDir(workspace)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".arcentra-artifact-") {
						stagingPath = filepath.Join(workspace, entry.Name())
						break
					}
				}
				if stagingPath == "" {
					return errors.New("no staging directory available for substitution")
				}
				temporary := filepath.Join(stagingPath, "content")
				directoryInfo, err := os.Stat(stagingPath)
				if err != nil || directoryInfo.Mode().Perm()&0o077 != 0 {
					t.Fatalf("staging directory is not private: %v %v", directoryInfo, err)
				}
				fileInfo, err := os.Stat(temporary)
				if err != nil || fileInfo.Mode().Perm()&0o077 != 0 {
					t.Fatalf("staging file is not private: %v %v", fileInfo, err)
				}
				switch replacement {
				case "regular_file", "symlink":
					if err := os.Remove(temporary); err != nil {
						return err
					}
					if replacement == "symlink" {
						return os.Symlink(filepath.Join(outside, "content"), temporary)
					}
					return os.WriteFile(temporary, []byte("tampered"), 0o600)
				case "same_inode_content":
					return os.WriteFile(temporary, []byte("tampered"), 0o600)
				case "same_inode_truncate":
					return os.WriteFile(temporary, []byte("short"), 0o600)
				case "same_inode_append":
					return os.WriteFile(temporary, []byte("verified-extra"), 0o600)
				case "directory", "directory_symlink":
					if err := os.Rename(stagingPath, filepath.Join(workspace, "moved-staging")); err != nil {
						return err
					}
					if replacement == "directory_symlink" {
						return os.Symlink(outside, stagingPath)
					}
					if err := os.Mkdir(stagingPath, 0o700); err != nil {
						return err
					}
					return os.WriteFile(temporary, []byte("replacement directory content"), 0o600)
				}
				return nil
			}}
			st := &memoryArtifactStorage{download: func(context.Context, string) (io.ReadCloser, error) { return reader, nil }}
			if err := downloadArtifactFile(context.Background(), root, "destination", "object", artifactTestContentReference("verified"), st); err == nil {
				t.Fatal("staging substitution reported successful integrity verification")
			}
			if closes != 1 {
				t.Fatalf("storage stream closed %d times", closes)
			}
			if artifactTestRead(t, workspace, "destination") != "original destination" || artifactTestRead(t, outside, "content") != "outside content" {
				t.Fatal("staging substitution changed the destination or external data")
			}
			if strings.HasPrefix(replacement, "directory") {
				// 恶意同 UID 可将整个目录改名到未知路径；固定句柄仍清掉原暂存内容
				entries, err := os.ReadDir(filepath.Join(workspace, "moved-staging"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("original staging content was not removed: %v %v", entries, err)
				}
			}
			if replacement == "directory" {
				// 不递归清理已由其他目录取代的路径，避免删除非本次下载拥有的数据
				data, err := os.ReadFile(filepath.Join(stagingPath, "content"))
				if err != nil || string(data) != "replacement directory content" {
					t.Fatalf("cleanup modified an unrelated replacement directory: %q %v", data, err)
				}
			} else if _, err := os.Lstat(stagingPath); !os.IsNotExist(err) {
				t.Fatalf("staging file or symlink leaked: %v", err)
			}
		})
	}
}
