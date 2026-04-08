package entrypoint

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Env holds the parsed GHA environment needed by the code agent entrypoint.
type Env struct {
	Owner         string // parsed from GITHUB_REPOSITORY (before '/')
	Repo          string // parsed from GITHUB_REPOSITORY (after '/')
	IssueNumber   int    // parsed from event payload JSON
	Workspace     string // GITHUB_WORKSPACE
	DefaultBranch string // from repo metadata or GITHUB_REF_NAME fallback
	BotToken      string // FULLSEND_CODE_BOT_TOKEN — never given to agent
	AgentDir      string // default "agents"
}

// LoadEnv reads GHA environment variables and the event payload file to
// construct an Env. It returns an error if any required variable is missing
// or the event payload cannot be parsed.
func LoadEnv() (*Env, error) {
	ghRepo := os.Getenv("GITHUB_REPOSITORY")
	if ghRepo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is not set")
	}
	parts := strings.SplitN(ghRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY %q does not contain '/'", ghRepo)
	}
	owner, repo := parts[0], parts[1]

	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		return nil, fmt.Errorf("GITHUB_WORKSPACE is not set")
	}

	botToken := os.Getenv("FULLSEND_CODE_BOT_TOKEN")
	if botToken == "" {
		return nil, fmt.Errorf("FULLSEND_CODE_BOT_TOKEN is not set")
	}

	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	if eventPath == "" {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH is not set")
	}

	data, err := os.ReadFile(eventPath)
	if err != nil {
		return nil, fmt.Errorf("read event payload: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse event payload: %w", err)
	}

	issueNumber, err := extractIssueNumber(payload)
	if err != nil {
		return nil, err
	}

	defaultBranch := os.Getenv("FULLSEND_DEFAULT_BRANCH")

	agentDir := os.Getenv("FULLSEND_AGENT_DIR")
	if agentDir == "" {
		agentDir = "agents"
	}

	return &Env{
		Owner:         owner,
		Repo:          repo,
		IssueNumber:   issueNumber,
		Workspace:     workspace,
		DefaultBranch: defaultBranch,
		BotToken:      botToken,
		AgentDir:      agentDir,
	}, nil
}

// extractIssueNumber tries to get the issue number from the event payload.
// It checks client_payload.issue_number (float64) first, then
// inputs.issue_number (string).
func extractIssueNumber(payload map[string]any) (int, error) {
	if v, ok := jsonGet(payload, "client_payload", "issue_number"); ok {
		if f, ok := v.(float64); ok {
			return int(f), nil
		}
	}

	if v, ok := jsonGet(payload, "inputs", "issue_number"); ok {
		if s, ok := v.(string); ok {
			n, err := strconv.Atoi(s)
			if err != nil {
				return 0, fmt.Errorf("parse inputs.issue_number %q: %w", s, err)
			}
			return n, nil
		}
	}

	return 0, fmt.Errorf("event payload missing issue number at client_payload.issue_number or inputs.issue_number")
}

// jsonGet navigates nested map[string]any using the given keys.
func jsonGet(m map[string]any, keys ...string) (any, bool) {
	var current any = m
	for _, k := range keys {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = cm[k]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
