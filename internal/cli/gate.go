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
	cmd.AddCommand(newGateFixCmd())
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

			token, err := resolveToken()
			if err != nil {
				printer.Raw(fmt.Sprintf("::warning::No GitHub token available: %v\n", err))
			}
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
	botLoginRe     = regexp.MustCompile(`^[a-zA-Z0-9._-]+(\[bot\])?$`)
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
	errs := validateGateCodeInputs(cfg.IssueNumber, cfg.RepoFullName, cfg.IssueURL)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.Raw(fmt.Sprintf("::error::%s\n", e))
		}
		return fmt.Errorf("input validation failed with %d error(s)", len(errs))
	}

	printer.Raw(fmt.Sprintf("::notice::Code target: %s\n", cfg.IssueURL))
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

	if err := cfg.Client.EnsureLabel(ctx, owner, repo, "pr-open", "An open PR already addresses this issue", "D4C5F9"); err != nil {
		printer.Raw(fmt.Sprintf("::warning::Failed to ensure pr-open label: %v\n", err))
	}
	if err := cfg.Client.AddIssueLabels(ctx, owner, repo, issueNum, []string{"pr-open"}); err != nil {
		printer.Raw(fmt.Sprintf("::warning::Failed to add pr-open label to issue #%d: %v\n", issueNum, err))
	}

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
		printer.Raw(fmt.Sprintf("::warning::Failed to check existing comments on issue #%d: %v\n", issueNum, err))
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
		if _, err := cfg.Client.CreateIssueComment(ctx, owner, repo, issueNum, commentBody); err != nil {
			printer.Raw(fmt.Sprintf("::warning::Failed to post comment on issue #%d: %v\n", issueNum, err))
		}
	} else {
		printer.Raw(fmt.Sprintf("::notice::Skipping duplicate comment — bot already posted on issue #%d\n", issueNum))
	}

	if err := writeGateOutput(cfg.OutputFile, "skip", "true"); err != nil {
		printer.Raw(fmt.Sprintf("::warning::Failed to write GITHUB_OUTPUT: %v\n", err))
	}

	printer.StepDone(fmt.Sprintf("Skipping code agent — existing PR(s) found for issue #%d", issueNum))
	return nil
}

func newGateFixCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fix",
		Short: "Gate for the fix agent — validates inputs and enforces iteration caps",
		Long: `Validates workflow_dispatch inputs for the fix agent and enforces
iteration caps to prevent unbounded fix loops.

Required environment variables:
  PR_NUMBER          — positive integer
  REPO_FULL_NAME     — owner/repo format
  TRIGGER_SOURCE     — GitHub username that triggered the fix

Optional:
  FIX_ITERATION      — current iteration count (default: 1)
  ITERATION_CAP      — max bot-triggered iterations (default: 5)
  ITERATION_CAP_HUMAN — max human-triggered iterations (default: 10)
  HUMAN_INSTRUCTION  — instruction text (validated for length)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			cfg := gateFixConfig{
				PRNumber:         os.Getenv("PR_NUMBER"),
				RepoFullName:     os.Getenv("REPO_FULL_NAME"),
				TriggerSource:    os.Getenv("TRIGGER_SOURCE"),
				Iteration:        os.Getenv("FIX_ITERATION"),
				IterationCap:     os.Getenv("ITERATION_CAP"),
				IterationCapHuman: os.Getenv("ITERATION_CAP_HUMAN"),
				HumanInstruction: os.Getenv("HUMAN_INSTRUCTION"),
			}

			return runGateFix(cmd.Context(), cfg, printer)
		},
	}
}

type gateFixConfig struct {
	PRNumber         string
	RepoFullName     string
	TriggerSource    string
	Iteration        string
	IterationCap     string
	IterationCapHuman string
	HumanInstruction string
}

const (
	maxInstructionBytes    = 10000
	maxBotInstructionBytes = 1048576 // 1 MB — matches review body cap in fix.yml
)

func isBotUser(username string) bool {
	return strings.HasSuffix(username, "[bot]")
}

func parsePositiveIntOrDefault(envName, raw string, defaultVal int) (int, error) {
	if raw == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer, got: '%s'", envName, raw)
	}
	if v < 1 {
		return 0, fmt.Errorf("%s must be positive, got: %d", envName, v)
	}
	return v, nil
}

var triggerSourceRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+(\[bot\])?$`)

func validateGateFixInputs(prNumber, repoFullName, triggerSource string) []string {
	var errs []string

	if !issueNumberRe.MatchString(prNumber) {
		errs = append(errs, fmt.Sprintf("PR_NUMBER must be a positive integer, got: '%s'", prNumber))
	}
	if !repoFullNameRe.MatchString(repoFullName) {
		errs = append(errs, fmt.Sprintf("REPO_FULL_NAME must be owner/repo format, got: '%s'", repoFullName))
	}
	if triggerSource == "" {
		errs = append(errs, "TRIGGER_SOURCE is required (GitHub username that triggered the fix)")
	} else if !triggerSourceRe.MatchString(triggerSource) {
		errs = append(errs, fmt.Sprintf("TRIGGER_SOURCE must be a valid GitHub username, got: '%s'", triggerSource))
	}

	return errs
}

func runGateFix(_ context.Context, cfg gateFixConfig, printer *ui.Printer) error {
	errs := validateGateFixInputs(cfg.PRNumber, cfg.RepoFullName, cfg.TriggerSource)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.Raw(fmt.Sprintf("::error::%s\n", e))
		}
		return fmt.Errorf("input validation failed with %d error(s)", len(errs))
	}

	// Instruction length cap — lower for humans, higher for bots.
	instrCap := maxInstructionBytes
	if isBotUser(cfg.TriggerSource) {
		instrCap = maxBotInstructionBytes
	}
	if len(cfg.HumanInstruction) > instrCap {
		printer.Raw(fmt.Sprintf("::error::HUMAN_INSTRUCTION is %d bytes (max: %d). Truncate the instruction.\n",
			len(cfg.HumanInstruction), instrCap))
		return fmt.Errorf("HUMAN_INSTRUCTION is %d bytes (max: %d)", len(cfg.HumanInstruction), instrCap)
	}

	iteration, err := parsePositiveIntOrDefault("FIX_ITERATION", cfg.Iteration, 1)
	if err != nil {
		printer.Raw(fmt.Sprintf("::error::%v\n", err))
		return err
	}

	botCap, err := parsePositiveIntOrDefault("ITERATION_CAP", cfg.IterationCap, 5)
	if err != nil {
		printer.Raw(fmt.Sprintf("::error::%v\n", err))
		return err
	}

	humanCap, err := parsePositiveIntOrDefault("ITERATION_CAP_HUMAN", cfg.IterationCapHuman, 10)
	if err != nil {
		printer.Raw(fmt.Sprintf("::error::%v\n", err))
		return err
	}

	cap := humanCap
	if isBotUser(cfg.TriggerSource) {
		cap = botCap
	}

	if iteration > cap {
		if isBotUser(cfg.TriggerSource) {
			printer.Raw(fmt.Sprintf("::error::Fix iteration %d exceeds bot cap of %d. Escalating to human.\n", iteration, cap))
			printer.Raw(fmt.Sprintf("::error::The review→fix loop has run %d times without converging.\n", iteration))
			printer.Raw(fmt.Sprintf("::error::A human can still direct the agent with /fix (up to %d total iterations).\n", humanCap))
		} else {
			printer.Raw(fmt.Sprintf("::error::Fix iteration %d exceeds human cap of %d.\n", iteration, cap))
			printer.Raw(fmt.Sprintf("::error::The /fix loop has run %d times. Further attempts are blocked.\n", iteration))
		}
		return fmt.Errorf("fix iteration %d exceeds cap of %d", iteration, cap)
	}

	printer.StepDone("Input validation passed")
	printer.KeyValue("PR_NUMBER", cfg.PRNumber)
	printer.KeyValue("REPO_FULL_NAME", cfg.RepoFullName)
	printer.KeyValue("TRIGGER_SOURCE", cfg.TriggerSource)
	printer.KeyValue("FIX_ITERATION", fmt.Sprintf("%d of %d", iteration, cap))
	if !isBotUser(cfg.TriggerSource) && cfg.HumanInstruction != "" {
		preview := cfg.HumanInstruction
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		printer.KeyValue("HUMAN_INSTRUCTION", preview)
	}

	return nil
}

func writeGateOutput(path, key, value string) error {
	if path == "" {
		return nil
	}
	if strings.ContainsAny(key, "\n\r") || strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("GITHUB_OUTPUT key/value must not contain newlines")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "%s=%s\n", key, value)
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}
