package taskqueue

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestArtifactReferenceIntegrityPresenceAndCanonicalDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, metadata string
		valid          bool
	}{
		{"historical", `{}`, false},
		{"missing_size", `{"sha256":"` + digest + `"}`, false},
		{"null_size", `{"sha256":"` + digest + `","size":null}`, false},
		{"explicit_empty", `{"sha256":"` + digest + `","size":0}`, true},
		{"positive_size", `{"sha256":"` + digest + `","size":5}`, true},
		{"negative_size", `{"sha256":"` + digest + `","size":-1}`, false},
		{"missing_digest", `{"size":0}`, false},
		{"uppercase_digest", `{"sha256":"` + strings.ToUpper(digest) + `","size":0}`, false},
		{"short_digest", `{"sha256":"aaaa","size":0}`, false},
		{"nonhex_digest", `{"sha256":"` + strings.Repeat("z", 64) + `","size":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reference ArtifactReference
			if err := sonic.UnmarshalString(test.metadata, &reference); err != nil {
				t.Fatal(err)
			}
			if err := reference.ValidateIntegrity(); (err == nil) != test.valid {
				t.Fatalf("validation = %v; want valid %v", err, test.valid)
			}
			encoded, err := sonic.Marshal(reference)
			if err != nil {
				t.Fatal(err)
			}
			var replay ArtifactReference
			if err := sonic.Unmarshal(encoded, &replay); err != nil {
				t.Fatal(err)
			}
			if (reference.Size == nil) != (replay.Size == nil) || reference.Size != nil && *reference.Size != *replay.Size {
				t.Fatalf("size presence changed during queue round trip: %s", encoded)
			}
		})
	}
}
