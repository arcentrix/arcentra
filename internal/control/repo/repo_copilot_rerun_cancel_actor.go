// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import "context"

// CopilotRerunCancelUnclaimedAs 使用当前取消者授权，保留原始所有者作为不可变出处
// The actor is independent of input.OwnerUserID. No owner identity is supplied
// to the authorization callback on behalf of a different cancelling user.
func (r *Repositories) CopilotRerunCancelUnclaimedAs(ctx context.Context, actor string, in CopilotRerunTargetCancelInput, authorize CopilotRerunAuthorize) (*CopilotRerunTargetCancelReceipt, error) {
	return r.copilotRerunCancelUnclaimedAs(ctx, actor, in, authorize, false)
}
