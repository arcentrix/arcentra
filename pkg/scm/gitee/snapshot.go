// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package gitee

import (
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"net/http"
)

type SnapshotReader = pollsnapshot.V3Reader
type SnapshotRequest = pollsnapshot.Request
type SnapshotResult = pollsnapshot.Result
type SnapshotLimits = pollsnapshot.Limits

const SnapshotVersion = pollsnapshot.Version3

func DefaultSnapshotLimits() SnapshotLimits { return pollsnapshot.DefaultLimits() }
func NewSnapshotReader(cfg scm.ProviderConfig, client *http.Client, limits SnapshotLimits) (*SnapshotReader, error) {
	if cfg.Kind != scm.ProviderKindGitee {
		return nil, &pollsnapshot.Error{Reason: pollsnapshot.ReasonConfiguration}
	}
	return pollsnapshot.NewV3Reader(cfg, client, limits)
}
