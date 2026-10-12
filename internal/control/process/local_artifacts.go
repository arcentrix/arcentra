package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/google/uuid"
)

func (rc *Coordinator) recordLocalArtifacts(ctx context.Context, jobID, stepID, source string, names []string, expireIn string) error {
	if rc.engine.repos.Artifact == nil {
		return fmt.Errorf("artifact registry is not configured")
	}
	definition, err := rc.engine.repos.Pipeline.Get(ctx, rc.run.PipelineID)
	if err != nil {
		return err
	}
	if definition == nil {
		return fmt.Errorf("pipeline no longer exists")
	}
	job, err := rc.engine.repos.JobRun.GetByJobRunID(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil || job.PipelineRunID != rc.run.RunID {
		return fmt.Errorf("artifact job does not belong to run")
	}
	step, err := rc.engine.repos.StepRun.GetByStepRunID(ctx, stepID)
	if err != nil {
		return err
	}
	if step == nil || step.JobRunID != jobID || step.PipelineRunID != rc.run.RunID {
		return fmt.Errorf("artifact step does not belong to job")
	}
	// 使用持久化的步骤创建时间，重试回调不会延长保留期限
	expires, err := artifact.ParseExpiry(expireIn, step.CreatedAt)
	if err != nil {
		return err
	}
	expected := filepath.Join(rc.resolveWorkspace(), rc.spec.Namespace, rc.run.RunID, "artifacts", job.JobName)
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	expectedAbs, err := filepath.Abs(expected)
	if err != nil {
		return err
	}
	if sourceAbs != expectedAbs {
		return fmt.Errorf("artifact source is outside this job")
	}
	src, err := artifact.OpenRoot(ctx, sourceAbs)
	if err != nil {
		return err
	}
	defer src.Close()
	files, err := collectArtifactFiles(ctx, src, names)
	if err != nil {
		return err
	}
	existing, total, err := rc.engine.repos.Artifact.List(ctx, repo.ArtifactQuery{ProjectID: definition.ProjectID, RunID: rc.run.RunID, JobRunID: jobID, Limit: 500})
	if err != nil {
		return err
	}
	if total > 1000 {
		return fmt.Errorf("artifact job exceeds 1000 registered files")
	}
	if int64(len(existing)) < total {
		rest, _, err := rc.engine.repos.Artifact.List(ctx, repo.ArtifactQuery{ProjectID: definition.ProjectID, RunID: rc.run.RunID, JobRunID: jobID, Limit: 500, Offset: len(existing)})
		if err != nil {
			return err
		}
		existing = append(existing, rest...)
	}
	known := map[string]bool{}
	for _, entry := range existing {
		known[entry.ArtifactID] = true
	}
	added := int64(0)
	for _, name := range files {
		if !known[artifact.ID(definition.ProjectID, rc.run.RunID, jobID, stepID, name)] {
			added++
		}
	}
	if total+added > 1000 {
		return fmt.Errorf("artifact job exceeds 1000 registered files")
	}
	for _, name := range files {
		relative, err := artifact.LocalPath(definition.ProjectID, rc.run.RunID, jobID, stepID, name)
		if err != nil {
			return err
		}
		size, digest, err := copyManagedArtifact(ctx, sourceAbs, name, artifact.Root(), relative)
		if err != nil {
			return err
		}
		entry := &model.ProjectArtifact{ArtifactID: artifact.ID(definition.ProjectID, rc.run.RunID, jobID, stepID, name), ProjectID: definition.ProjectID, PipelineID: rc.run.PipelineID, PipelineRunID: rc.run.RunID, JobRunID: jobID, StepRunID: stepID, Name: name, StorageType: "local", StoragePath: relative, Size: size, SHA256: digest, MediaType: mime.TypeByExtension(path.Ext(name)), ExpiresAt: expires}
		if entry.MediaType == "" {
			entry.MediaType = "application/octet-stream"
		}
		if err := rc.engine.repos.Artifact.Upsert(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func collectArtifactFiles(ctx context.Context, root *os.Root, names []string) ([]string, error) {
	selected := map[string]struct{}{}
	var visit func(string, int) error
	visit = func(name string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 64 {
			return fmt.Errorf("artifact directory nesting exceeds 64 levels")
		}
		if err := artifact.Name(name); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			selected[name] = struct{}{}
			if len(selected) > 1000 {
				return fmt.Errorf("artifact upload exceeds 1000 files")
			}
			return nil
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact contains unsupported file type")
		}
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, readErr := dir.ReadDir(128)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range entries {
				if err := visit(path.Join(name, entry.Name()), depth+1); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
		return nil
	}
	for _, name := range names {
		if err := visit(name, 0); err != nil {
			return nil, err
		}
	}
	files := make([]string, 0, len(selected))
	for name := range selected {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

type artifactContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r artifactContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyManagedArtifact(ctx context.Context, sourceRoot, name, destinationRoot, relative string) (int64, string, error) {
	abs, err := filepath.Abs(destinationRoot)
	if err != nil || abs == string(filepath.Separator) {
		return 0, "", fmt.Errorf("artifact root must be a dedicated directory")
	}
	if err := artifact.Name(relative); err != nil {
		return 0, "", err
	}
	src, err := artifact.OpenLocal(ctx, sourceRoot, name)
	if err != nil {
		return 0, "", err
	}
	defer src.Close()
	before, err := src.Stat()
	if err != nil {
		return 0, "", err
	}
	parent, err := makeArtifactDirectory(ctx, filepath.Join(destinationRoot, filepath.FromSlash(path.Dir(relative))))
	if err != nil {
		return 0, "", err
	}
	defer parent.Close()
	tempName := ".upload-" + uuid.NewString()
	file, err := parent.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = file.Close(); _ = parent.Remove(tempName) }()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), artifactContextReader{ctx, src})
	if err != nil {
		return 0, "", err
	}
	after, err := src.Stat()
	if err != nil {
		return 0, "", err
	}
	if size != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return 0, "", fmt.Errorf("artifact source changed during upload")
	}
	if err = file.Sync(); err != nil {
		return 0, "", err
	}
	if err = file.Close(); err != nil {
		return 0, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	base := path.Base(relative)
	// Link 原子发布且拒绝覆盖，重复回调只能复用完全相同的已持久化文件
	if err = parent.Link(tempName, base); errors.Is(err, os.ErrExist) {
		old, openErr := artifact.OpenLocal(ctx, destinationRoot, relative)
		if openErr != nil {
			return 0, "", openErr
		}
		previous := sha256.New()
		oldSize, copyErr := io.Copy(previous, artifactContextReader{ctx, old})
		_ = old.Close()
		if copyErr != nil {
			return 0, "", copyErr
		}
		if oldSize != size || hex.EncodeToString(previous.Sum(nil)) != digest {
			return 0, "", fmt.Errorf("immutable artifact already exists with different content")
		}
	} else if err != nil {
		return 0, "", err
	}
	if err = parent.Remove(tempName); err != nil {
		return 0, "", err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return 0, "", err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return 0, "", err
	}
	return size, digest, nil
}

func makeArtifactDirectory(ctx context.Context, directory string) (*os.Root, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	current, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			_ = current.Close()
			return nil, fmt.Errorf("invalid artifact directory")
		}
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return nil, err
		}
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err = current.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				_ = current.Close()
				return nil, err
			}
			parent, openErr := current.Open(".")
			if openErr != nil {
				_ = current.Close()
				return nil, openErr
			}
			syncErr := parent.Sync()
			_ = parent.Close()
			if syncErr != nil {
				_ = current.Close()
				return nil, syncErr
			}
			info, err = current.Lstat(part)
		}
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, fmt.Errorf("artifact directory contains symlink or non-directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		actual, statErr := next.Stat(".")
		_ = current.Close()
		if statErr != nil || !os.SameFile(info, actual) {
			_ = next.Close()
			return nil, fmt.Errorf("artifact directory changed while opening")
		}
		current = next
	}
	return current, nil
}

// cleanupRunWorkspace 保留声明的制品和报告，失败的登记也不会自动销毁源输出
func cleanupRunWorkspace(workspace string, preserved ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := artifact.OpenRoot(ctx, workspace)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range preserved {
		if err := artifact.Name(filepath.ToSlash(name)); err != nil {
			return err
		}
	}
	var clean func(string) error
	clean = func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, readErr := dir.ReadDir(128)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				relative := path.Join(name, entry.Name())
				keep, ancestor := false, false
				for _, preserve := range preserved {
					preserve = filepath.ToSlash(preserve)
					keep = keep || relative == preserve
					ancestor = ancestor || strings.HasPrefix(preserve, relative+"/")
				}
				if keep {
					continue
				}
				if ancestor && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
					if err := clean(relative); err != nil {
						return err
					}
					continue
				}
				if err := root.RemoveAll(relative); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
		}
	}
	return clean(".")
}

// checkLocalArtifacts 拒绝过期、未登记或被修改的源制品，源副本保留并不授予下载权限。
func (rc *Coordinator) checkLocalArtifacts(ctx context.Context, jobName string, names []string) error {
	if rc.engine.repos.Artifact == nil {
		return fmt.Errorf("artifact registry is not configured")
	}
	definition, err := rc.engine.repos.Pipeline.Get(ctx, rc.run.PipelineID)
	if err != nil {
		return err
	}
	if definition == nil {
		return fmt.Errorf("pipeline unavailable")
	}
	jobs, err := rc.engine.repos.JobRun.ListByPipelineRunID(ctx, rc.run.RunID)
	if err != nil {
		return err
	}
	var jobID string
	for _, job := range jobs {
		if job.JobName == jobName {
			jobID = job.JobRunID
			break
		}
	}
	if jobID == "" {
		return fmt.Errorf("artifact source job unavailable")
	}
	source := filepath.Join(rc.resolveWorkspace(), rc.spec.Namespace, rc.run.RunID, "artifacts", jobName)
	root, err := artifact.OpenRoot(ctx, source)
	if err != nil {
		return err
	}
	defer root.Close()
	files, err := collectArtifactFiles(ctx, root, names)
	if err != nil {
		return err
	}
	query := repo.ArtifactQuery{ProjectID: definition.ProjectID, RunID: rc.run.RunID, JobRunID: jobID, Limit: 500}
	records, total, err := rc.engine.repos.Artifact.List(ctx, query)
	if err != nil {
		return err
	}
	if total > 1000 {
		return fmt.Errorf("artifact source exceeds validation limit")
	}
	if int64(len(records)) < total {
		query.Offset = len(records)
		rest, _, err := rc.engine.repos.Artifact.List(ctx, query)
		if err != nil {
			return err
		}
		records = append(records, rest...)
	}
	latest := map[string]model.ProjectArtifact{}
	for _, entry := range records {
		prior, ok := latest[entry.Name]
		if !ok || entry.CreatedAt.After(prior.CreatedAt) {
			latest[entry.Name] = entry
		}
	}
	for _, name := range files {
		entry, ok := latest[name]
		if !ok || entry.StorageType != "local" || entry.JobRunID != jobID || entry.PipelineRunID != rc.run.RunID || entry.ProjectID != definition.ProjectID {
			return fmt.Errorf("artifact source is not registered")
		}
		if entry.ExpiresAt != nil && !entry.ExpiresAt.After(time.Now()) {
			return fmt.Errorf("artifact source has expired")
		}
		file, err := artifact.OpenLocal(ctx, source, name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, artifactContextReader{ctx, file})
		_ = file.Close()
		if copyErr != nil {
			return copyErr
		}
		if size != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("artifact source content no longer matches registration")
		}
	}
	return nil
}
