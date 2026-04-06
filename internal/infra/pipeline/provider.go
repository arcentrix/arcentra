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

package pipeline

import (
	"github.com/arcentrix/arcentra/internal/infra/pipeline/builtin"
	pipelinepkg "github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	ProvideBuiltinManager,
)

func ProvideBuiltinManager(logger *log.Logger) pipelinepkg.IBuiltinManager {
	return builtin.NewManager(log.Logger{SugaredLogger: logger.SugaredLogger})
}
