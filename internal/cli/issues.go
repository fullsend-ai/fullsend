package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/sticky"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// knownTrackers is the set of recognized --tracker values. Used for
// early validation so a typo or case mismatch produces a clear error
// before reaching newTrackerClient.
var knownTrackers = []string{trackerGitHub, trackerGitLab, trackerJira}

func newIssuesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issues",
		Short: "Read and write issue content across trackers",
		Long: `Commands for reading and writing issue content (title, body,
comments, labels) across GitHub, GitLab, and Jira.

Use --tracker to select the tracker backend. For GitHub and GitLab,
--project is "owner/repo". For Jira, --project is the project key
(e.g. "PROJ") and the issue is addressed as PROJ-<number>.

--tracker is required unless a default is supplied via config: set
"tracker: github|gitlab|jira" in config.yaml and pass --fullsend-dir
pointing at the directory containing it.`,
	}
	cmd.AddCommand(newIssuesGetCmd())
	cmd.AddCommand(newIssuesPostCommentCmd())
	return cmd
}

// issueGetResult is the JSON output of "fullsend issues get".
type issueGetResult struct {
	Number   int                     `json:"number"`
	Title    string                  `json:"title"`
	Body     string                  `json:"body"`
	URL      string                  `json:"url"`
	Labels   []string                `json:"labels"`
	Comments []issueCommentGetResult `json:"comments"`
}

type issueCommentGetResult struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url,omitempty"`
}

// issuesGetConfig holds the flags and test overrides for "fullsend issues get".
type issuesGetConfig struct {
	trackerName string
	project     string
	number      int
	token       string
	jiraURL     string
	jiraEmail   string
	fullsendDir string

	// Test overrides — when non-nil, used instead of creating a real
	// tracker client. Not set by CLI flag parsing.
	testClient       tracker.Client
	testWriter       io.Writer
	testConfigReader config.PerRepoConfigReader
}

func newIssuesGetCmd() *cobra.Command {
	var cfg issuesGetConfig

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Read issue content from a tracker",
		Long: `Reads an issue's title, body, labels, and comments from the
specified tracker (GitHub, GitLab, or Jira) and prints them as JSON.

For GitHub/GitLab, --project is "owner/repo". For Jira, --project is
the Jira project key (e.g. "PROJ") and the issue number maps to
PROJ-<number>.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIssuesGet(cmd.Context(), &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.trackerName, "tracker", "", "tracker backend: github, gitlab, or jira (required unless a default is set via config)")
	cmd.Flags().StringVar(&cfg.project, "project", "", "project identifier: owner/repo (GitHub/GitLab) or project key (Jira) (required)")
	cmd.Flags().IntVar(&cfg.number, "number", 0, "issue number (required)")
	cmd.Flags().StringVar(&cfg.token, "token", "", "API token (default: env var per tracker)")
	cmd.Flags().StringVar(&cfg.jiraURL, "jira-url", "", "Jira instance URL (default: $JIRA_BASE_URL)")
	cmd.Flags().StringVar(&cfg.jiraEmail, "jira-email", "", "Jira user email for Basic auth (default: $JIRA_USER_EMAIL)")
	cmd.Flags().StringVar(&cfg.fullsendDir, "fullsend-dir", "", "path to .fullsend config directory (sources a default --tracker from its config.yaml when --tracker is omitted)")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("number")

	return cmd
}

func runIssuesGet(ctx context.Context, cfg *issuesGetConfig) error {
	if cfg.number <= 0 {
		return fmt.Errorf("--number must be a positive integer, got %d", cfg.number)
	}

	tc := cfg.testClient
	if tc == nil {
		trackerName, err := resolveTracker(cfg.trackerName, cfg.fullsendDir, cfg.testConfigReader)
		if err != nil {
			return err
		}
		tc, err = newTrackerClient(trackerName, cfg.token, cfg.jiraURL, cfg.jiraEmail)
		if err != nil {
			return err
		}
	}

	issue, err := tc.GetIssue(ctx, cfg.project, cfg.number)
	if err != nil {
		return fmt.Errorf("getting issue: %w", err)
	}

	comments, err := tc.ListComments(ctx, cfg.project, cfg.number)
	if err != nil {
		return fmt.Errorf("listing comments: %w", err)
	}

	labels := issue.Labels
	if labels == nil {
		labels = []string{}
	}
	result := issueGetResult{
		Number:   issue.Number,
		Title:    issue.Title,
		Body:     string(issue.Body),
		URL:      issue.URL,
		Labels:   labels,
		Comments: make([]issueCommentGetResult, len(comments)),
	}
	for i, c := range comments {
		result.Comments[i] = issueCommentGetResult{
			ID:        c.ID,
			Author:    c.Author,
			Body:      string(c.Body),
			CreatedAt: c.CreatedAt,
			HTMLURL:   c.HTMLURL,
		}
	}

	w := cfg.testWriter
	if w == nil {
		w = os.Stdout
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// issuesPostCommentConfig holds the flags and test overrides for
// "fullsend issues post-comment".
type issuesPostCommentConfig struct {
	trackerName string
	project     string
	number      int
	marker      string
	result      string
	token       string
	jiraURL     string
	jiraEmail   string
	dryRun      bool
	fullsendDir string

	// Test overrides — when non-nil, used instead of creating a real
	// tracker client. Not set by CLI flag parsing.
	testClient       tracker.Client
	testPrinter      *ui.Printer
	testBody         string // when non-empty, used instead of reading from result/stdin
	testConfigReader config.PerRepoConfigReader
}

func newIssuesPostCommentCmd() *cobra.Command {
	var cfg issuesPostCommentConfig

	cmd := &cobra.Command{
		Use:   "post-comment",
		Short: "Post or update a sticky comment on an issue",
		Long: `Posts a comment with a sticky marker on an issue. On first
run, creates a new comment. On re-runs, finds the existing comment
by its marker and edits in-place, collapsing old content into
<details> blocks. This prevents comment flooding on re-runs.

Works across GitHub, GitLab, and Jira via --tracker.

The --marker flag identifies this agent's comments. Each agent
should use a unique marker (e.g. "<!-- fullsend:triage-agent -->").
For --tracker jira, the marker is stored as an invisible comment
entity property rather than embedded in the visible comment body,
so marker character restrictions do not apply.

Trust model: marker-based comment lookup does not verify the comment
author. In a trusted CI environment (the intended deployment) this
is safe because only the bot writes marker-bearing comments. If
untrusted users can post issue comments containing the marker
string, they could cause the bot to edit their comment instead of
creating its own. Do not use this command in environments where
untrusted users can write arbitrary issue comments bearing your
marker.

--tracker is required unless a default is supplied via config: set
"tracker: github|gitlab|jira" in config.yaml and pass --fullsend-dir
pointing at the directory containing it.

The --result flag accepts a file path or "-" for stdin.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIssuesPostComment(cmd.Context(), &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.trackerName, "tracker", "", "tracker backend: github, gitlab, or jira (required unless a default is set via config)")
	cmd.Flags().StringVar(&cfg.project, "project", "", "project identifier: owner/repo (GitHub/GitLab) or project key (Jira) (required)")
	cmd.Flags().IntVar(&cfg.number, "number", 0, "issue number (required)")
	cmd.Flags().StringVar(&cfg.marker, "marker", "", "sticky marker to identify this agent's comments (required)")
	cmd.Flags().StringVar(&cfg.result, "result", "-", "path to comment body file, or '-' for stdin")
	cmd.Flags().StringVar(&cfg.token, "token", "", "API token (default: env var per tracker)")
	cmd.Flags().StringVar(&cfg.jiraURL, "jira-url", "", "Jira instance URL (default: $JIRA_BASE_URL)")
	cmd.Flags().StringVar(&cfg.jiraEmail, "jira-email", "", "Jira user email for Basic auth (default: $JIRA_USER_EMAIL)")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "print what would be posted without making API calls")
	cmd.Flags().StringVar(&cfg.fullsendDir, "fullsend-dir", "", "path to .fullsend config directory (sources a default --tracker from its config.yaml when --tracker is omitted)")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("marker")

	return cmd
}

func runIssuesPostComment(ctx context.Context, cfg *issuesPostCommentConfig) error {
	printer := cfg.testPrinter
	if printer == nil {
		printer = ui.New(os.Stdout)
	}

	if cfg.number <= 0 {
		return fmt.Errorf("--number must be a positive integer, got %d", cfg.number)
	}
	if strings.TrimSpace(cfg.marker) == "" {
		return fmt.Errorf("--marker must not be empty")
	}

	trackerName, err := resolveTracker(cfg.trackerName, cfg.fullsendDir, cfg.testConfigReader)
	if err != nil {
		return err
	}

	body := cfg.testBody
	if body == "" {
		body, err = readBody(cfg.result)
		if err != nil {
			return fmt.Errorf("reading comment body: %w", err)
		}
	}

	tc := cfg.testClient
	if tc == nil {
		tc, err = newTrackerClient(trackerName, cfg.token, cfg.jiraURL, cfg.jiraEmail)
		if err != nil {
			return err
		}
	}

	printer.Header("Post Comment")

	stickyCfg := sticky.Config{
		Marker: cfg.marker,
		DryRun: cfg.dryRun,
	}
	if trackerName == trackerJira {
		// The Jira write path routes every body through
		// jira.MarkdownToADF, which hard-rejects input over
		// jira.MaxMarkdownBytes. Sticky's default max size (65000,
		// derived from GitHub's comment cap) is well over that, so an
		// accumulated sticky body that grows past the Jira limit would
		// otherwise pass sticky's trim and only fail once it reaches
		// Jira. Capping here keeps sticky's own trimming in charge of
		// staying under the limit that will actually be enforced.
		stickyCfg.MaxSize = jira.MaxMarkdownBytes
	}

	// Jira stores sticky markers as comment entity properties instead
	// of embedding them in the visible ADF body (Jira has no HTML
	// comments — the marker would be visible to users). When the
	// tracker.Client is a *tracker.JiraClient, use the property-based
	// path; otherwise fall back to the body-embedded path used by
	// GitHub/GitLab.
	if jc, ok := tc.(*tracker.JiraClient); ok {
		_, err = postJiraStickyComment(ctx, jc, cfg.project, cfg.number, body, stickyCfg, printer)
	} else {
		_, err = postTrackerStickyComment(ctx, tc, cfg.project, cfg.number, body, stickyCfg, printer)
	}
	return err
}

// postJiraStickyComment implements the sticky comment lifecycle for Jira
// using comment entity properties to store the marker, keeping it out
// of the visible ADF body. Jira has no HTML comment equivalent, so
// without this the marker would be visible to users.
//
// On create, the marker is stored as a comment property via the Jira
// comment-create API's properties array. On lookup, comments are fetched
// with ?expand=properties and matched by property value. On update, the
// property is (re)set to handle legacy migration from body-embedded
// markers.
func postJiraStickyComment(ctx context.Context, jc *tracker.JiraClient, project string, number int, body string, cfg sticky.Config, printer *ui.Printer) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("comment body is empty")
	}
	if strings.TrimSpace(cfg.Marker) == "" {
		return "", fmt.Errorf("marker is empty")
	}

	jiraComments, err := jc.ListJiraComments(ctx, project, number)
	if err != nil {
		return "", fmt.Errorf("listing comments: %w", err)
	}

	existing := jc.FindCommentByMarkerProperty(jiraComments, cfg.Marker)

	if existing != nil {
		printer.StepStart("Found existing comment, updating in-place")

		// Build the updated body using sticky's history-collapsing
		// logic. The old body is read from Jira as Markdown (via
		// ADFToMarkdown). BuildUpdatedBody uses cfg.Marker to strip
		// the marker from oldBody (handling both legacy body-embedded
		// markers and the property-based path where oldBody has no
		// marker). The new body is passed without the marker prefix
		// because the marker lives in a property, not the visible body.
		oldBody := jira.ADFToMarkdown(existing.Body)
		newBody := sticky.BuildUpdatedBody(oldBody, body, cfg)

		if cfg.DryRun {
			printer.StepInfo("Dry run — would update comment " + existing.ID)
			printer.StepInfo(fmt.Sprintf("Body length: %d", len(newBody)))
			return "", nil
		}

		if err := jc.MigrateAndUpdateComment(ctx, project, number, existing.ID, tracker.Body(newBody), cfg.Marker); err != nil {
			return "", fmt.Errorf("updating comment: %w", err)
		}
		printer.StepDone("Comment updated")
		return "", nil // Jira has no stable comment permalink
	}

	printer.StepStart("No existing comment found, creating new one")

	if cfg.DryRun {
		printer.StepInfo("Dry run — would create new comment")
		printer.StepInfo(fmt.Sprintf("Body length: %d", len(body)))
		return "", nil
	}

	created, err := jc.CreateCommentWithMarker(ctx, project, number, tracker.Body(body), cfg.Marker)
	if err != nil {
		return "", fmt.Errorf("creating comment: %w", err)
	}
	printer.StepDone("Comment created")
	return created.HTMLURL, nil
}

// postTrackerStickyComment implements the sticky comment lifecycle using
// tracker.Client instead of forge.Client. It mirrors the behavior of
// sticky.Post: find an existing comment bearing the marker, collapse
// old content into history, and create or update in-place.
//
// Unlike sticky.Post, this function does not perform bot-user
// verification for marker spoofing protection (tracker.Client has no
// GetAuthenticatedUser method). This is acceptable because the new
// command is used by agents in trusted CI environments, not by
// untrusted external callers.
func postTrackerStickyComment(ctx context.Context, tc tracker.Client, project string, number int, body string, cfg sticky.Config, printer *ui.Printer) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("comment body is empty")
	}
	if strings.TrimSpace(cfg.Marker) == "" {
		return "", fmt.Errorf("marker is empty")
	}

	comments, err := tc.ListComments(ctx, project, number)
	if err != nil {
		return "", fmt.Errorf("listing comments: %w", err)
	}

	existing := findMarkedTrackerComment(comments, cfg.Marker)
	// Same reason as sticky.Post: the body is agent output, and an agent
	// can be induced to write fullsend marker syntax into it. Defanged
	// before this comment's own marker is prepended.
	markedBody := cfg.Marker + "\n" + statuscomment.NeutralizeMarkers(body)

	if existing != nil {
		printer.StepStart("Found existing comment, updating in-place")

		newBody := sticky.BuildUpdatedBody(string(existing.Body), markedBody, cfg)

		if cfg.DryRun {
			printer.StepInfo("Dry run — would update comment " + existing.ID)
			printer.StepInfo(fmt.Sprintf("Body length: %d", len(newBody)))
			return "", nil
		}

		if err := tc.UpdateComment(ctx, project, number, existing.ID, tracker.Body(newBody)); err != nil {
			return "", fmt.Errorf("updating comment: %w", err)
		}
		printer.StepDone("Comment updated")
		return existing.HTMLURL, nil
	}

	printer.StepStart("No existing comment found, creating new one")

	if cfg.DryRun {
		printer.StepInfo("Dry run — would create new comment")
		printer.StepInfo(fmt.Sprintf("Body length: %d", len(markedBody)))
		return "", nil
	}

	created, err := tc.CreateComment(ctx, project, number, tracker.Body(markedBody))
	if err != nil {
		return "", fmt.Errorf("creating comment: %w", err)
	}
	printer.StepDone("Comment created")
	return created.HTMLURL, nil
}

// resolveTracker returns trackerFlag if it is non-empty (the --tracker
// flag was set explicitly). Otherwise it looks for a default tracker
// in the config.yaml under fullsendDir (the "tracker:" field), per
// fullsend-ai/fullsend#5991: --tracker is required unless a default is
// supplied via config. testConfigReader, when non-nil, is used instead
// of loading config from disk (for tests).
func resolveTracker(trackerFlag, fullsendDir string, testConfigReader config.PerRepoConfigReader) (string, error) {
	if trackerFlag != "" {
		return validateTrackerName(trackerFlag)
	}

	prc := testConfigReader
	if prc == nil && fullsendDir != "" {
		reader, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
		if err != nil {
			return "", fmt.Errorf("loading config for default --tracker: %w", err)
		}
		prc, _ = reader.(config.PerRepoConfigReader)
	}

	if prc != nil {
		if t := prc.ConfigTracker(); t != "" {
			return validateTrackerName(t)
		}
	}

	return "", fmt.Errorf("--tracker is required (no default tracker configured; set \"tracker: github|gitlab|jira\" in config.yaml and pass --fullsend-dir, or pass --tracker explicitly)")
}

// validateTrackerName normalizes a tracker name to lowercase and
// validates it against the known set. This catches case-mismatched
// inputs (e.g. "--tracker JIRA") that would otherwise skip tracker-
// specific protections gated on exact string comparisons and then
// fail later in newTrackerClient with a confusing error.
func validateTrackerName(name string) (string, error) {
	normalized := strings.ToLower(name)
	if !slices.Contains(knownTrackers, normalized) {
		return "", fmt.Errorf("unsupported --tracker value %q: must be one of %s", name, strings.Join(knownTrackers, ", "))
	}
	return normalized, nil
}

// findMarkedTrackerComment returns the first tracker comment whose body
// contains the given marker string, or nil if none is found. This is
// the tracker.Comment equivalent of sticky.FindMarkedComment.
func findMarkedTrackerComment(comments []tracker.Comment, marker string) *tracker.Comment {
	for i := range comments {
		if strings.Contains(string(comments[i].Body), marker) {
			return &comments[i]
		}
	}
	return nil
}
