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

package agent

import (
	"sync/atomic"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// StorageHolder provides thread-safe access to a lazily-initialized IStorage
// instance. The control plane delivers StorageConfig via RegisterResponse;
// the agent calls SetFromProto to build the storage client.
type StorageHolder struct {
	st atomic.Pointer[storageSnapshot]
}

// NewStorageHolder creates an empty holder. Get() returns nil until configured.
func NewStorageHolder() *StorageHolder {
	return &StorageHolder{}
}

// Get returns the current IStorage, or nil if not yet configured.
func (h *StorageHolder) Get() storage.IStorage {
	if p := h.st.Load(); p != nil {
		return p.storage
	}
	return nil
}

// SetFromProto initialises or replaces the IStorage from a proto StorageConfig.
func (h *StorageHolder) SetFromProto(cfg *agentv1.StorageConfig) {
	if cfg == nil || cfg.GetProvider() == "" {
		return
	}
	st, err := storage.NewStorage(&storage.Storage{
		Provider:  cfg.GetProvider(),
		Endpoint:  cfg.GetEndpoint(),
		Bucket:    cfg.GetBucket(),
		Region:    cfg.GetRegion(),
		AccessKey: cfg.GetAccessKey(),
		SecretKey: cfg.GetSecretKey(),
		BasePath:  cfg.GetBasePath(),
		UseTLS:    cfg.GetUseSsl(),
	})
	if err != nil {
		log.Warn("agent: failed to init storage from control-plane config")
		return
	}
	h.st.Store(&storageSnapshot{storage: st, reference: taskqueue.ArtifactReference{StorageID: cfg.GetStorageId(), Fingerprint: cfg.GetStorageFingerprint()}})
	log.Infow("agent: object storage initialised from control-plane",
		"provider", cfg.GetProvider(), "storageID", cfg.GetStorageId())
}

type storageSnapshot struct {
	storage   storage.IStorage
	reference taskqueue.ArtifactReference
}

// Snapshot 原子读取存储客户端及不包含凭据的位置身份
func (h *StorageHolder) Snapshot() (storage.IStorage, taskqueue.ArtifactReference) {
	if p := h.st.Load(); p != nil {
		return p.storage, p.reference
	}
	return nil, taskqueue.ArtifactReference{}
}
