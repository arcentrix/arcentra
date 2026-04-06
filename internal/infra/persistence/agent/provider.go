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
	agentcase "github.com/arcentrix/arcentra/internal/case/agent"
	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	NewAgentRepo,
	wire.Bind(new(domain.IAgentRepository), new(*AgentRepo)),
	NewStorageRepo,
	wire.Bind(new(domain.IStorageRepository), new(*StorageRepo)),
	NewAgentSecretProvider,
	wire.Bind(new(agentcase.IAgentSecretProvider), new(*AgentSecretProvider)),
)
