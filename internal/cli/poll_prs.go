package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/prpoll"
)

// prPollInputDriver is the --input-driver value of the fork PR review poll
// (ADR 0171).
const prPollInputDriver = "github-pr-poll"

// prPollClient is the GitHub client the fork PR review poll uses: the poll
// operations plus the per-client API call counter for the summary line.
type prPollClient interface {
	prpoll.Client
	APICalls() map[string]int64
}

// newPRPollClientFn builds the poll's GitHub client from GITHUB_TOKEN.
// Override in tests to supply a fake client.
var newPRPollClientFn = func(token string) prPollClient {
	return newConfiguredHostGitHubClient(token)
}

// prPollFlags are the github-pr-poll driver's flags.
type prPollFlags struct {
	targetRepo  string
	outputPath  string
	fullsendDir string
	since       string
	// stateFile is the JSON file of per-head state kept across cycles;
	// empty keeps no state.
	stateFile string
	// defaultBranch is the repository's default branch; empty reads it
	// from the repository.
	defaultBranch string
}

// prPollStateChangedSuffix names the marker file written next to the
// state file when a cycle changed the state, so the workflow saves it.
const prPollStateChangedSuffix = ".changed"

// runGitHubPRPoll is the github-pr-poll input driver: one fork PR review
// poll cycle that writes the review dispatches as a JSON array to
// --output, for reusable-dispatch.yml. When the poll is disabled, the kill
// switch is on or the review agent is disabled, it writes [] without
// contacting GitHub. The job holds a read-only GITHUB_TOKEN.
//
// With --state-file it reads the per-head state before the cycle and, when
// the cycle changed it, writes it back and creates <state-file>.changed;
// it logs "state: changed" or "state: unchanged".
func runGitHubPRPoll(cmd *cobra.Command, flags prPollFlags) error {
	targetRepo := flags.targetRepo
	if targetRepo == "" {
		targetRepo = os.Getenv("GITHUB_REPOSITORY")
	}
	if targetRepo == "" {
		return fmt.Errorf("--target-repo or GITHUB_REPOSITORY is required")
	}
	owner, repo, ok := strings.Cut(targetRepo, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || strings.Contains(targetRepo, "..") || strings.ContainsAny(targetRepo, " \t\n") {
		return fmt.Errorf("--target-repo %q must be an owner/repo slug", targetRepo)
	}
	if flags.outputPath == "" {
		return fmt.Errorf("--output is required for --input-driver %s", prPollInputDriver)
	}
	if flags.stateFile != "" {
		// A stale marker would make the workflow save an unchanged state.
		if err := os.Remove(flags.stateFile + prPollStateChangedSuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove poll state marker: %w", err)
		}
	}

	cfg, err := config.LoadConfig(flags.fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	stderr := cmd.ErrOrStderr()
	var pollCfg *config.ForkPRReviewPollConfig
	perRepo, isPerRepo := cfg.(config.PerRepoConfigReader)
	if isPerRepo {
		pollCfg = perRepo.ForkPRReviewPoll()
	}
	switch {
	case !pollCfg.PollEnabled():
		fmt.Fprintln(stderr, "fork PR review poll is disabled (fork_pr_review_poll.enabled is not true in config.yaml); skipping the poll cycle")
		return writePRPollDispatches(flags.outputPath, nil)
	case cfg.IsKillSwitchActive():
		fmt.Fprintln(stderr, "kill switch is active; skipping the fork PR review poll cycle")
		return writePRPollDispatches(flags.outputPath, nil)
	case config.IsAgentExplicitlyDisabled(cfg.AgentEntries(), "review"):
		fmt.Fprintln(stderr, "the review agent is disabled; skipping the fork PR review poll cycle")
		return writePRPollDispatches(flags.outputPath, nil)
	}
	// Config loading does not run per-repo validation, so enforce the
	// documented bounds here before they size a cycle.
	if err := config.ValidateForkPRReviewPoll(pollCfg); err != nil {
		return fmt.Errorf("--input-driver %s: %w", prPollInputDriver, err)
	}

	if flags.since == "" {
		return fmt.Errorf("--since is required for --input-driver %s: it is the time the poll was enabled, stamped in the poll caller workflow", prPollInputDriver)
	}
	since, err := time.Parse(time.RFC3339, flags.since)
	if err != nil {
		return fmt.Errorf("--since %q must be an RFC 3339 timestamp", flags.since)
	}

	// Without the review App's client ID the poll cannot tell the App's
	// comments from anyone else's, so it cannot see finished or running
	// reviews. Fail the job, writing no dispatch file, rather than start
	// duplicate reviews.
	reviewClientID := os.Getenv(forge.VarReviewClientID)
	if reviewClientID == "" {
		return fmt.Errorf("--input-driver %s: %w", prPollInputDriver, prpoll.ErrNoReviewAppClientID)
	}

	// The poll job's read-only GITHUB_TOKEN, through the CLI's shared
	// credential resolution.
	token, err := resolveGitHubToken("")
	if err != nil {
		return fmt.Errorf("--input-driver %s: %w", prPollInputDriver, err)
	}
	client := newPRPollClientFn(token)

	opts := prpoll.Options{
		Owner:             owner,
		Repo:              repo,
		Since:             since,
		MaxStarts:         pollCfg.EffectiveMaxStartsPerCycle(),
		MaxAttempts:       pollCfg.EffectiveMaxAttemptsPerHead(),
		DefaultBranch:     flags.defaultBranch,
		ReviewBotLogins:   reviewBotLogins(owner, os.Getenv(forge.VarAppSet)),
		ReviewAppClientID: reviewClientID,
		Log:               stderr,
		APICalls: func() int64 {
			var total int64
			for _, n := range client.APICalls() {
				total += n
			}
			return total
		},
	}
	opts.Authorize = prPollAuthorizer(flags.fullsendDir, cfg)
	if flags.stateFile != "" {
		opts.State = prpoll.LoadState(flags.stateFile, stderr)
	} else {
		fmt.Fprintln(stderr, "warning: no --state-file; dispatch counts are not kept across poll cycles")
	}

	refs, err := prpoll.New(client, opts).Run(cmd.Context())
	if err != nil {
		return err
	}
	if err := writePRPollDispatches(flags.outputPath, refs); err != nil {
		return err
	}
	if flags.stateFile == "" {
		return nil
	}
	return savePRPollState(flags.stateFile, opts.State, stderr)
}

// savePRPollState writes state to path and the <path>.changed marker when
// the cycle changed it, and logs "state: changed" or "state: unchanged".
func savePRPollState(path string, state *prpoll.State, log io.Writer) error {
	if !state.Changed() {
		fmt.Fprintln(log, "state: unchanged")
		return nil
	}
	if err := state.Save(path); err != nil {
		return err
	}
	if err := os.WriteFile(path+prPollStateChangedSuffix, []byte("changed\n"), 0o644); err != nil {
		return fmt.Errorf("write poll state marker: %w", err)
	}
	fmt.Fprintf(log, "state: changed (%d heads)\n", len(state.Heads))
	return nil
}

// reviewBotLogins returns the logins of fullsend's review App for a
// repository owned by owner: the org-specific App, the shared vendor App
// and, when FULLSEND_APP_SET is set, the custom app-set App (as
// pre-fetch-prior-review.sh matches them).
func reviewBotLogins(owner, appSet string) []string {
	logins := []string{owner + "-review[bot]", "fullsend-ai-review[bot]"}
	if appSet != "" {
		logins = append(logins, appSet+"-review[bot]")
	}
	return logins
}

// prPollAuthorizer returns the poll's authorization gate: the shared
// dispatch core (ADR 0054, authorization contract v1) with the OWNERS
// upgrade of harnessdispatch.AuthCheckEvent, for the review harness.
func prPollAuthorizer(fullsendDir string, cfg config.ConfigReader) func(*normevent.Event) bool {
	configDir := filepath.Clean(fullsendDir)
	authOpts := harnessdispatch.AuthOptionsFor(cfg)
	return func(ev *normevent.Event) bool {
		chk := harnessdispatch.AuthCheckEvent(configDir, cfg, ev)
		return harnessdispatch.IsHarnessAuthorizedWith(&chk, "review", authOpts)
	}
}

// writePRPollDispatches writes refs to path as a JSON array, [] when empty.
func writePRPollDispatches(path string, refs []harnessdispatch.ExecutionRef) error {
	data := []byte("[]\n")
	if len(refs) > 0 {
		encoded, err := json.MarshalIndent(refs, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal dispatches: %w", err)
		}
		data = append(encoded, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write dispatches: %w", err)
	}
	return nil
}
