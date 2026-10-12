package sourceprovenance

import (
	"strings"
	"testing"
)

func TestBindingUsesExactExecutionAndSourceWithoutChangingDispatch(t *testing.T) {
	b, err := NewBinding("pipeline", "run", "job", "GIT", "https://GitHub.COM/Owner/Repo.git", "Main")
	if err != nil || b.Type != "git" || b.Repository != "https://github.com/Owner/Repo.git" || !ValidBindingDigest(b.Digest) {
		t.Fatalf("binding: %+v %v", b, err)
	}
	for _, args := range [][6]string{{"other", "run", "job", "git", b.Repository, "Main"}, {"pipeline", "other", "job", "git", b.Repository, "Main"}, {"pipeline", "run", "other", "git", b.Repository, "Main"}, {"pipeline", "run", "job", "other", b.Repository, "Main"}, {"pipeline", "run", "job", "git", "https://github.com/owner/Repo.git", "Main"}, {"pipeline", "run", "job", "git", b.Repository, "main"}} {
		changed, err := NewBinding(args[0], args[1], args[2], args[3], args[4], args[5])
		if err != nil || changed.Digest == b.Digest {
			t.Fatalf("binding did not change: %+v %v", args, err)
		}
	}
}
func TestRepositoryRejectsCredentialBearingAndAmbiguousIdentity(t *testing.T) {
	for _, value := range []string{"", " https://example/repo", "https://token@example/repo", "ssh://git@example/repo", "https://example/repo?token=secret", "https://example/repo#secret", "https://example/repo\n", "secret@example:repo", "https:///repo", "unsupported://example/repo", "file://other/repo"} {
		if _, err := CanonicalRepository(value); err == nil {
			t.Fatalf("unsafe identity accepted: %q", value)
		}
	}
	for _, value := range []string{"/tmp/fixtures/repo", "relative/repo", "file:///tmp/repo", "git@example:org/repo.git", "ssh://example/org/repo", "https://example/Org/repo.git"} {
		if _, err := CanonicalRepository(value); err != nil {
			t.Fatalf("safe fixture rejected: %q %v", value, err)
		}
	}
	for _, typ := range []string{"", " git", "git ", "git\n"} {
		if _, err := NewBinding("p", "r", "j", typ, "/tmp/repo", "main"); err == nil {
			t.Fatal("ambiguous type accepted")
		}
	}
}
func TestCommitAndBindingFormats(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		if !ValidCommit(value) {
			t.Fatal("full commit rejected")
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("A", 40), strings.Repeat("g", 64), strings.Repeat("a", 40) + "\n"} {
		if ValidCommit(value) {
			t.Fatalf("invalid commit accepted %q", value)
		}
	}
	for _, value := range []string{strings.Repeat("a", 64), "v2:" + strings.Repeat("a", 64), "v1:" + strings.Repeat("A", 64)} {
		if ValidBindingDigest(value) {
			t.Fatal("invalid binding accepted")
		}
	}
}
