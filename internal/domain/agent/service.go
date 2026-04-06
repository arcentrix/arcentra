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

package agent

import (
	"encoding/json"
	"fmt"
)

// AgentDomainService contains pure domain logic that doesn't belong to a single entity.
type AgentDomainService struct{}

func NewAgentDomainService() *AgentDomainService {
	return &AgentDomainService{}
}

// ValidateStatusTransition checks whether transitioning from one status to
// another is allowed by domain rules.
func (s *AgentDomainService) ValidateStatusTransition(from, to AgentStatus) error {
	if !from.IsValid() {
		return fmt.Errorf("invalid source status: %d", from)
	}
	if !to.IsValid() {
		return fmt.Errorf("invalid target status: %d", to)
	}

	allowed := map[AgentStatus][]AgentStatus{
		AgentStatusUnknown: {AgentStatusOnline, AgentStatusOffline},
		AgentStatusOnline:  {AgentStatusOffline, AgentStatusBusy, AgentStatusIdle},
		AgentStatusOffline: {AgentStatusOnline},
		AgentStatusBusy:    {AgentStatusOnline, AgentStatusIdle, AgentStatusOffline},
		AgentStatusIdle:    {AgentStatusOnline, AgentStatusBusy, AgentStatusOffline},
	}

	targets, ok := allowed[from]
	if !ok {
		return fmt.Errorf("no transitions defined from status %s", from)
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("transition from %s to %s is not allowed", from, to)
}

// ValidateStorageConfig validates the configuration JSON for the given storage type.
func (s *AgentDomainService) ValidateStorageConfig(storageType StorageType, configJSON json.RawMessage) error {
	if !storageType.IsValid() {
		return fmt.Errorf("unsupported storage type: %s", storageType)
	}

	var detail StorageConfigDetail
	if err := json.Unmarshal(configJSON, &detail); err != nil {
		return fmt.Errorf("invalid config JSON: %w", err)
	}

	if detail.Endpoint == "" && storageType != StorageTypeGCS {
		return fmt.Errorf("%s endpoint is required", storageType)
	}
	if detail.AccessKey == "" {
		return fmt.Errorf("%s access key is required", storageType)
	}
	if storageType != StorageTypeGCS && detail.SecretKey == "" {
		return fmt.Errorf("%s secret key is required", storageType)
	}
	if detail.Bucket == "" {
		return fmt.Errorf("%s bucket is required", storageType)
	}

	return nil
}
