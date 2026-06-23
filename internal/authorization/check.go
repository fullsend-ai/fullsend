package authorization

import (
	"context"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Phase identifies when an authorization check runs in the agent pipeline.
type Phase string

const (
	PhasePreRun  Phase = "pre-run"
	PhaseMint    Phase = "mint"
	PhasePrePush Phase = "pre-push"
)

// Status is the outcome of an authorization check.
type Status string

const (
	StatusOK               Status = "ok"
	StatusBlocked          Status = "blocked"
	StatusUnauthorizedPush Status = "unauthorized_push"
	StatusStale            Status = "stale"
)

// Target identifies the issue or PR under authorization.
type Target struct {
	Owner  string
	Repo   string
	Number int
}

// Options configures authorization evaluation.
type Options struct {
	ChangedFiles       []string
	TriggerCommentID   int
	InvalidateOnStale  bool
	EnsureNeededOnPush bool
}

// Result holds the outcome of Evaluate.
type Result struct {
	Status     Status
	Elevations []string
	Gate       Gate
}

// Evaluate runs the authorization gate for the given phase.
func Evaluate(ctx context.Context, client forge.Client, gate Gate, target Target, phase Phase, opts Options) (Result, error) {
	result := Result{Gate: gate, Status: StatusOK}

	issue, err := client.GetIssue(ctx, target.Owner, target.Repo, target.Number)
	if err != nil {
		return result, err
	}

	hasNeeded := hasLabel(issue.Labels, gate.NeededLabel)
	hasAllowed := hasLabel(issue.Labels, gate.AllowedLabel)

	switch phase {
	case PhasePreRun:
		if hasNeeded && !hasAllowed {
			result.Status = StatusBlocked
			return result, nil
		}
		if hasAllowed {
			stale, err := checkStaleIfAllowed(ctx, client, gate, target, opts)
			if err != nil {
				return result, err
			}
			if stale {
				result.Status = StatusStale
			}
		}

	case PhaseMint:
		if hasAllowed {
			stale, err := checkStaleIfAllowed(ctx, client, gate, target, opts)
			if err != nil {
				return result, err
			}
			if stale {
				result.Status = StatusStale
				return result, nil
			}
			result.Elevations = []string{gate.Name}
			return result, nil
		}
		if hasNeeded && !hasAllowed {
			result.Status = StatusBlocked
			return result, nil
		}

	case PhasePrePush:
		touchesWorkflow := MatchesAnyInList(opts.ChangedFiles, gate.FilePatterns)
		if !touchesWorkflow {
			return result, nil
		}
		if !hasAllowed {
			result.Status = StatusUnauthorizedPush
			return result, nil
		}
		stale, err := checkStaleIfAllowed(ctx, client, gate, target, opts)
		if err != nil {
			return result, err
		}
		if stale {
			result.Status = StatusStale
		}

	default:
		return result, nil
	}

	return result, nil
}

func checkStaleIfAllowed(ctx context.Context, client forge.Client, gate Gate, target Target, opts Options) (bool, error) {
	allowedAt, err := client.GetLabelAppliedAt(ctx, target.Owner, target.Repo, target.Number, gate.AllowedLabel)
	if err != nil {
		if forge.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if allowedAt.IsZero() {
		return false, nil
	}
	return CheckStale(ctx, client, target.Owner, target.Repo, target.Number, gate, allowedAt, opts.TriggerCommentID)
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

// MatchesAnyInList reports whether any path in files matches patterns.
func MatchesAnyInList(files, patterns []string) bool {
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if MatchesAny(f, patterns) {
			return true
		}
	}
	return false
}

// ApplyMutations updates labels when a check fails and apply is enabled.
func ApplyMutations(ctx context.Context, client forge.Client, gate Gate, target Target, status Status) error {
	switch status {
	case StatusStale:
		if err := client.RemoveIssueLabel(ctx, target.Owner, target.Repo, target.Number, gate.AllowedLabel); err != nil && !forge.IsNotFound(err) {
			return err
		}
		return client.AddIssueLabels(ctx, target.Owner, target.Repo, target.Number, gate.NeededLabel)
	case StatusUnauthorizedPush:
		return client.AddIssueLabels(ctx, target.Owner, target.Repo, target.Number, gate.NeededLabel)
	default:
		return nil
	}
}

// ParseChangedFiles splits a newline-separated file list.
func ParseChangedFiles(input string) []string {
	var files []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// AllowedAtZero is the zero time sentinel used when a label was never applied.
var AllowedAtZero time.Time
