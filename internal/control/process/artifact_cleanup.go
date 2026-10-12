package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/artifact"
)

// ArtifactStorageResolver 按持久化绑定解析制品原始对象存储，不得选择当前默认存储
type ArtifactStorageResolver func(context.Context, *model.ProjectArtifact) (storage.IStorage, error)

// ArtifactCleaner 提供显式、项目限定的过期制品清理，不在后台自动执行
type ArtifactCleaner struct {
	Artifacts      repo.IArtifactRepository
	Pipelines      repo.IPipelineRepository
	ResolveStorage ArtifactStorageResolver
}

// ArtifactCleanupOptions 要求明确项目及过期截止时间，默认仅预览
type ArtifactCleanupOptions struct {
	ProjectID        string
	RunID            string
	Before           time.Time
	Limit            int
	Apply            bool
	ExecutorsStopped bool
}

// ArtifactCleanupItem 描述一条计划或执行结果，不泄漏内部存储键与凭据
type ArtifactCleanupItem struct {
	ArtifactID string `json:"artifactId"`
	RunID      string `json:"runId"`
	Name       string `json:"name"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
}

// ArtifactCleanupResult 返回有界批次的处理汇总
type ArtifactCleanupResult struct {
	DryRun  bool                  `json:"dryRun"`
	Items   []ArtifactCleanupItem `json:"items"`
	Deleted int                   `json:"deleted"`
	Skipped int                   `json:"skipped"`
	Failed  int                   `json:"failed"`
}

// Prune 只清理已结束运行的已过期制品，对象删除失败时必须保留登记记录
func (ac *ArtifactCleaner) Prune(ctx context.Context, options ArtifactCleanupOptions) (*ArtifactCleanupResult, error) {
	if ac == nil || ac.Artifacts == nil || ac.Pipelines == nil {
		return nil, fmt.Errorf("artifact cleanup repositories are required")
	}
	if err := artifact.Identity(options.ProjectID); err != nil {
		return nil, fmt.Errorf("explicit project ID is required")
	}
	if options.RunID != "" {
		if err := artifact.Identity(options.RunID); err != nil {
			return nil, err
		}
	}
	if options.Before.IsZero() {
		options.Before = time.Now().UTC()
	}
	if options.Before.After(time.Now()) {
		return nil, fmt.Errorf("cleanup cutoff cannot be in the future")
	}
	if options.Limit == 0 {
		options.Limit = 100
	}
	if options.Limit < 1 || options.Limit > 500 {
		return nil, fmt.Errorf("cleanup limit must be between 1 and 500")
	}
	if options.Apply && !options.ExecutorsStopped {
		return nil, fmt.Errorf("--apply requires all controllers/agents stopped and no artifact consumers active")
	}
	entries, err := ac.Artifacts.ListExpired(ctx, options.ProjectID, options.RunID, options.Before, options.Limit)
	if err != nil {
		return nil, err
	}
	result := &ArtifactCleanupResult{DryRun: !options.Apply, Items: make([]ArtifactCleanupItem, 0, len(entries))}
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry := &entries[i]
		item := ArtifactCleanupItem{ArtifactID: entry.ArtifactID, RunID: entry.PipelineRunID, Name: entry.Name}
		if entry.ProjectID != options.ProjectID || options.RunID != "" && entry.PipelineRunID != options.RunID || entry.ExpiresAt == nil || entry.ExpiresAt.After(options.Before) {
			return result, fmt.Errorf("artifact cleanup repository returned an out-of-scope record")
		}
		run, err := ac.Pipelines.GetRun(ctx, entry.PipelineRunID)
		if err != nil {
			return result, err
		}
		if run != nil && run.RunID == entry.PipelineRunID && run.PipelineID == entry.PipelineID {
			pipeline, err := ac.Pipelines.Get(ctx, entry.PipelineID)
			if err != nil {
				return result, err
			}
			if pipeline == nil || pipeline.PipelineID != entry.PipelineID || pipeline.ProjectID != options.ProjectID || pipeline.IsDeleted != 0 {
				item.Outcome = "skipped"
				item.Reason = "pipeline project ownership is unavailable"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
		}
		if run == nil || run.RunID != entry.PipelineRunID || run.PipelineID != entry.PipelineID {
			item.Outcome = "skipped"
			item.Reason = "run ownership is unavailable"
			result.Skipped++
		} else if run.Status != model.PipelineStatusSuccess && run.Status != model.PipelineStatusFailed && run.Status != model.PipelineStatusCancelled {
			item.Outcome = "skipped"
			item.Reason = "run is not terminal"
			result.Skipped++
		} else if !options.Apply {
			item.Outcome = "would-delete"
		} else {
			if err := ac.deleteObject(ctx, entry); err != nil {
				item.Outcome = "failed"
				item.Reason = "object removal failed; registry retained"
				result.Failed++
			} else {
				deleted, err := ac.Artifacts.DeleteVersion(ctx, entry)
				if err != nil {
					item.Outcome = "failed"
					item.Reason = "registry removal failed; retry is safe"
					result.Failed++
				} else if !deleted {
					item.Outcome = "skipped"
					item.Reason = "registry version changed or was already removed"
					result.Skipped++
				} else {
					item.Outcome = "deleted"
					result.Deleted++
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (ac *ArtifactCleaner) deleteObject(ctx context.Context, entry *model.ProjectArtifact) error {
	if entry.ArtifactID != artifact.ID(entry.ProjectID, entry.PipelineRunID, entry.JobRunID, entry.StepRunID, entry.Name) {
		return fmt.Errorf("artifact identity mismatch")
	}
	if entry.StorageType != "local" {
		if ac.ResolveStorage == nil {
			return fmt.Errorf("artifact storage resolver is missing")
		}
		store, err := ac.ResolveStorage(ctx, entry)
		if err != nil {
			return err
		}
		if store == nil {
			return fmt.Errorf("artifact storage unavailable")
		}
		return store.Delete(ctx, entry.StoragePath)
	}
	expected, err := artifact.LocalPath(entry.ProjectID, entry.PipelineRunID, entry.JobRunID, entry.StepRunID, entry.Name)
	if err != nil || entry.StoragePath != expected {
		return fmt.Errorf("artifact local path mismatch")
	}
	if len(entry.SHA256) != 64 || strings.ToLower(entry.SHA256) != entry.SHA256 {
		return fmt.Errorf("artifact checksum is required for cleanup")
	}
	if _, err := hex.DecodeString(entry.SHA256); err != nil {
		return fmt.Errorf("artifact checksum is invalid")
	}
	root, err := artifact.OpenRoot(ctx, artifact.Root())
	if err != nil {
		return fmt.Errorf("managed artifact root unavailable: %w", err)
	}
	_ = root.Close()
	file, err := artifact.OpenLocal(ctx, artifact.Root(), expected)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != entry.Size {
		return fmt.Errorf("artifact file size mismatch")
	}
	if entry.SHA256 != "" {
		sum := sha256.New()
		if _, err := io.Copy(sum, artifactContextReader{ctx, file}); err != nil {
			return err
		}
		if hex.EncodeToString(sum.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("artifact content digest mismatch")
		}
	}
	parent, err := artifact.OpenRoot(ctx, path.Join(artifact.Root(), path.Dir(expected)))
	if err != nil {
		return err
	}
	defer parent.Close()
	current, err := parent.Lstat(path.Base(expected))
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || !current.Mode().IsRegular() {
		return fmt.Errorf("artifact changed before removal")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := parent.Remove(path.Base(expected)); err != nil {
		return err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
