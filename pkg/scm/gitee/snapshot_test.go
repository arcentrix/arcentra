// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package gitee

import (
	"github.com/arcentrix/arcentra/pkg/scm"
	"testing"
)

func TestSnapshotV3ProviderBinding(t *testing.T) {
	if SnapshotVersion != 3 {
		t.Fatal("wrong version")
	}
	if _, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitee, BaseURL: "https://gitee.com"}, nil, SnapshotLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitHub, BaseURL: "https://gitee.com"}, nil, SnapshotLimits{}); err == nil {
		t.Fatal("provider confusion accepted")
	}
	if _, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitee, BaseURL: "https://private.example"}, nil, SnapshotLimits{}); err == nil {
		t.Fatal("private service accepted")
	}
}
