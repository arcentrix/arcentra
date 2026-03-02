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

// Package spec exposes pipeline schema from proto as single source of truth.
// DO NOT define manual schema structs here.
package spec

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type Pipeline = pipelinev1.Spec
type Runtime = pipelinev1.Runtime
type Resources = pipelinev1.Resources
type Job = pipelinev1.Job
type Retry = pipelinev1.Retry
type Step = pipelinev1.Step
type Source = pipelinev1.Source
type SourceAuth = pipelinev1.SourceAuth
type Approval = pipelinev1.Approval
type Target = pipelinev1.Target
type Notify = pipelinev1.Notify
type NotifyItem = pipelinev1.NotifyItem
type Trigger = pipelinev1.Trigger
type AgentSelector = pipelinev1.AgentSelector
type LabelExpression = pipelinev1.LabelExpression

func StructAsMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	return s.AsMap()
}

func MapAsStruct(m map[string]any) *structpb.Struct {
	if len(m) == 0 {
		v, _ := structpb.NewStruct(map[string]any{})
		return v
	}
	v, err := structpb.NewStruct(m)
	if err != nil {
		fallback, _ := structpb.NewStruct(map[string]any{})
		return fallback
	}
	return v
}
