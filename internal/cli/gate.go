package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Pre-agent gates that run before sandbox creation",
		Long: `Gates validate inputs and check preconditions on the GitHub Actions
runner BEFORE sandbox creation. Each subcommand corresponds to an
agent stage (code, triage, review).`,
	}
	cmd.AddCommand(newGateCodeCmd())
	return cmd
}

func newGateCodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "code",
		Short: "Gate for the code agent — validates inputs and checks for existing PRs",
		Long: `Validates workflow_dispatch inputs and checks whether a human PR
already addresses the target issue. If a human PR exists, applies
the pr-open label, posts an informational comment, and writes
skip=true to GITHUB_OUTPUT so downstream workflow steps can be
conditionally skipped.

Required environment variables:
  ISSUE_NUMBER       — positive integer
  REPO_FULL_NAME     — owner/repo format
  GITHUB_ISSUE_URL   — valid GitHub issue URL

Optional:
  GH_TOKEN / GITHUB_TOKEN — required for PR check
  CODE_FORCE=true          — skip PR check
  FULLSEND_BOT_LOGIN       — bot login to filter (default: fullsend-ai[bot])`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			cfg := gateCodeConfig{
				IssueNumber:  os.Getenv("ISSUE_NUMBER"),
				RepoFullName: os.Getenv("REPO_FULL_NAME"),
				IssueURL:     os.Getenv("GITHUB_ISSUE_URL"),
				BotLogin:     os.Getenv("FULLSEND_BOT_LOGIN"),
				Force:        os.Getenv("CODE_FORCE") == "true",
				OutputFile:   os.Getenv("GITHUB_OUTPUT"),
			}

			token, _ := resolveToken()
			if token != "" {
				cfg.Client = gh.New(token)
			}

			return runGateCode(cmd.Context(), cfg, printer)
		},
	}
}

type gateCodeConfig struct {
	IssueNumber  string
	RepoFullName string
	IssueURL     string
	BotLogin     string
	Force        bool
	Client       forge.Client
	OutputFile   string // GITHUB_OUTPUT path for writing step outputs
}

var (
	issueNumberRe  = regexp.MustCompile(`^[1-9][0-9]*$`)
	repoFullNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`)
	issueURLRe     = regexp.MustCompile(`^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$`)
	botLoginRe     = regexp.MustCompile(`^[][a-zA-Z0-9._-]+$`)
)

func validateGateCodeInputs(issueNumber, repoFullName, issueURL string) []string {
	var errs []string

	if !issueNumberRe.MatchString(issueNumber) {
		errs = append(errs, fmt.Sprintf("ISSUE_NUMBER must be a positive integer, got: '%s'", issueNumber))
	}
	if !repoFullNameRe.MatchString(repoFullName) {
		errs = append(errs, fmt.Sprintf("REPO_FULL_NAME must be owner/repo format, got: '%s'", repoFullName))
	}
	if !issueURLRe.MatchString(issueURL) {
		errs = append(errs, fmt.Sprintf("GITHUB_ISSUE_URL format invalid, got: '%s'", issueURL))
	}

	if issueURLRe.MatchString(issueURL) && repoFullNameRe.MatchString(repoFullName) {
		u, err := url.Parse(issueURL)
		if err == nil {
			parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
			if len(parts) >= 4 {
				urlRepo := parts[0] + "/" + parts[1]
				urlIssue := parts[3]
				if urlRepo != repoFullName {
					errs = append(errs, fmt.Sprintf("REPO_FULL_NAME does not match issue URL repo ('%s' vs '%s')", repoFullName, urlRepo))
				}
				if urlIssue != issueNumber {
					errs = append(errs, fmt.Sprintf("ISSUE_NUMBER does not match issue URL number ('%s' vs '%s')", issueNumber, urlIssue))
				}
			}
		}
	}

	return errs
}

func runGateCode(ctx context.Context, cfg gateCodeConfig, printer *ui.Printer) error {
	printer.Raw(fmt.Sprintf("::notice::Code target: %s\n", cfg.IssueURL))

	errs := validateGateCodeInputs(cfg.IssueNumber, cfg.RepoFullName, cfg.IssueURL)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.Raw(fmt.Sprintf("::error::%s\n", e))
		}
		return fmt.Errorf("input validation failed with %d error(s)", len(errs))
	}

	printer.StepDone("Input validation passed")
	printer.KeyValue("ISSUE_NUMBER", cfg.IssueNumber)
	printer.KeyValue("REPO_FULL_NAME", cfg.RepoFullName)
	printer.KeyValue("GITHUB_ISSUE_URL", cfg.IssueURL)

	if cfg.Client == nil {
		printer.StepInfo("GH_TOKEN not set — skipping existing-PR check")
		return nil
	}

	if cfg.Force {
		printer.StepInfo("CODE_FORCE=true — skipping existing-PR check")
		return nil
	}

	botLogin := cfg.BotLogin
	if botLogin == "" {
		botLogin = "fullsend-ai[bot]"
	}
	if !botLoginRe.MatchString(botLogin) {
		printer.Raw(fmt.Sprintf("::error::FULLSEND_BOT_LOGIN contains invalid characters: '%s'\n", botLogin))
		return fmt.Errorf("FULLSEND_BOT_LOGIN contains invalid characters: '%s'", botLogin)
	}

	parts := strings.SplitN(cfg.RepoFullName, "/", 2)
	owner, repo := parts[0], parts[1]
	issueNum, _ := strconv.Atoi(cfg.IssueNumber)

	printer.StepStart(fmt.Sprintf("Checking for existing open PRs linked to issue #%d...", issueNum))

	events, err := cfg.Client.ListIssueTimeline(ctx, owner, repo, issueNum)
	if err != nil {
		printer.Raw(fmt.Sprintf("::warning::Failed to check for existing PRs: %v\n", err))
		printer.StepInfo("No existing human PRs found — proceeding with code agent")
		return nil
	}

	var humanPRs []forge.TimelineEvent
	for _, e := range events {
		if e.PRState == "open" && e.PRAuthor != botLogin {
			humanPRs = append(humanPRs, e)
		}
	}

	if len(humanPRs) == 0 {
		printer.StepDone("No existing human PRs found — proceeding with code agent")
		return nil
	}

	first := humanPRs[0]
	printer.Raw(fmt.Sprintf("::notice::Found existing human PR #%d by @%s\n", first.PRNumber, first.PRAuthor))

	_ = cfg.Client.EnsureLabel(ctx, owner, repo, "pr-open", "An open PR already addresses this issue", "D4C5F9")
	_ = cfg.Client.AddIssueLabels(ctx, owner, repo, issueNum, []string{"pr-open"})

	var prListMD string
	for _, pr := range humanPRs {
		prListMD += fmt.Sprintf("\n- #%d by @%s", pr.PRNumber, pr.PRAuthor)
	}

	commentBody := fmt.Sprintf(`An open PR already addresses this issue — skipping automated implementation.
%s

To override, comment `+"`/code --force`"+` on this issue.

<sub>Posted by <a href="https://github.com/fullsend-ai/fullsend">fullsend</a> pre-code check</sub>`, prListMD)

	comments, err := cfg.Client.ListIssueComments(ctx, owner, repo, issueNum)
	if err != nil {
		comments = nil
	}

	hasExisting := false
	for _, c := range comments {
		if strings.HasPrefix(c.Body, "An open PR already addresses") {
			hasExisting = true
			break
		}
	}

	if !hasExisting {
		_ = cfg.Client.AddIssueComment(ctx, owner, repo, issueNum, commentBody)
	} else {
		printer.Raw(fmt.Sprintf("::notice::Skipping duplicate comment — bot already posted on issue #%d\n", issueNum))
	}

	writeGateOutput(cfg.OutputFile, "skip", "true")

	printer.StepDone(fmt.Sprintf("Skipping code agent — existing PR(s) found for issue #%d", issueNum))
	return nil
}

func writeGateOutput(path, key, value string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s=%s\n", key, value)
}
