package jirapoll

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

func TestExtractRepoSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawURL   string
		expected string
	}{
		{
			name:     "Standard GitHub HTTPS",
			rawURL:   "https://github.com/my-org/pay-backend",
			expected: "my-org/pay-backend",
		},
		{
			name:     "GitHub HTTPS with .git",
			rawURL:   "https://github.com/my-org/pay-backend.git",
			expected: "my-org/pay-backend",
		},
		{
			name:     "GitHub HTTPS with trailing slash",
			rawURL:   "https://github.com/my-org/pay-backend/",
			expected: "my-org/pay-backend",
		},
		{
			name:     "GitHub pull request subpath",
			rawURL:   "https://github.com/my-org/pay-backend/pull/42",
			expected: "my-org/pay-backend",
		},
		{
			name:     "GitHub commits subpath",
			rawURL:   "https://github.com/my-org/pay-backend/commits/main",
			expected: "my-org/pay-backend",
		},
		{
			name:     "GitHub tree subpath",
			rawURL:   "https://github.com/my-org/pay-backend/tree/main",
			expected: "my-org/pay-backend",
		},
		{
			name:     "Bitbucket pull request subpath",
			rawURL:   "https://bitbucket.org/my-org/pay-backend/pull-requests/10",
			expected: "my-org/pay-backend",
		},
		{
			name:     "Bitbucket src subpath",
			rawURL:   "https://bitbucket.org/my-org/pay-backend/src/master/main.go",
			expected: "my-org/pay-backend",
		},
		{
			name:     "Standard GitLab HTTPS",
			rawURL:   "https://gitlab.com/my-org/pay-frontend",
			expected: "my-org/pay-frontend",
		},
		{
			name:     "GitLab merge request subpath",
			rawURL:   "https://gitlab.example.com/group/subgroup/my-service/-/merge_requests/123",
			expected: "group/subgroup/my-service",
		},
		{
			name:     "GitLab tree subpath",
			rawURL:   "https://gitlab.com/group/repo/-/tree/develop",
			expected: "group/repo",
		},
		{
			name:     "GitLab subgroup named tree",
			rawURL:   "https://gitlab.example.com/group/tree/project/-/merge_requests/12",
			expected: "group/tree/project",
		},
		{
			name:     "GitLab subgroup named blob",
			rawURL:   "https://gitlab.example.com/group/blob/project",
			expected: "group/blob/project",
		},
		{
			name:     "GitLab subgroup named issues",
			rawURL:   "https://gitlab.example.com/group/issues/project",
			expected: "group/issues/project",
		},
		{
			name:     "SSH git@ format GitHub",
			rawURL:   "git@github.com:my-org/pay-backend.git",
			expected: "my-org/pay-backend",
		},
		{
			name:     "SSH git@ format GitLab with subgroups",
			rawURL:   "git@gitlab.example.com:group/subgroup/my-service.git",
			expected: "group/subgroup/my-service",
		},
		{
			name:     "Lookalike domain rejected: github.com.evil.example",
			rawURL:   "https://github.com.evil.example/my-org/pay-backend",
			expected: "",
		},
		{
			name:     "Lookalike domain rejected: notgitlab.com",
			rawURL:   "https://notgitlab.com/group/subgroup/my-service",
			expected: "",
		},
		{
			name:     "Non-git URL: Google Docs",
			rawURL:   "https://docs.google.com/document/d/12345/edit",
			expected: "",
		},
		{
			name:     "Non-git URL: Confluence",
			rawURL:   "https://confluence.corp.example.com/display/PROJ/Architecture",
			expected: "",
		},
		{
			name:     "Non-git URL: Figma",
			rawURL:   "https://www.figma.com/file/abcdef/Mockups",
			expected: "",
		},
		{
			name:     "Empty URL",
			rawURL:   "",
			expected: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual := extractRepoSlug(tc.rawURL)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestFilterRouting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("AttachedLinkMatch", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}},
				},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo: "my-org/pay-backend",
		})

		candidates := []jira.Issue{{Key: "PROJ-1"}}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("AttachedLinkMismatch_Skipped", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-frontend"}},
				},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo: "my-org/pay-backend",
		})

		candidates := []jira.Issue{{Key: "PROJ-1"}}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		assert.Empty(t, routed)
	})

	t.Run("AttachedLinkPriorityOverComponent", func(t *testing.T) {
		t.Parallel()
		// Issue has component "backend", but attached link points to "pay-frontend".
		// pay-backend poller must skip it because the attached link has priority.
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-frontend"}},
				},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "backend",
		})

		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		assert.Empty(t, routed)
	})

	t.Run("NonGitLinkIgnored_FallsBackToComponent", func(t *testing.T) {
		t.Parallel()
		// Issue has a Google Doc attached and component "backend".
		// Google doc is ignored as a non-repo link; component match claims the issue.
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://docs.google.com/document/d/12345/edit"}},
				},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "backend",
		})

		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("MultipleReposAttached_CurrentRepoMatched", func(t *testing.T) {
		t.Parallel()
		// Issue links to both frontend and backend repos.
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-frontend"}},
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}},
				},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo: "my-org/pay-backend",
		})

		candidates := []jira.Issue{{Key: "PROJ-1"}}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("ComponentFallback_Match", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Payments",
		})

		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "payments"}}, // case-insensitive check
				},
			},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("ComponentFallback_Mismatch_Skipped", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Payments",
		})

		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "Frontend"}},
				},
			},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		assert.Empty(t, routed)
	})

	t.Run("NoAttachedLinks_NoComponentConfigured_Kept", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo: "my-org/pay-backend",
		})

		candidates := []jira.Issue{{Key: "PROJ-1"}}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("RemoteLinksError_SafelySkipped", func(t *testing.T) {
		t.Parallel()
		// If listing remote links fails, do NOT fall back to component.
		// Skip the issue to prevent claiming an issue for the wrong repo.
		mc := &mockClient{
			remoteLinksErr: map[string]error{
				"PROJ-1": fmt.Errorf("network timeout"),
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Backend",
		})

		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		assert.Empty(t, routed)
	})

	t.Run("CandidateOrderPreserved", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}}},
				"PROJ-2": {{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}}},
				"PROJ-3": {{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}}},
			},
		}
		p := newTestPoller(mc, nil, Options{
			TargetRepo: "my-org/pay-backend",
		})

		candidates := []jira.Issue{
			{Key: "PROJ-1"},
			{Key: "PROJ-2"},
			{Key: "PROJ-3"},
		}
		routed, err := p.filterRouting(ctx, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 3)
		assert.Equal(t, "PROJ-1", routed[0].Key)
		assert.Equal(t, "PROJ-2", routed[1].Key)
		assert.Equal(t, "PROJ-3", routed[2].Key)
	})
}
