// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

// containerAcceptanceRetryInputs 精确验证累计输入集合，不把非直接上游制品误判为额外输入
func containerAcceptanceRetryInputs(manifest *taskpayload.JobInputManifest, consumer, originalCommit, requestedKey string, registered map[string]model.ProjectArtifact) error {
	names := []string{"retry-a/seed/input.txt"}
	source := originalCommit
	switch consumer {
	case "retry-b":
	case "retry-c":
		names = append(names, "retry-b/proof/result.txt")
		source = ""
	default:
		return fmt.Errorf("unexpected retry-chain input consumer")
	}
	if manifest == nil || manifest.SourceCommit != source || len(manifest.Artifacts) != len(names) || len(registered) != len(names) {
		return fmt.Errorf("retry consumer changed its exact source or cumulative input set")
	}
	if _, err := manifest.Canonical(); err != nil {
		return fmt.Errorf("retry consumer input manifest is not canonical")
	}
	requested := 0
	keys := make(map[string]bool, len(names))
	for i, name := range names {
		input := manifest.Artifacts[i]
		row, exists := registered[name]
		if !exists || input.Name != name || row.Name != strings.SplitN(name, "/", 2)[1] || input.Key != row.StoragePath || keys[input.Key] || input.Reference.SHA256 != row.SHA256 || input.Reference.Size == nil || *input.Reference.Size != row.Size || input.Reference.StorageID != row.StorageID || input.Reference.Fingerprint != row.StorageFingerprint || row.ExpiresAt == nil || input.Reference.ExpiresAt != row.ExpiresAt.Unix() {
			return fmt.Errorf("retry input changed its exact registered name/content/storage metadata")
		}
		keys[input.Key] = true
		if input.Key == requestedKey {
			requested++
		}
	}
	if requested != 1 {
		return fmt.Errorf("retry transfer is outside the exact pinned cumulative input set")
	}
	return nil
}

func TestContainerRetryExactInputSet(t *testing.T) {
	commit := strings.Repeat("a", 40)
	fixture := func(consumer string) (*taskpayload.JobInputManifest, map[string]model.ProjectArtifact) {
		t.Helper()
		expires := time.Unix(1900000000, 0)
		registered := map[string]model.ProjectArtifact{
			"retry-a/seed/input.txt":   {Name: "seed/input.txt", StoragePath: "objects/a", Size: 14, SHA256: strings.Repeat("b", 64), StorageID: "store", StorageFingerprint: strings.Repeat("d", 64), ExpiresAt: &expires},
			"retry-b/proof/result.txt": {Name: "proof/result.txt", StoragePath: "objects/b", Size: 100, SHA256: strings.Repeat("c", 64), StorageID: "store", StorageFingerprint: strings.Repeat("d", 64), ExpiresAt: &expires},
		}
		source := ""
		if consumer == "retry-b" {
			delete(registered, "retry-b/proof/result.txt")
			source = commit
		}
		uris := make(map[string]string)
		refs := make(map[string]taskpayload.ArtifactReference)
		for name, row := range registered {
			size := row.Size
			uris[name] = row.StoragePath
			refs[name] = taskpayload.ArtifactReference{SHA256: row.SHA256, Size: &size, StorageID: row.StorageID, Fingerprint: row.StorageFingerprint, ExpiresAt: row.ExpiresAt.Unix()}
		}
		manifest, err := taskpayload.NewJobInputManifest(source, uris, refs)
		if err != nil {
			t.Fatal(err)
		}
		return manifest, registered
	}
	for _, consumer := range []string{"retry-b", "retry-c"} {
		manifest, registered := fixture(consumer)
		for _, input := range manifest.Artifacts {
			if err := containerAcceptanceRetryInputs(manifest, consumer, commit, input.Key, registered); err != nil {
				t.Fatalf("exact %s cumulative input rejected: %v", consumer, err)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*taskpayload.JobInputManifest, map[string]model.ProjectArtifact)
	}{
		{"missing-transitive-A", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts = m.Artifacts[1:]
		}},
		{"missing-direct-B", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts = m.Artifacts[:1]
		}},
		{"extra-input", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts = append(m.Artifacts, m.Artifacts[1])
		}},
		{"foreign-name", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Name = "foreign/seed/input.txt"
		}},
		{"foreign-key", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Key = "objects/foreign"
		}},
		{"changed-sha", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Reference.SHA256 = strings.Repeat("e", 64)
		}},
		{"changed-size", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			*m.Artifacts[0].Reference.Size++
		}},
		{"missing-size", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Reference.Size = nil
		}},
		{"changed-storage", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Reference.StorageID = "foreign"
		}},
		{"changed-fingerprint", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Reference.Fingerprint = strings.Repeat("e", 64)
		}},
		{"changed-expiry", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) {
			m.Artifacts[0].Reference.ExpiresAt++
		}},
		{"unexpected-source", func(m *taskpayload.JobInputManifest, _ map[string]model.ProjectArtifact) { m.SourceCommit = commit }},
		{"missing-registration", func(_ *taskpayload.JobInputManifest, r map[string]model.ProjectArtifact) {
			delete(r, "retry-a/seed/input.txt")
		}},
		{"foreign-registration", func(_ *taskpayload.JobInputManifest, r map[string]model.ProjectArtifact) {
			r["foreign/seed/input.txt"] = r["retry-a/seed/input.txt"]
			delete(r, "retry-a/seed/input.txt")
		}},
		{"extra-registration", func(_ *taskpayload.JobInputManifest, r map[string]model.ProjectArtifact) {
			r["foreign/seed/input.txt"] = r["retry-a/seed/input.txt"]
		}},
		{"changed-registered-path", func(_ *taskpayload.JobInputManifest, r map[string]model.ProjectArtifact) {
			row := r["retry-a/seed/input.txt"]
			row.Name = "foreign.txt"
			r["retry-a/seed/input.txt"] = row
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, registered := fixture("retry-c")
			tc.mutate(manifest, registered)
			if err := containerAcceptanceRetryInputs(manifest, "retry-c", commit, "objects/b", registered); err == nil {
				t.Fatal("changed pinned cumulative inputs accepted")
			}
		})
	}
	manifest, registered := fixture("retry-c")
	if containerAcceptanceRetryInputs(manifest, "retry-b", commit, "objects/a", registered) == nil || containerAcceptanceRetryInputs(manifest, "retry-c", commit, "objects/foreign", registered) == nil || containerAcceptanceRetryInputs(manifest, "foreign", commit, "objects/a", registered) == nil {
		t.Fatal("wrong consumer, unexpected B input or unpinned transfer accepted")
	}
}
