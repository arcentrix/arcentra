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

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/bytedance/sonic"
)

const effectiveActionsCacheTTL = 5 * time.Minute

func buildEffectiveActionsCacheKey(subject Subject, scopeType, scopeID string) string {
	return fmt.Sprintf("authz:eff:%s:%s:%s", subject.UserID, scopeType, scopeID)
}

func getCachedActions(ctx context.Context, ch cache.ICache, key string) ([]string, bool) {
	if ch == nil {
		return nil, false
	}
	raw, err := ch.Get(ctx, key).Result()
	if err != nil || raw == "" {
		return nil, false
	}
	var actions []string
	if err = sonic.UnmarshalString(raw, &actions); err != nil {
		return nil, false
	}
	return actions, true
}

func setCachedActions(ctx context.Context, ch cache.ICache, key string, actions []string) {
	if ch == nil {
		return
	}
	bs, err := sonic.Marshal(actions)
	if err != nil {
		return
	}
	_ = ch.Set(ctx, key, string(bs), effectiveActionsCacheTTL)
}
