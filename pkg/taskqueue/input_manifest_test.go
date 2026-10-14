package taskqueue

import (
	"fmt"
	"strings"
	"testing"
)

func TestJobInputManifestCanonicalAndCopy(t *testing.T) {
	size := int64(0)
	refs := map[string]ArtifactReference{"a.txt": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: strings.Repeat("a", 64), Size: &size}}
	m, err := NewJobInputManifest("", map[string]string{"a.txt": "artifacts/run/job/step/a/a.txt"}, refs)
	if err != nil {
		t.Fatal(err)
	}
	size = 88
	if *m.Artifacts[0].Reference.Size != 0 {
		t.Fatal("manifest retains caller size pointer")
	}
	encoded, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseJobInputManifest(encoded); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(encoded, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(encoded, `"version":1`, `"version":1,"extra":true`, 1), encoded + " ", strings.Replace(encoded, `"size":0`, `"size":null`, 1)} {
		if _, err := ParseJobInputManifest(bad); err == nil {
			t.Fatalf("accepted ambiguous encoding %s", bad)
		}
	}
	i := JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: "v1:" + strings.Repeat("f", 64)}
	digest, err := m.Digest(i)
	if err != nil {
		t.Fatal(err)
	}
	i.ClaimID = "other"
	changed, err := m.Digest(i)
	if err != nil || digest == changed {
		t.Fatal("claim not bound")
	}
}

func TestJobInputManifestRejectsUnknownArtifactIntegrity(t *testing.T) {
	for _, ref := range []ArtifactReference{{}, {StorageID: "store", SHA256: strings.Repeat("a", 64)}, {StorageID: "store", SHA256: strings.Repeat("a", 64), Size: new(int64)}} {
		if _, err := NewJobInputManifest("", map[string]string{"a": "key"}, map[string]ArtifactReference{"a": ref}); err == nil {
			t.Fatal("unknown metadata accepted")
		}
	}
	empty, err := NewJobInputManifest("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := empty.Canonical()
	if err != nil || encoded != `{"version":1,"sourceCommit":"","artifacts":[]}` {
		t.Fatalf("empty inputs not explicit: %s %v", encoded, err)
	}
}

func TestJobInputManifestRejectsOversizeBeforePersistence(t *testing.T) {
	uris, refs := map[string]string{}, map[string]ArtifactReference{}
	size := int64(0)
	for index := range 1000 {
		name := fmt.Sprintf("%04d/%s", index, strings.Repeat("a", 1790))
		uris[name] = strings.Repeat("b", 1800)
		refs[name] = ArtifactReference{StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: strings.Repeat("a", 64), Size: &size}
	}
	if _, err := NewJobInputManifest("", uris, refs); err == nil {
		t.Fatal("oversized input set reached persistence")
	}
	if _, err := ParseJobInputManifest(strings.Repeat(" ", MaxJobInputManifestBytes+1)); err == nil {
		t.Fatal("oversized RPC manifest accepted")
	}
}
