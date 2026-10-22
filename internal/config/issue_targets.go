package config

// DefaultIssueCreationRepos returns the cross-repository issue destinations
// granted by a fresh GitHub installation without a preset or explicit create_issues
// setting. The source repository is implicitly allowed by the post-scripts.
// Each call returns its own slice so callers can customize it independently.
func DefaultIssueCreationRepos() []string {
	return []string{DefaultUpstreamRepo, DefaultAgentsRepo}
}
