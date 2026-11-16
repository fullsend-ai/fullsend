//go:build live_forgejo

package forgejo

import (
	"context"
	"os"
	"testing"
)

func TestLiveForgejoSmoke(t *testing.T) {
	tok := os.Getenv("FORGEJO_TOKEN")
	if tok == "" {
		t.Skip("FORGEJO_TOKEN not set")
	}
	c := New(tok, WithBaseURL("https://git.forge.internal/api/v1"))
	u, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.GetRepo(context.Background(), "forgeops-ai", "forge-platform")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := c.ListOrgRepos(context.Background(), "forgeops-ai", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("user=%s repo=%s count=%d", u, r.FullName, len(repos))
	if r.FullName == "" || len(repos) < 1 {
		t.Fatal("unexpected empty")
	}
}
