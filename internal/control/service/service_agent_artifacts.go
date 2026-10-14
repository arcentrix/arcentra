// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"encoding/hex"
	"path"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type agentJobStatusRepository interface {
	PatchReportedStatus(context.Context, *model.JobRun, map[string]any) error
	PatchReportedStatusAndArtifacts(context.Context, *model.JobRun, map[string]any, []*model.ProjectArtifact) error
}

func patchAgentJobStatus(ctx context.Context, repository any, expected *model.JobRun, updates map[string]any, artifacts []*model.ProjectArtifact) error {
	writer, ok := repository.(agentJobStatusRepository)
	if !ok {
		return status.Error(codes.Unimplemented, "atomic status and artifact repository unavailable")
	}
	if len(artifacts) == 0 {
		return writer.PatchReportedStatus(ctx, expected, updates)
	}
	return writer.PatchReportedStatusAndArtifacts(ctx, expected, updates, artifacts)
}

func (a *AgentServiceImpl) prepareJobArtifacts(ctx context.Context, job *model.JobRun, req *agentv1.ReportJobRunStatusRequest, updates map[string]any) ([]*model.ProjectArtifact, error) {
	if job.RetryPlan != "" && (len(req.Artifacts) > 0 || len(req.ArtifactUris) > 0) && (req.Status != model.JobRunStatusSuccess || len(req.Artifacts) == 0) {
		return nil, status.Error(codes.FailedPrecondition, "generation artifacts require complete final-success publication")
	}

	if len(req.Artifacts) > 1000 || len(req.ArtifactUris) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "artifact report exceeds 1000 files")
	}
	if req.Status == model.JobRunStatusRunning {
		if len(req.Artifacts) != 0 || len(req.ArtifactUris) != 0 {
			return nil, status.Error(codes.InvalidArgument, "artifacts require a terminal job report")
		}
		if req.ArtifactStorageId == "" && req.ArtifactStorageFingerprint == "" {
			return nil, nil
		}
		if artifactutil.Identity(req.ArtifactStorageId) != nil || !validArtifactDigest(req.ArtifactStorageFingerprint) {
			return nil, status.Error(codes.InvalidArgument, "invalid artifact storage identity")
		}
		if a.storageRepo == nil {
			return nil, status.Error(codes.FailedPrecondition, "artifact storage repository unavailable")
		}
		config, err := a.storageRepo.Get(ctx, req.ArtifactStorageId)
		if err != nil || config == nil || config.IsEnabled != 1 {
			return nil, status.Error(codes.FailedPrecondition, "artifact storage is unavailable")
		}
		var detail model.StorageConfigDetail
		if sonic.Unmarshal(config.Config, &detail) != nil {
			return nil, status.Error(codes.FailedPrecondition, "artifact storage configuration is invalid")
		}
		fingerprint := artifactutil.StorageFingerprint(config.StorageType, detail.Endpoint, detail.Bucket, detail.Region, detail.BasePath, detail.UseTLS)
		if fingerprint != req.ArtifactStorageFingerprint {
			return nil, status.Error(codes.FailedPrecondition, "artifact storage configuration changed; re-register the agent")
		}
		updates["artifact_storage_id"], updates["artifact_storage_fingerprint"], updates["artifact_storage_type"] = config.StorageID, fingerprint, config.StorageType
		return nil, nil
	}
	if len(req.Artifacts) == 0 {
		// 旧 Agent 仍可报告执行完成，但不能让缺少来源绑定的对象成为可下载制品
		if len(req.ArtifactUris) != 0 {
			if job.ArtifactStorageID != "" || req.ArtifactStorageId != "" || req.ArtifactStorageFingerprint != "" {
				return nil, status.Error(codes.FailedPrecondition, "artifact file metadata is required")
			}
			for name, key := range req.ArtifactUris {
				if artifactutil.Name(name) != nil || key != path.Join("artifacts", job.PipelineRunID, job.JobRunID, name) {
					return nil, status.Error(codes.InvalidArgument, "legacy artifact key is outside its execution namespace")
				}
			}
			encoded, err := sonic.Marshal(req.ArtifactUris)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "invalid legacy artifact keys")
			}
			updates["artifact_uris"] = string(encoded)
		}
		if model.IsJobRunTerminal(job.Status) && job.ArtifactReferences != "" && job.ArtifactReferences != "{}" {
			return nil, status.Error(codes.FailedPrecondition, "completed artifact metadata does not match")
		}
		return nil, nil
	}
	if job.ArtifactStorageID == "" || job.ArtifactStorageFingerprint == "" || req.ArtifactStorageId != job.ArtifactStorageID || req.ArtifactStorageFingerprint != job.ArtifactStorageFingerprint {
		return nil, status.Error(codes.PermissionDenied, "artifact storage identity does not match the execution claim")
	}
	artifacts := make([]*model.ProjectArtifact, 0, len(req.Artifacts))
	uris := make(map[string]string, len(req.Artifacts))
	refs := make(map[string]taskqueue.ArtifactReference, len(req.Artifacts))
	seen := make(map[string]bool, len(req.Artifacts))
	for _, file := range req.Artifacts {
		if file == nil || artifactutil.Identity(file.StepRunId) != nil || artifactutil.Name(file.Name) != nil || len(file.Name) > 512 || file.Size < 0 || !validArtifactDigest(file.Sha256) || file.MediaType != "application/octet-stream" || file.ExpiresAt < 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid artifact file metadata")
		}
		key := path.Join("artifacts", job.PipelineRunID, job.JobRunID, file.StepRunId, file.Sha256, file.Name)
		if file.StorageKey != key {
			return nil, status.Error(codes.InvalidArgument, "artifact object key is outside its execution namespace")
		}
		identity := file.StepRunId + "\x00" + file.Name
		if seen[identity] {
			return nil, status.Error(codes.InvalidArgument, "duplicate artifact file metadata")
		}
		seen[identity] = true
		item := &model.ProjectArtifact{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, StepRunID: file.StepRunId, Name: file.Name, StorageType: job.ArtifactStorageType, StorageID: job.ArtifactStorageID, StorageFingerprint: job.ArtifactStorageFingerprint, StoragePath: key, Size: file.Size, SHA256: file.Sha256, MediaType: file.MediaType}
		if file.ExpiresAt > 0 {
			expires := time.Unix(file.ExpiresAt, 0).UTC()
			item.ExpiresAt = &expires
		}
		artifacts = append(artifacts, item)
		uris[file.Name] = key
		size := file.Size
		refs[file.Name] = taskqueue.ArtifactReference{StorageID: job.ArtifactStorageID, Fingerprint: job.ArtifactStorageFingerprint, ExpiresAt: file.ExpiresAt, SHA256: file.Sha256, Size: &size}
	}
	if len(req.ArtifactUris) != len(uris) {
		return nil, status.Error(codes.InvalidArgument, "artifact keys do not match file metadata")
	}
	for name, key := range uris {
		if req.ArtifactUris[name] != key {
			return nil, status.Error(codes.InvalidArgument, "artifact keys do not match file metadata")
		}
	}
	encoded, err := sonic.Marshal(uris)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid artifact keys")
	}
	encodedRefs, err := sonic.Marshal(refs)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid artifact references")
	}
	updates["artifact_uris"], updates["artifact_references"] = string(encoded), string(encodedRefs)
	return artifacts, nil
}

func validArtifactDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
