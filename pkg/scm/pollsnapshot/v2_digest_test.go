// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"net/http"
	"testing"
)

// These constants also pass against the unchanged V31 3670087 reader sources.
func TestVersion2FrozenDigest(t *testing.T) {
	expected := map[string]string{"github": "0799287570d47864f2c99272eb224a7994c41158f95f63e980c3be78265797da", "gitea": "7da6cc4112a18d1a55b1b5027ea1f7568b87a904d1652e2afc6878111b47250e"}
	for provider, want := range expected {
		t.Run(provider, func(t *testing.T) {
			reader := fixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response { return inventoryResponse(req, fixturePR()) })
			result, err := reader.Read(context.Background(), fixtureRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Digest != want {
				t.Fatalf("v2 digest: %s", result.Digest)
			}
		})
	}
}
