package forgejostack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// The seeded agent-definition repository. It takes the place of
// fullsend-ai/agents on GitHub, so behaviour tests on the stack resolve
// built-in agents from the stack itself and need no live GitHub access.
const (
	// AgentsOwner is the organization that owns the seeded repository.
	AgentsOwner = "fullsend-ai"
	// AgentsRepo is the name of the seeded agent-definition repository.
	AgentsRepo = "agents"
)

// Environment variables that point fullsend at a Forgejo agent-definition
// source. They duplicate the names internal/cli reads (see
// run_agents_repo_forgejo.go): the behaviourtest graph must not import
// internal/cli.
const (
	agentsURLEnv  = "FULLSEND_FORGEJO_URL"
	agentsRepoEnv = "FULLSEND_FORGEJO_AGENTS_REPO"
	agentsRefEnv  = "FULLSEND_FORGEJO_AGENTS_REF"
)

// agentFixture is the minimal agent-definition repository: a triage
// harness and the agent prompt and policy it references, laid out as in
// fullsend-ai/agents.
var agentFixture = map[string]string{
	"harness/triage.yaml": `# Minimal triage harness of the forgejo-dev agent-definition fixture.
role: triage
slug: triage
agent: agents/triage.md
policy: policies/triage.yaml
`,
	"agents/triage.md":     "# Triage\n\nTriage agent of the forgejo-dev agent-definition fixture.\n",
	"policies/triage.yaml": "# Policy of the forgejo-dev agent-definition fixture.\n",
}

// AgentFixtureFiles returns a copy of the seeded repository's files, keyed
// by path.
func AgentFixtureFiles() map[string]string {
	out := make(map[string]string, len(agentFixture))
	for k, v := range agentFixture {
		out[k] = v
	}
	return out
}

var commitSHAPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// seedAgentDefinitions creates the public AgentsOwner organization and its
// AgentsRepo repository, commits the fixture in one commit, and records
// that commit as the pinned revision.
func (s *Stack) seedAgentDefinitions(ctx context.Context) error {
	if err := s.apiExpect(ctx, http.MethodPost, "api/v1/orgs", map[string]any{
		"username": AgentsOwner, "visibility": "public",
	}, http.StatusCreated); err != nil {
		return fmt.Errorf("creating organization %s: %w", AgentsOwner, err)
	}
	if err := s.apiExpect(ctx, http.MethodPost, "api/v1/orgs/"+AgentsOwner+"/repos", map[string]any{
		"name": AgentsRepo, "auto_init": true, "default_branch": defaultBranch, "private": false,
	}, http.StatusCreated); err != nil {
		return fmt.Errorf("creating repository %s/%s: %w", AgentsOwner, AgentsRepo, err)
	}
	paths := make([]string, 0, len(agentFixture))
	for p := range agentFixture {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		files = append(files, map[string]string{
			"operation": "create",
			"path":      p,
			"content":   base64.StdEncoding.EncodeToString([]byte(agentFixture[p])),
		})
	}
	status, body, err := s.api(ctx, http.MethodPost, "api/v1/repos/"+AgentsOwner+"/"+AgentsRepo+"/contents", map[string]any{
		"branch": defaultBranch, "message": "Add agent-definition fixture", "files": files,
	}, true)
	if err != nil {
		return fmt.Errorf("committing the fixture: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("committing the fixture: HTTP %d: %s", status, truncate(body))
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("committing the fixture: %w", err)
	}
	if !commitSHAPattern.MatchString(out.Commit.SHA) {
		return fmt.Errorf("committing the fixture: response has no commit SHA: %s", truncate(body))
	}
	s.agentsRef = out.Commit.SHA
	return nil
}

// AgentsRef returns the commit of the seeded agent-definition repository.
func (s *Stack) AgentsRef() string { return s.agentsRef }

// AgentSourceEnv returns the environment that makes fullsend resolve
// built-in agents from this stack's seeded repository at its pinned
// commit.
func (s *Stack) AgentSourceEnv() map[string]string {
	return AgentSourceEnvFor(s.url, s.agentsRef)
}

// AgentSourceEnvFor builds the same environment for any Forgejo instance:
// it points fullsend at AgentsOwner/AgentsRepo at commit ref on the
// instance at baseURL.
func AgentSourceEnvFor(baseURL, ref string) map[string]string {
	return map[string]string{
		agentsURLEnv:  strings.TrimSuffix(baseURL, "/"),
		agentsRepoEnv: AgentsOwner + "/" + AgentsRepo,
		agentsRefEnv:  ref,
	}
}
