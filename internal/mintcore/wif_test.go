package mintcore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildRepoProviderID(t *testing.T) {
	tests := []struct {
		owner, repo string
		want        string
	}{
		{"myorg", "my-repo", "gh-myorg-my-repo"},
		{"MyOrg", "My-Repo", "gh-myorg-my-repo"},
		{"org", "repo.name", "gh-org-repo-name"},
		{"org", "repo_name", "gh-org-repo-name"},
	}
	for _, tt := range tests {
		t.Run(tt.owner+"/"+tt.repo, func(t *testing.T) {
			got := BuildRepoProviderID(tt.owner, tt.repo)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildRepoProviderID_Truncation(t *testing.T) {
	id := BuildRepoProviderID("long-organization-name", "very-long-repository-name")
	assert.LessOrEqual(t, len(id), 32)
	assert.False(t, strings.HasSuffix(id, "-"))
}

func TestBuildRepoProviderID_NoTrailingHyphen(t *testing.T) {
	id := BuildRepoProviderID("org", "repo---")
	assert.False(t, strings.HasSuffix(id, "-"))
}
