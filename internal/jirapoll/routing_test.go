package jirapoll

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

func TestExtractRepoSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		rawURL            string
		allowedGitLabHost string
		expected          string
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
			name:     "GitHub reserved non-repo route: orgs",
			rawURL:   "https://github.com/orgs/my-org/teams/backend",
			expected: "",
		},
		{
			name:     "GitHub reserved non-repo route: advisories",
			rawURL:   "https://github.com/advisories/GHSA-1234-5678",
			expected: "",
		},
		{
			name:     "GitHub reserved non-repo route: user-attachments",
			rawURL:   "https://github.com/user-attachments/assets/abc-def",
			expected: "",
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
			name:     "Bitbucket reserved non-repo route: workspaces",
			rawURL:   "https://bitbucket.org/workspaces/my-workspace/settings",
			expected: "",
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
			name:     "GitLab legacy route without /-/: merge_requests",
			rawURL:   "https://gitlab.example.com/group/project/merge_requests/12",
			expected: "group/project",
		},
		{
			name:     "GitLab legacy route without /-/: nested issues",
			rawURL:   "https://gitlab.example.com/group/subgroup/project/issues/45",
			expected: "group/subgroup/project",
		},
		{
			name:     "GitLab subgroup named tree with /-/ UI route",
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
			name:     "GitLab project named packages",
			rawURL:   "https://gitlab.com/group/sub/packages",
			expected: "group/sub/packages",
		},
		{
			name:     "GitLab nested subgroup named packages",
			rawURL:   "https://gitlab.com/group/sub/packages/repo",
			expected: "group/sub/packages/repo",
		},
		{
			name:     "GitLab project named issues",
			rawURL:   "https://gitlab.com/group/sub/issues",
			expected: "group/sub/issues",
		},
		{
			name:     "GitLab legacy route without /-/: deeply nested issues ID",
			rawURL:   "https://gitlab.com/group/sub/issues/42",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: deeply nested merge_requests ID",
			rawURL:   "https://gitlab.com/group/sub/merge_requests/12",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: commit SHA",
			rawURL:   "https://gitlab.com/group/sub/commit/a1b2c3d4",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: compare range",
			rawURL:   "https://gitlab.com/group/sub/compare/main...feat",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: releases version tag",
			rawURL:   "https://gitlab.com/group/sub/releases/v1.2.0",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: wildcard blob subpath",
			rawURL:   "https://gitlab.com/group/sub/blob/main/README.md",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: wildcard tree subpath",
			rawURL:   "https://gitlab.com/group/sub/tree/main",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: wildcard commits subpath",
			rawURL:   "https://gitlab.com/group/sub/commits/main",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: wildcard raw subpath",
			rawURL:   "https://gitlab.com/group/sub/raw/main/file.txt",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: wildcard wikis subpath",
			rawURL:   "https://gitlab.com/group/sub/wikis/home",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: jobs raw asset",
			rawURL:   "https://gitlab.com/group/sub/jobs/99/raw",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: container_registry numeric ID",
			rawURL:   "https://gitlab.com/group/sub/container_registry/123",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: tags version tag",
			rawURL:   "https://gitlab.com/group/sub/tags/v1.2.0",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: tags edit action",
			rawURL:   "https://gitlab.com/group/sub/tags/v1.2.0/edit",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: numeric calendar release tag",
			rawURL:   "https://gitlab.com/group/sub/releases/2026.11",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: multi-segment release download",
			rawURL:   "https://gitlab.com/group/sub/releases/v1.2.0/downloads/app.tgz",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: release edit action",
			rawURL:   "https://gitlab.com/group/sub/releases/v1.2.0/edit",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: release new action",
			rawURL:   "https://gitlab.com/group/sub/releases/new",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: compare with slashed branch",
			rawURL:   "https://gitlab.com/group/sub/compare/main...feat/auth",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: issues new action",
			rawURL:   "https://gitlab.com/group/sub/issues/new",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: pipelines new action",
			rawURL:   "https://gitlab.com/group/sub/pipelines/new",
			expected: "group/sub",
		},
		{
			name:     "GitLab legacy route without /-/: forks new action",
			rawURL:   "https://gitlab.com/group/sub/forks/new",
			expected: "group/sub",
		},
		{
			name:     "GitLab nested subgroup under commit named my-repo preserved",
			rawURL:   "https://gitlab.com/group/sub/commit/my-repo",
			expected: "group/sub/commit/my-repo",
		},
		{
			name:     "GitLab nested subgroup under commit with short hex preserved",
			rawURL:   "https://gitlab.com/group/sub/commit/abc123",
			expected: "group/sub/commit/abc123",
		},
		{
			name:     "GitLab nested subgroup under jobs named my-repo preserved",
			rawURL:   "https://gitlab.com/group/sub/jobs/my-repo",
			expected: "group/sub/jobs/my-repo",
		},
		{
			name:     "GitLab reserved non-repo route: groups",
			rawURL:   "https://gitlab.example.com/groups/my-group",
			expected: "",
		},
		{
			name:     "GitLab reserved non-repo route: dashboard",
			rawURL:   "https://gitlab.com/dashboard/issues",
			expected: "",
		},
		{
			name:              "Configured self-hosted GitLab host recognized",
			rawURL:            "https://code.internal.corp/team/sub/service/-/merge_requests/9",
			allowedGitLabHost: "code.internal.corp",
			expected:          "team/sub/service",
		},
		{
			name:              "Configured self-hosted GitLab host with path prefix strips prefix",
			rawURL:            "https://example.com/gitlab/group/repo/-/merge_requests/1",
			allowedGitLabHost: "https://example.com/gitlab",
			expected:          "group/repo",
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
			name:     "Generic git.* HTTPS with .git preserves nested subgroups",
			rawURL:   "https://git.example.com/group/sub/repo.git",
			expected: "group/sub/repo",
		},
		{
			name:     "Generic SSH URL preserves nested subgroups",
			rawURL:   "ssh://git@git.example.com/group/sub/repo.git",
			expected: "group/sub/repo",
		},
		{
			name:     "Non-git host ending with .git rejected",
			rawURL:   "https://docs.example.com/download/tool.git",
			expected: "",
		},
		{
			name:     "Generic git.* host without .git rejected",
			rawURL:   "https://git.example.com/group/repo",
			expected: "",
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
		{
			name:     "Invalid URL parse error",
			rawURL:   "://not-a-valid-url",
			expected: "",
		},
		{
			name:     "Relative path without host",
			rawURL:   "/owner/repo",
			expected: "",
		},
		{
			name:     "Unsupported ftp scheme",
			rawURL:   "ftp://github.com/my-org/pay-backend",
			expected: "",
		},
		{
			name:     "SSH git@ without colon",
			rawURL:   "git@github.com/my-org/pay-backend.git",
			expected: "",
		},
		{
			name:     "HTTPS URL with port strips port",
			rawURL:   "https://github.com:443/my-org/pay-backend",
			expected: "my-org/pay-backend",
		},
		{
			name:     "Root host URL without path",
			rawURL:   "https://github.com/",
			expected: "",
		},
		{
			name:     "GitHub single-segment path rejected",
			rawURL:   "https://github.com/my-org",
			expected: "",
		},
		{
			name:     "GitHub path with dotdot segment rejected",
			rawURL:   "https://github.com/my-org/../pay-backend",
			expected: "",
		},
		{
			name:     "Bitbucket reserved workspace route rejected",
			rawURL:   "https://bitbucket.org/workspaces/team",
			expected: "",
		},
		{
			name:     "GitLab single-segment path rejected",
			rawURL:   "https://gitlab.com/my-org",
			expected: "",
		},
		{
			name:     "GitLab path with dot segment rejected",
			rawURL:   "https://gitlab.com/my-org/./repo",
			expected: "",
		},
		{
			name:     "Generic git.* host with /-/",
			rawURL:   "https://git.example.com/group/sub/repo/-/merge_requests/1.git",
			expected: "group/sub/repo",
		},
		{
			name:     "Generic SSH single-segment path rejected",
			rawURL:   "ssh://git@git.example.com/only-one",
			expected: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual := extractRepoSlugWithAllowedHost(tc.rawURL, tc.allowedGitLabHost)
			assert.Equal(t, tc.expected, actual)
			if tc.allowedGitLabHost == "" {
				assert.Equal(t, tc.expected, extractRepoSlug(tc.rawURL))
			}
		})
	}
	assert.Equal(t, "", hostFromURL(""))
	assert.Equal(t, "", hostFromURL("://bad"))
	assert.Equal(t, "code.internal.corp", hostFromURL("https://Code.Internal.Corp:8443/team/repo"))

	h, p := parseGitLabInstance("https://Example.COM:8443/gitlab/")
	assert.Equal(t, "example.com", h)
	assert.Equal(t, "gitlab", p)
	host, slug := extractRepoHostAndSlug("https://example.com/gitlab/group/repo/-/merge_requests/1", "https://example.com/gitlab")
	assert.Equal(t, "example.com", host)
	assert.Equal(t, "group/repo", slug)
	hostOutside, slugOutside := extractRepoHostAndSlug("https://example.com/other/repo/-/merge_requests/1", "https://example.com/gitlab")
	assert.Equal(t, "", hostOutside)
	assert.Equal(t, "", slugOutside)
}

func runDiscoverForCandidates(ctx context.Context, mc *mockClient, opts Options, candidates []jira.Issue) ([]jira.Issue, error) {
	mc.searchResult = candidates
	if opts.JiraProject == "" {
		opts.JiraProject = "PROJ"
	}
	return newTestPoller(mc, nil, opts).discoverCandidates(ctx)
}

func TestFilterRouting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("EmptyCandidates", func(t *testing.T) {
		t.Parallel()
		mc := newMockClient()
		p := newTestPoller(mc, nil, Options{JiraProject: "PROJ", TargetRepo: "my-org/pay-backend"})
		routed, attempted, linkErrors, stopScan := p.filterRoutingPage(ctx, nil, 0)
		assert.Empty(t, routed)
		assert.Zero(t, attempted)
		assert.Zero(t, linkErrors)
		assert.False(t, stopScan)
	})

	t.Run("AttachedLinkMatch", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("AttachedLinkMatch_ConfiguredGitLabHost", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://code.internal.corp/team/sub/pay-backend/-/merge_requests/12"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:  "team/sub/pay-backend",
			TargetForge: TargetForgeGitLab,
			GitLabURL:   "https://code.internal.corp",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("AttachedLinkMatch_ConfiguredGitLabWithPathPrefix", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://example.com/gitlab/group/repo/-/merge_requests/1"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:  "group/repo",
			TargetForge: TargetForgeGitLab,
			GitLabURL:   "https://example.com/gitlab",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("SameSlugDifferentHost_Skipped", func(t *testing.T) {
		t.Parallel()
		mcGitLabOnly := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://gitlab.com/acme/backend/-/merge_requests/1"}},
				},
			},
		}
		routedGH, err := runDiscoverForCandidates(ctx, mcGitLabOnly, Options{
			TargetRepo: "acme/backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		assert.Empty(t, routedGH)

		mcSpoofGitLab := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-2": {
					{Object: jira.RemoteLinkObject{URL: "https://gitlab.attacker.example/acme/backend"}},
				},
			},
		}
		routedSpoofGH, err := runDiscoverForCandidates(ctx, mcSpoofGitLab, Options{
			TargetRepo: "acme/backend",
		}, []jira.Issue{{Key: "PROJ-2"}})
		require.NoError(t, err)
		assert.Empty(t, routedSpoofGH)

		mcGitHubAndSpoof := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-3": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/acme/backend/pull/1"}},
					{Object: jira.RemoteLinkObject{URL: "https://gitlab.attacker.example/acme/backend"}},
				},
			},
		}
		routedGL, err := runDiscoverForCandidates(ctx, mcGitHubAndSpoof, Options{
			TargetRepo:  "acme/backend",
			TargetForge: TargetForgeGitLab,
			GitLabURL:   "https://gitlab.com",
		}, []jira.Issue{{Key: "PROJ-3"}})
		require.NoError(t, err)
		assert.Empty(t, routedGL)
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
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
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
		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "backend",
		}, candidates)
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
		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "backend",
		}, candidates)
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
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("ComponentFallback_Match", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "payments"}}, // case-insensitive check
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Payments",
		}, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("ComponentFallback_Mismatch_Skipped", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "Frontend"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Payments",
		}, candidates)
		require.NoError(t, err)
		assert.Empty(t, routed)
	})

	t.Run("NoAttachedLinks_NoComponentConfigured_Kept", func(t *testing.T) {
		t.Parallel()
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-1", routed[0].Key)
	})

	t.Run("RemoteLinksError_PartialFailure_SafelySkipped", func(t *testing.T) {
		t.Parallel()
		// If listing remote links fails for one candidate, do NOT fall back to component;
		// skip the failed candidate while still routing valid candidates without error.
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-2": {{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}}},
			},
			remoteLinksErr: map[string]error{
				"PROJ-1": fmt.Errorf("network timeout"),
			},
		}
		candidates := []jira.Issue{
			{
				Key: "PROJ-1",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
			{
				Key: "PROJ-2",
				Fields: jira.IssueFields{
					Components: []jira.Component{{Name: "backend"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Backend",
		}, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 1)
		assert.Equal(t, "PROJ-2", routed[0].Key)
	})

	t.Run("RemoteLinksError_AllFailed_ReturnsError", func(t *testing.T) {
		t.Parallel()
		// When every candidate fails remote-link lookup, return an error so the cycle
		// does not silently finish as a quiet project.
		mc := &mockClient{
			remoteLinksErr: map[string]error{
				"PROJ-1": fmt.Errorf("403 forbidden"),
				"PROJ-2": fmt.Errorf("403 forbidden"),
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo:    "my-org/pay-backend",
			JiraComponent: "Backend",
		}, []jira.Issue{{Key: "PROJ-1"}, {Key: "PROJ-2"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "all 2 candidates dropped by remote-link lookup errors")
		assert.Empty(t, routed)
	})

	t.Run("UnrelatedUpstreamIssueLink_NoComponent_Skipped", func(t *testing.T) {
		t.Parallel()
		// Even with no component configured, Priority 1 link routing skips candidates
		// whose attached forge link resolves to a different repository slug.
		mc := &mockClient{
			remoteLinks: map[string][]jira.RemoteLink{
				"PROJ-1": {
					{Object: jira.RemoteLinkObject{URL: "https://github.com/kubernetes/kubernetes/issues/123"}},
				},
			},
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
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
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{
			{Key: "PROJ-1"},
			{Key: "PROJ-2"},
			{Key: "PROJ-3"},
		})
		require.NoError(t, err)
		require.Len(t, routed, 3)
		assert.Equal(t, "PROJ-1", routed[0].Key)
		assert.Equal(t, "PROJ-2", routed[1].Key)
		assert.Equal(t, "PROJ-3", routed[2].Key)
	})

	t.Run("StopsFetchingOnceMReached", func(t *testing.T) {
		t.Parallel()
		mc := newMockClient()
		candidates := make([]jira.Issue, 20)
		for i := range candidates {
			key := fmt.Sprintf("PROJ-%d", i+1)
			candidates[i] = jira.Issue{Key: key}
			mc.remoteLinks[key] = []jira.RemoteLink{
				{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}},
			}
		}
		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
			M:          2,
		}, candidates)
		require.NoError(t, err)
		require.Len(t, routed, 2)
		assert.Equal(t, 1, mc.searchPageCalls)
		assert.LessOrEqual(t, len(mc.remoteLinksCalls), routingBatchConcurrency)
	})

	t.Run("AbortsEarlyOnRateLimitOrServerError", func(t *testing.T) {
		t.Parallel()
		mc := newMockClient()
		candidates := make([]jira.Issue, 20)
		for i := range candidates {
			key := fmt.Sprintf("PROJ-%d", i+1)
			candidates[i] = jira.Issue{Key: key}
			mc.remoteLinks[key] = []jira.RemoteLink{
				{Object: jira.RemoteLinkObject{URL: "https://github.com/my-org/pay-backend"}},
			}
		}
		mc.remoteLinksErr["PROJ-1"] = &jira.APIError{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}

		routed, err := runDiscoverForCandidates(ctx, mc, Options{
			TargetRepo: "my-org/pay-backend",
			M:          10,
		}, candidates)
		require.NoError(t, err)
		// Batch 1 (8 issues) finishes with 7 routed and 1 429 error; batch 2 is not started.
		assert.Len(t, routed, routingBatchConcurrency-1)
		assert.Len(t, mc.remoteLinksCalls, routingBatchConcurrency)

		// When an entire first batch fails with 5xx, discoverCandidates stops and reports error.
		mc2 := newMockClient()
		for i := 0; i < routingBatchConcurrency; i++ {
			key := fmt.Sprintf("PROJ-%d", i+1)
			mc2.remoteLinksErr[key] = &jira.APIError{StatusCode: http.StatusServiceUnavailable, Message: "unavailable"}
		}
		routed2, err2 := runDiscoverForCandidates(ctx, mc2, Options{
			TargetRepo: "my-org/pay-backend",
			M:          10,
		}, candidates)
		require.Error(t, err2)
		assert.Contains(t, err2.Error(), fmt.Sprintf("all %d candidates dropped by remote-link lookup errors", routingBatchConcurrency))
		assert.Empty(t, routed2)
		assert.Len(t, mc2.remoteLinksCalls, routingBatchConcurrency)
	})

	t.Run("AbortsOnFanoutDeadline", func(t *testing.T) {
		t.Parallel()
		mc := newMockClient()
		mc.searchResult = []jira.Issue{{Key: "PROJ-1"}}
		p := newTestPoller(mc, nil, Options{
			JiraProject: "PROJ",
			TargetRepo:  "my-org/pay-backend",
		})
		p.routingFanoutTimeout = 1 * time.Nanosecond
		time.Sleep(2 * time.Millisecond)

		routed, err := p.discoverCandidates(ctx)
		require.NoError(t, err)
		assert.Empty(t, routed)
		assert.Empty(t, mc.remoteLinksCalls)

		expiredCtx, cancel := context.WithCancel(ctx)
		cancel()
		routed2, err2 := runDiscoverForCandidates(expiredCtx, newMockClient(), Options{
			TargetRepo: "my-org/pay-backend",
		}, []jira.Issue{{Key: "PROJ-1"}})
		require.NoError(t, err2)
		assert.Empty(t, routed2)
	})
}

func TestIsRoutingAbortError(t *testing.T) {
	t.Parallel()
	assert.True(t, isRoutingAbortError(context.DeadlineExceeded))
	assert.True(t, isRoutingAbortError(context.Canceled))
	assert.True(t, isRoutingAbortError(&jira.APIError{StatusCode: http.StatusUnauthorized}))
	assert.True(t, isRoutingAbortError(&jira.APIError{StatusCode: http.StatusForbidden}))
	assert.True(t, isRoutingAbortError(&jira.APIError{StatusCode: http.StatusTooManyRequests}))
	assert.True(t, isRoutingAbortError(&jira.APIError{StatusCode: http.StatusBadGateway}))
	assert.False(t, isRoutingAbortError(&jira.APIError{StatusCode: http.StatusNotFound}))
	assert.False(t, isRoutingAbortError(fmt.Errorf("transient network error")))
}

func TestPollerDiscoverCandidates_LinkingDisabled403_EndToEnd(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			_, _ = w.Write([]byte(`{"issues":[{"id":"1","key":"PROJ-1","fields":{"summary":"Fix bug","labels":[]}}],"isLast":true}`))
		case "/rest/api/3/issue/PROJ-1/remotelink":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errorMessages":["Issue linking is currently disabled."],"errors":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL), jira.WithEmail("bot@example.com"), jira.WithHTTPClient(srv.Client()))
	require.NoError(t, err)

	p := newTestPoller(client, nil, Options{
		JiraProject: "PROJ",
		TargetRepo:  "my-org/pay-backend",
	})
	routed, err := p.discoverCandidates(context.Background())
	require.NoError(t, err)
	require.Len(t, routed, 1)
	assert.Equal(t, "PROJ-1", routed[0].Key)
}

func TestPollerRun_AllRemoteLinksErrorsReturnError(t *testing.T) {
	t.Parallel()

	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{Key: "PROJ-1"},
		{Key: "PROJ-2"},
	}
	mc.remoteLinksErr["PROJ-1"] = fmt.Errorf("403 forbidden")
	mc.remoteLinksErr["PROJ-2"] = fmt.Errorf("403 forbidden")

	p := newTestPoller(mc, nil, Options{
		JiraProject: "PROJ",
		TargetRepo:  "my-org/pay-backend",
	})

	err := p.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all 2 candidates dropped by remote-link lookup errors")
}
