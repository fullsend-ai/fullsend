package entrypoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	labelReadyForReview = "ready-for-review"
	labelReadyToCode    = "ready-to-code"
	labelRequiresManual = "requires-manual-review"
)

// ReviewOutcome represents the result of a pre-push review.
type ReviewOutcome struct {
	Approved bool
	Summary  string
}

// ReviewReader interprets a review agent's output into a structured outcome.
type ReviewReader interface {
	Read(exitCode int, workspace string) ReviewOutcome
}

// ExitCodeReader interprets exit code 0 as approved, nonzero as rejected.
type ExitCodeReader struct{}

func (r *ExitCodeReader) Read(exitCode int, _ string) ReviewOutcome {
	if exitCode == 0 {
		return ReviewOutcome{Approved: true, Summary: "review passed"}
	}
	return ReviewOutcome{Approved: false, Summary: fmt.Sprintf("review agent exited %d", exitCode)}
}

// CodeResult represents the outcome of a successful code agent run.
type CodeResult struct {
	Branch string
	PRURL  string
}

// RunCode orchestrates the code agent lifecycle:
//  1. Validate workspace (agent definition exists)
//  2. Resolve default branch if needed
//  3. Configure git identity and create branch
//  4. Run code agent
//  5. Check for commits
//  6. Run secret scan
//  7. Run review agent (if available)
//  8. Push branch
//  9. Create or update PR
//  10. Swap labels on issue
func RunCode(ctx context.Context, env *Env, runner Runner, client forge.Client, safeEnv []string) (*CodeResult, error) {
	agentFile := filepath.Join(env.Workspace, env.AgentDir, "code.md")
	if _, err := os.Stat(agentFile); err != nil {
		return nil, fmt.Errorf("agent definition not found: %s", agentFile)
	}

	if env.DefaultBranch == "" {
		repo, err := client.GetRepo(ctx, env.Owner, env.Repo)
		if err != nil {
			return nil, fmt.Errorf("get repo metadata: %w", err)
		}
		env.DefaultBranch = repo.DefaultBranch
	}

	branch := fmt.Sprintf("agent/%d", env.IssueNumber)

	// Configure git identity and create working branch.
	for _, args := range [][]string{
		{"config", "user.name", "fullsend[bot]"},
		{"config", "user.email", "fullsend[bot]@users.noreply.github.com"},
		{"checkout", "-b", branch},
	} {
		code, err := runner.Run(ctx, "git", args, env.Workspace, nil)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		if code != 0 {
			return nil, fmt.Errorf("git %s exited %d", args[0], code)
		}
	}

	// Run code agent.
	prompt := fmt.Sprintf(
		"Implement the changes requested in issue #%d. "+
			"Follow the instructions in the issue description. "+
			"Commit your changes with clear commit messages.",
		env.IssueNumber,
	)
	agentCode, err := runner.Run(ctx, "claude", []string{
		"--agent", filepath.Join(env.AgentDir, "code.md"),
		"--print", prompt,
	}, env.Workspace, safeEnv)
	if err != nil {
		return nil, fmt.Errorf("run code agent: %w", err)
	}
	if agentCode != 0 {
		applyFailureLabel(ctx, client, env)
		return nil, fmt.Errorf("code agent exited %d", agentCode)
	}

	// Check for commits. git diff --quiet returns 0 if no diff (no commits),
	// 1 if diff exists (commits were made), or 128+ for infrastructure errors.
	diffCode, err := runner.Run(ctx, "git", []string{
		"diff", "--quiet", env.DefaultBranch + "..HEAD",
	}, env.Workspace, nil)
	if err != nil {
		return nil, fmt.Errorf("check for commits: %w", err)
	}
	switch diffCode {
	case 0:
		applyFailureLabel(ctx, client, env)
		return nil, fmt.Errorf("code agent produced no commits")
	case 1:
		// has commits, proceed
	default:
		return nil, fmt.Errorf("git diff exited %d", diffCode)
	}

	// Secret scan on new commits.
	leaksCode, err := runner.Run(ctx, "gitleaks", []string{
		"detect", "--source", ".", "--log-opts", env.DefaultBranch + "..HEAD",
	}, env.Workspace, nil)
	if err != nil {
		return nil, fmt.Errorf("run secret scan: %w", err)
	}
	if leaksCode != 0 {
		applyFailureLabel(ctx, client, env)
		_ = client.AddIssueComment(ctx, env.Owner, env.Repo, env.IssueNumber,
			"Secret scan detected potential secrets in agent commits. Requires manual review.")
		return nil, fmt.Errorf("secret scan failed (exit %d)", leaksCode)
	}

	// Pre-push review (graceful degradation if review agent absent).
	reviewFile := filepath.Join(env.Workspace, env.AgentDir, "review.md")
	if _, statErr := os.Stat(reviewFile); statErr == nil {
		reviewCode, err := runner.Run(ctx, "claude", []string{
			"--agent", filepath.Join(env.AgentDir, "review.md"),
			"--print", fmt.Sprintf(
				"Review the changes on branch %s relative to %s. "+
					"Examine the diff and provide your verdict.",
				branch, env.DefaultBranch,
			),
		}, env.Workspace, safeEnv)
		if err != nil {
			return nil, fmt.Errorf("run review agent: %w", err)
		}

		reader := &ExitCodeReader{}
		outcome := reader.Read(reviewCode, env.Workspace)
		if !outcome.Approved {
			applyFailureLabel(ctx, client, env)
			_ = client.AddIssueComment(ctx, env.Owner, env.Repo, env.IssueNumber,
				fmt.Sprintf("Pre-push review rejected: %s", outcome.Summary))
			return nil, fmt.Errorf("review rejected: %s", outcome.Summary)
		}
	}

	// Push branch using bot token.
	authedURL := fmt.Sprintf(
		"https://x-access-token:%s@github.com/%s/%s.git",
		env.BotToken, env.Owner, env.Repo,
	)
	cleanURL := fmt.Sprintf(
		"https://github.com/%s/%s.git",
		env.Owner, env.Repo,
	)
	for _, args := range [][]string{
		{"remote", "set-url", "origin", authedURL},
		{"push", "--set-upstream", "origin", branch},
		{"remote", "set-url", "origin", cleanURL},
	} {
		code, err := runner.Run(ctx, "git", args, env.Workspace, nil)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		if code != 0 {
			return nil, fmt.Errorf("git %s exited %d", args[0], code)
		}
	}

	// Create or update PR.
	var prURL string
	existing, err := client.FindOpenPRByHead(ctx, env.Owner, env.Repo, branch)
	if err != nil && !forge.IsNotFound(err) {
		return nil, fmt.Errorf("find existing PR: %w", err)
	}
	if existing != nil {
		prURL = existing.URL
	} else {
		title := fmt.Sprintf("agent: implement issue #%d", env.IssueNumber)
		body := fmt.Sprintf(
			"Automated implementation for #%d.\n\nGenerated by the fullsend code agent.",
			env.IssueNumber,
		)
		pr, err := client.CreateDraftChangeProposal(
			ctx, env.Owner, env.Repo, title, body, branch, env.DefaultBranch,
		)
		if err != nil {
			return nil, fmt.Errorf("create PR: %w", err)
		}
		prURL = pr.URL
	}

	// Swap labels on issue (best-effort).
	_ = client.AddIssueLabel(ctx, env.Owner, env.Repo, env.IssueNumber, labelReadyForReview)
	_ = client.RemoveIssueLabel(ctx, env.Owner, env.Repo, env.IssueNumber, labelReadyToCode)

	return &CodeResult{
		Branch: branch,
		PRURL:  prURL,
	}, nil
}

// applyFailureLabel applies the requires-manual-review label (best-effort).
func applyFailureLabel(ctx context.Context, client forge.Client, env *Env) {
	_ = client.AddIssueLabel(ctx, env.Owner, env.Repo, env.IssueNumber, labelRequiresManual)
}
