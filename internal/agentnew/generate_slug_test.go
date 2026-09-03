package agentnew

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerFromRemoteURL(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"git@github.com:my-org/my-repo.git", "my-org"},
		{"git@github.com:my-org/my-repo", "my-org"},
		{"https://github.com/my-org/my-repo.git", "my-org"},
		{"https://github.com/my-org/my-repo", "my-org"},
		{"https://gitlab.cee.example.com/group/sub/my-repo", "sub"},
		{"ssh://git@github.com/my-org/my-repo.git", "my-org"},
		// Owners that would not survive harness.ValidSlug fall back rather
		// than producing a harness that fails validation.
		{"https://github.com/-bad/my-repo", ""},
		{"https://github.com/o.k/my-repo", ""},
		{"nonsense", ""},
		{"", ""},
	} {
		if got := ownerFromRemoteURL(tc.url); got != tc.want {
			t.Errorf("ownerFromRemoteURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestOwnerFromGitConfig(t *testing.T) {
	cfg := `[core]
	repositoryformatversion = 0
[remote "upstream"]
	url = git@github.com:upstream-org/repo.git
	fetch = +refs/heads/*:refs/remotes/upstream/*
[remote "origin"]
	url = git@github.com:my-org/my-repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
`
	if got := ownerFromGitConfig(cfg); got != "my-org" {
		t.Errorf("ownerFromGitConfig = %q, want my-org", got)
	}
	if got := ownerFromGitConfig("[core]\n\tbare = false\n"); got != "" {
		t.Errorf("config with no origin should yield \"\", got %q", got)
	}
	if got := ownerFromGitConfig("[remote \"origin\"]\n\turl\n"); got != "" {
		t.Errorf("malformed url line should yield \"\", got %q", got)
	}
}

// TestGitConfigOwnerWalksUp: --fullsend-dir is normally .fullsend inside the
// repository, so the owner lookup has to climb to the repository root.
func TestGitConfigOwnerWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = https://github.com/my-org/my-repo.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, ".fullsend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := GitConfigOwner(nested); got != "my-org" {
		t.Errorf("GitConfigOwner = %q, want my-org", got)
	}
	if got := GitConfigOwner(t.TempDir()); got != "" {
		t.Errorf("a directory outside any repository should yield \"\", got %q", got)
	}
}

func TestDeriveSlug(t *testing.T) {
	if got := DeriveSlug("lint-docs", "my-org"); got != "my-org-lint-docs" {
		t.Errorf("DeriveSlug = %q", got)
	}
	if got := DeriveSlug("lint-docs", ""); got != "fullsend-lint-docs" {
		t.Errorf("DeriveSlug fallback = %q", got)
	}
}
