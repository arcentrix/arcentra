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

package authz

import "context"

// Subject 表示鉴权主体
type Subject struct {
	UserID  string
	TeamIDs []string
	IsAgent bool
}

// ResourceRef 表示待鉴权资源
type ResourceRef struct {
	Type      string
	ID        string
	OrgID     string
	ProjectID string
	TeamID    string
}

// IAuthorizer 定义统一鉴权接口
type IAuthorizer interface {
	Check(ctx context.Context, subject Subject, action string, resource ResourceRef) (bool, error)
	CheckAny(ctx context.Context, subject Subject, actions []string, resource ResourceRef) (bool, error)
	EffectiveActions(ctx context.Context, subject Subject, scopeType, scopeID string) ([]string, error)
	Reload(ctx context.Context) error
	InvalidateSubjectCache(ctx context.Context, subject Subject, scopeType, scopeID string) error
}
