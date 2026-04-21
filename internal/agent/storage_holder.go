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
)

// StorageHolder provides thread-safe access to a lazily-initialized IStorage
// instance. The control plane delivers StorageConfig via RegisterResponse;
// the agent calls SetFromProto to build the storage client.
type StorageHolder struct {
	st atomic.Pointer[storage.IStorage]
}

// NewStorageHolder creates an empty holder. Get() returns nil until configured.
func NewStorageHolder() *StorageHolder {
	return &StorageHolder{}
}

// Get returns the current IStorage, or nil if not yet configured.
func (h *StorageHolder) Get() storage.IStorage {
	if p := h.st.Load(); p != nil {
		return *p
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
		log.Warnw("agent: failed to init storage from control-plane config", "error", err)
		return
	}
	h.st.Store(&st)
	log.Infow("agent: object storage initialised from control-plane",
		"provider", cfg.GetProvider(), "endpoint", cfg.GetEndpoint(), "bucket", cfg.GetBucket())
}
