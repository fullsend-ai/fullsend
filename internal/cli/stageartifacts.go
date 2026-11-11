package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/artifacts"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newStageArtifactsCmd() *cobra.Command {
	var (
		outputDir   string
		stagedDir   string
		fullsendDir string
		eventFile   string
	)
	cmd := &cobra.Command{
		Use:   "stage-artifacts",
		Short: "Build the staged copy of a run's output that CI uploads",
		Long: `Resolve the run artifact level from config.yaml (artifacts.content and
artifacts.by_source, ADR 0161) for the run's source, then copy what that
level allows from --output-dir into --artifact-dir. CI uploads only the
staged directory; --output-dir is never modified.

  all       everything in --output-dir (the default)
  metadata  only metrics.json, eval-measurements.jsonl and
            run-telemetry.jsonl (with content capture stripped) from
            each run directory
  none      nothing; CI skips the upload

The run's source is the normalized event's source.system (trimmed and
lowercased), read from --event-file or, without it, from the dispatch
payload fullsend run reads. Without any normalized event, a native GitHub
issue, pull request, comment or review event on GitHub Actions is the
source github. Otherwise, or when a dispatch payload names a source it
cannot back (its event is invalid or names a different source), the source
is unknown and the level is the strictest of artifacts.content and every
artifacts.by_source entry, with a warning when that is stricter than
artifacts.content. A known source with no by_source entry gets
artifacts.content. The rule applied is printed on stderr.

Prints the resolved level on stdout. Fails closed: when the level cannot
be resolved or the copy fails, --artifact-dir is left empty and the command
exits non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level, err := runStageArtifacts(outputDir, stagedDir, fullsendDir, eventFile, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), level)
			return err
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "run output directory written by fullsend run (required)")
	cmd.Flags().StringVar(&stagedDir, "artifact-dir", "", "directory to build the staged copy in; emptied first (required)")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".", "fullsend config directory holding config.yaml")
	cmd.Flags().StringVar(&eventFile, "event-file", "", "normalized event JSON passed to fullsend run, if any")
	_ = cmd.MarkFlagRequired("output-dir")
	_ = cmd.MarkFlagRequired("artifact-dir")
	return cmd
}

// runStageArtifacts empties stagedDir, resolves the artifact level and
// stages the copy. Any failure after the directory is emptied leaves it
// empty, so the raw output is never uploaded in its place.
func runStageArtifacts(outputDir, stagedDir, fullsendDir, eventFile string, stderr io.Writer) (string, error) {
	if err := artifacts.PrepareStagedDir(outputDir, stagedDir); err != nil {
		return "", err
	}
	printer := ui.New(stderr)
	res, err := resolveArtifactLevel(fullsendDir, eventFile)
	if err != nil {
		fmt.Fprintf(stderr, "fullsend: artifact level could not be resolved; nothing is staged: %v\n", err)
		return "", fmt.Errorf("resolving artifact level: %w", err)
	}
	ruleLine := "Run artifact level " + res.level + ": " + res.rule
	if res.strictened {
		// An operator should see why a manual or scheduled run published
		// less than artifacts.content allows.
		printer.StepWarn(ruleLine)
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			fmt.Fprintf(stderr, "::warning::%s\n", ruleLine)
		}
	} else {
		printer.StepInfo(ruleLine)
	}
	if msg := publicTranscriptWarning(res); msg != "" {
		printer.StepWarn(msg)
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			fmt.Fprintf(stderr, "::warning::%s\n", msg)
		}
	}
	if err := artifacts.Stage(outputDir, stagedDir, res.level); err != nil {
		fmt.Fprintf(stderr, "fullsend: staging run artifact failed; nothing is staged: %v\n", err)
		return "", fmt.Errorf("staging artifacts: %w", err)
	}
	return res.level, nil
}

// artifactResolution is the resolved level plus what it was resolved from.
type artifactResolution struct {
	level string
	// source is the run source used for by_source; "" when unknown.
	source string
	// rule says, for the log, how the source was found.
	rule string
	// cfg is the layered config; nil when there is none.
	cfg config.PerRepoConfigReader
	// strictened reports that the source is unknown and that made the
	// level stricter than artifacts.content alone.
	strictened bool
}

// unknownSourceRule is the stderr rule text when the run's source is
// unknown.
const unknownSourceRule = "using the strictest of artifacts.content and every artifacts.by_source entry"

// resolveArtifactLevel loads config.yaml the way fullsend run does for
// runtime selection and resolves the level for the run's source. The
// source comes from a valid normalized event or a native GitHub event
// (artifactRunSource); when it cannot be determined, the level is the strictest of content and every by_source
// entry, so a missing event never lowers a source's level.
func resolveArtifactLevel(fullsendDir, eventFile string) (artifactResolution, error) {
	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return artifactResolution{}, err
	}
	source, how, err := artifactRunSource(absFullsendDir, eventFile)
	if err != nil {
		return artifactResolution{}, err
	}
	rc, err := loadRunConfig(filepath.Join(absFullsendDir, config.OverlayConfigFile))
	if err != nil {
		return artifactResolution{}, err
	}
	res := artifactResolution{cfg: rc.perRepo, source: source}
	if source != "" {
		res.rule = fmt.Sprintf("run source %q %s", agentruntime.SanitizeOutput(source), how)
	} else {
		res.rule = "run source unknown (" + how + "); " + unknownSourceRule
	}
	res.level, err = config.ResolveArtifactContent(rc.perRepo, source)
	if err != nil {
		return artifactResolution{}, err
	}
	if source == "" && rc.perRepo != nil {
		content := config.StricterArtifactContent(rc.perRepo.ConfigArtifactsContent(), "")
		res.strictened = res.level != content
	}
	return res, nil
}

// normalizeSource trims and lowercases a source name, matching the
// lowercase artifacts.by_source keys.
func normalizeSource(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// artifactRunSource returns the run's normalized event.source.system
// (trimmed, lowercase), from the same inputs fullsend run reads:
// --event-file when given, else the dispatch payload. An --event-file that
// cannot be read or parsed is an error (nothing is staged).
//
// Without any normalized event, a native GitHub event gives the source
// github (see nativeGitHubEvent).
//
// The source is "" (unknown) when none of these applies, or when any
// dispatch payload's raw _normalized_event.source.system is non-empty but
// that event fails validation or names a different source: a payload that
// claims a source it cannot back must not let the run fall to a weaker
// level.
//
// how describes, for the log, where a known source came from, or why the
// source is unknown.
func artifactRunSource(absFullsendDir, eventFile string) (source, how string, err error) {
	if eventFile != "" {
		data, err := os.ReadFile(eventFile)
		if err != nil {
			return "", "", fmt.Errorf("reading event file: %w", err)
		}
		ev, err := normevent.ParseJSON(data)
		if err != nil {
			return "", "", fmt.Errorf("parsing event file: %w", err)
		}
		source = normalizeSource(string(ev.Source.System))
	} else if eventMap := extractNormalizedEventFromDispatch(absFullsendDir); eventMap != nil {
		source = normalizeSource(extractMapString(eventMap, "source", "system"))
	}
	for _, payload := range dispatchEventPayloads(absFullsendDir) {
		claim := rawDispatchSourceClaim(payload)
		if claim == "" {
			continue
		}
		valid := extractNormalizedEventFromPayload(payload)
		if valid == nil {
			return "", fmt.Sprintf("a dispatch payload names source %q but its normalized event is invalid",
				agentruntime.SanitizeOutput(claim)), nil
		}
		if got := normalizeSource(extractMapString(valid, "source", "system")); got != source {
			if source == "" {
				return "", fmt.Sprintf("a dispatch payload names source %q but no source was resolved",
					agentruntime.SanitizeOutput(got)), nil
			}
			return "", fmt.Sprintf("a dispatch payload names source %q, not the run's source %q",
				agentruntime.SanitizeOutput(got), agentruntime.SanitizeOutput(source)), nil
		}
	}
	if source != "" {
		return source, "from the normalized event", nil
	}
	if eventFile == "" {
		if name, ok := nativeGitHubEvent(absFullsendDir); ok {
			return string(normevent.SystemGitHub), "(native " + name + " event)", nil
		}
	}
	return "", "no valid normalized event", nil
}

// nativeGitHubEvents are the GitHub Actions event names that only GitHub
// itself starts: the triggers of the shipped fullsend shim (issues,
// issue_comment, pull_request_target, pull_request_review) plus the
// pull_request and pull_request_review_comment events a custom caller may
// use. None of them can carry a run started from a tracker.
//
// In a reusable workflow GITHUB_EVENT_NAME is the caller's event. The Jira
// poller's caller runs on schedule or workflow_dispatch, so the stage jobs
// it dispatches report one of those names, never one listed here.
// workflow_dispatch, repository_dispatch, schedule and workflow_call stay
// unknown.
var nativeGitHubEvents = map[string]bool{
	"issues":                      true,
	"issue_comment":               true,
	"pull_request":                true,
	"pull_request_target":         true,
	"pull_request_review":         true,
	"pull_request_review_comment": true,
}

// nativeGitHubEvent reports whether the run is a stage job of a native
// GitHub event, so its source is github even though the standard stage
// jobs receive no normalized event. It holds only on GitHub Actions, for a
// GITHUB_EVENT_NAME in nativeGitHubEvents, and when no payload — the
// dispatch payload file, inputs.event_payload or client_payload of the
// Actions event, or the Actions event itself — carries a normalized event
// or a source.system claim. Anything unreadable counts as a claim.
func nativeGitHubEvent(absFullsendDir string) (string, bool) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return "", false
	}
	name := os.Getenv("GITHUB_EVENT_NAME")
	if !nativeGitHubEvents[name] {
		return "", false
	}
	for _, payload := range dispatchEventPayloads(absFullsendDir) {
		if payloadClaimsSource(payload) {
			return "", false
		}
	}
	path := os.Getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if payloadClaimsSource(data) {
		return "", false
	}
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return "", false
	}
	if cp, ok := ev["client_payload"]; ok && cp != nil {
		b, err := json.Marshal(cp)
		if err != nil || payloadClaimsSource(b) {
			return "", false
		}
	}
	return name, true
}

// payloadClaimsSource reports whether a payload carries a
// _normalized_event or a top-level source.system, valid or not. A payload
// that is not a JSON object counts as a claim.
func payloadClaimsSource(payload []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil || raw == nil {
		return true
	}
	if _, ok := raw["_normalized_event"]; ok {
		return true
	}
	if src, ok := raw["source"].(map[string]any); ok {
		if _, ok := src["system"]; ok {
			return true
		}
	}
	return false
}

// rawDispatchSourceClaim returns a dispatch payload's unvalidated
// _normalized_event.source.system, trimmed and lowercased; "" when absent.
func rawDispatchSourceClaim(payload []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ""
	}
	norm, ok := raw["_normalized_event"].(map[string]any)
	if !ok {
		return ""
	}
	return normalizeSource(extractMapString(norm, "source", "system"))
}

// sourceForgejo is the Forgejo forge's source name. normevent has no
// constant for it yet; no Forgejo adapter emits events today.
const sourceForgejo = "forgejo"

// isForgeOrInternalSource reports whether a run source is a forge or
// fullsend itself, as opposed to an external tracker such as Jira.
func isForgeOrInternalSource(source string) bool {
	switch normevent.SourceSystem(source) {
	case normevent.SystemGitHub, normevent.SystemGitLab, normevent.SystemManual, normevent.SystemSchedule, sourceForgejo:
		return true
	}
	return false
}

// publicTranscriptWarning returns the warning for a run from an external
// tracker that publishes the full transcript only because the repo made
// no choice: level all, no by_source entry for the source, no explicit
// artifacts.content, and a repo not known to be private. It returns ""
// when no warning applies.
func publicTranscriptWarning(res artifactResolution) string {
	if res.source == "" || isForgeOrInternalSource(res.source) || res.level != config.ArtifactContentAll {
		return ""
	}
	if res.cfg != nil {
		if res.cfg.ConfigArtifactsContentSet() {
			return ""
		}
		if _, ok := res.cfg.ConfigArtifactsBySource()[res.source]; ok {
			return ""
		}
	}
	if detectRepoVisibility() == repoVisibilityPrivate {
		return ""
	}
	src := agentruntime.SanitizeOutput(res.source)
	return fmt.Sprintf("Run started from %s: the run artifact publishes the full transcript (artifacts level \"all\"). "+
		"If %s holds data that is not public, set artifacts.by_source.%s to metadata or none in config.yaml; "+
		"set it to all to silence this warning.", src, src, src)
}

type repoVisibility int

const (
	repoVisibilityUnknown repoVisibility = iota
	repoVisibilityNotPrivate
	repoVisibilityPrivate
)

// detectRepoVisibility reads the repo's visibility from the CI
// environment: repository.visibility in the GitHub Actions event file
// (repository.private when visibility is absent), or CI_PROJECT_VISIBILITY
// on GitLab CI. On both forges only "private" counts as private;
// "internal" is visible to every signed-in user of the instance or
// enterprise.
func detectRepoVisibility() repoVisibility {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		path := os.Getenv("GITHUB_EVENT_PATH")
		if path == "" {
			return repoVisibilityUnknown
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return repoVisibilityUnknown
		}
		var ev struct {
			Repository *struct {
				Visibility string `json:"visibility"`
				Private    *bool  `json:"private"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(data, &ev); err != nil || ev.Repository == nil {
			return repoVisibilityUnknown
		}
		switch ev.Repository.Visibility {
		case "private":
			return repoVisibilityPrivate
		case "public", "internal":
			return repoVisibilityNotPrivate
		}
		if ev.Repository.Private == nil {
			return repoVisibilityUnknown
		}
		if *ev.Repository.Private {
			return repoVisibilityPrivate
		}
		return repoVisibilityNotPrivate
	}
	if os.Getenv("GITLAB_CI") == "true" {
		switch os.Getenv("CI_PROJECT_VISIBILITY") {
		case "private":
			return repoVisibilityPrivate
		case "public", "internal":
			return repoVisibilityNotPrivate
		}
	}
	return repoVisibilityUnknown
}
