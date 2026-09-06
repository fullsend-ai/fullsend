package cli

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// steerCommentPrefix is the slash command the dispatch Route job routes on.
const steerCommentPrefix = "/fs-steer"

// workItemRef identifies an issue or pull request on a forge.
type workItemRef struct {
	Forge  string
	Owner  string
	Repo   string
	Number int
}

// parseWorkItemURL extracts the work item from a forge issue or PR URL.
//
// GitHub: https://github.com/{owner}/{repo}/{issues|pull}/{n}
// GitLab: https://gitlab.com/{group}[/sub...]/{repo}/-/{issues|merge_requests}/{n}
//
// The number is read from the segment after issues/pull, not from the end of
// the path, so a URL copied off a PR's Files tab or carrying a comment
// anchor resolves to the same item.
func parseWorkItemURL(raw string) (workItemRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return workItemRef{}, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return workItemRef{}, fmt.Errorf("unsupported scheme %q: pass the work item's https URL", u.Scheme)
	}

	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}

	host := strings.ToLower(u.Hostname())
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		owner, repo, number, err := workItemFromSegments(segs, map[string]bool{"issues": true, "pull": true})
		if err != nil {
			return workItemRef{}, fmt.Errorf("%w: %s", err, raw)
		}
		return workItemRef{Forge: "github", Owner: owner, Repo: repo, Number: number}, nil

	case host == "gitlab.com" || strings.Contains(host, "gitlab"):
		// Recognized only so the error can name the real gap rather than
		// reporting an unknown host.
		return workItemRef{Forge: "gitlab"}, nil

	default:
		return workItemRef{}, fmt.Errorf("unsupported forge host %q", host)
	}
}

// workItemFromSegments finds the {owner}/{repo}/{kind}/{n} shape in a URL
// path, where kind is one of kinds. Anything after the number (a Files tab,
// a review sub-path) is ignored.
func workItemFromSegments(segs []string, kinds map[string]bool) (string, string, int, error) {
	notFound := fmt.Errorf("not a GitHub issue or pull request URL")
	for i, seg := range segs {
		if !kinds[seg] || i < 2 || i+1 >= len(segs) {
			continue
		}
		n, err := strconv.Atoi(segs[i+1])
		if err != nil || n <= 0 {
			return "", "", 0, fmt.Errorf("the segment after issues/pull is not a valid item number")
		}
		return segs[i-2], segs[i-1], n, nil
	}
	return "", "", 0, notFound
}

// buildSteerComment renders the comment body the Route job routes on. The
// text is passed through verbatim: authorization happens in the route job
// and the runner sanitizes the delta it builds, so the CLI neither trusts
// nor needs to clean this.
func buildSteerComment(stage, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("the steer text is empty; say what the agent should do differently")
	}
	switch stage {
	case "":
		return steerCommentPrefix + " " + text, nil
	case "review", "fix", "triage":
		return steerCommentPrefix + " " + stage + ": " + text, nil
	default:
		return "", fmt.Errorf("--stage must be review, fix or triage, got %q", stage)
	}
}

// newSteerForgeClient is the CLI's normal forge composition path, held in a
// variable so tests can substitute a fake client.
var newSteerForgeClient = func() (forge.Client, error) {
	return newForgeClient(repos.ForgeGitHub, "", "")
}

func newSteerCmd() *cobra.Command {
	var stage string

	cmd := &cobra.Command{
		Use:   "steer <work-item-url> <text>",
		Short: "Send an update to the fullsend agent already running on a work item",
		Long: `Posts a "/fs-steer" comment on an issue or pull request.

The comment fires the repository's fullsend shim like any other event. Its
route job authorizes you the same way it authorizes /fs-review or /fs-fix,
and selects a stage; the agent run already in flight on that work item then
absorbs the comment instead of being cancelled and restarted (ADR 0101).

The CLI proves nothing: authentication is by the forge (the comment is
posted as you), authorization by the route job, and provenance by the runner.

By default a pull request steers the review stage and an issue steers triage.
Use --stage to pick explicitly; --stage fix needs write permission, the
others need triage.

If no run is in flight, the comment simply dispatches a normal run.

Authentication uses GH_TOKEN, then GITHUB_TOKEN, then 'gh auth token'.`,
		Example: `  fullsend steer https://github.com/org/repo/pull/123 "head moved; re-check the migration"
  fullsend steer --stage fix https://github.com/org/repo/pull/123 "rebase onto main first"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)

			item, err := parseWorkItemURL(args[0])
			if err != nil {
				return err
			}
			if item.Forge != "github" {
				return fmt.Errorf("steering is not supported on %s yet; "+
					"post a %s comment on the merge request by hand", item.Forge, steerCommentPrefix)
			}

			body, err := buildSteerComment(stage, args[1])
			if err != nil {
				return err
			}

			client, err := newSteerForgeClient()
			if err != nil {
				return err
			}

			printer.Header("Steer")
			printer.KeyValue("Work item", fmt.Sprintf("%s/%s#%d", item.Owner, item.Repo, item.Number))

			comment, err := client.CreateIssueComment(cmd.Context(), item.Owner, item.Repo, item.Number, body)
			if err != nil {
				return fmt.Errorf("posting the steer comment: %w", err)
			}
			if comment != nil && comment.HTMLURL != "" {
				printer.StepDone("Posted " + comment.HTMLURL)
			} else {
				printer.StepDone("Posted the steer comment")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&stage, "stage", "",
		"stage to steer: review, fix or triage (default: review for a PR, triage for an issue)")
	return cmd
}
