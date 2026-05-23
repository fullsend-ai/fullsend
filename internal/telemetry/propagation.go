package telemetry

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var reTraceparent = regexp.MustCompile(
	`^00-[a-f0-9]{32}-[a-f0-9]{16}-[a-f0-9]{2}$`,
)

// Traceparent formats a W3C traceparent header from the span context.
// Returns empty string if the span context is invalid.
func Traceparent(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	flags := "00"
	if sc.IsSampled() {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s",
		sc.TraceID().String(),
		sc.SpanID().String(),
		flags,
	)
}

// TraceparentEnvVar returns the TRACEPARENT=... string suitable for injection
// into a subprocess environment. Returns empty string if the context carries
// no valid span.
func TraceparentEnvVar(ctx context.Context) string {
	tp := Traceparent(ctx)
	if tp == "" {
		return ""
	}
	return "TRACEPARENT=" + tp
}

// IsValidTraceparent checks whether a string is a well-formed W3C traceparent.
func IsValidTraceparent(tp string) bool {
	return reTraceparent.MatchString(tp)
}

// ContextFromTraceparent extracts trace context from a TRACEPARENT env var
// or explicit value and returns a context with the remote span context set.
// Falls back to checking the TRACEPARENT environment variable if value is empty.
func ContextFromTraceparent(ctx context.Context, value string) context.Context {
	if value == "" {
		value = os.Getenv("TRACEPARENT")
	}
	if value == "" || !IsValidTraceparent(value) {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": value}
	prop := propagation.TraceContext{}
	return prop.Extract(ctx, carrier)
}

// WorkItemID constructs a canonical work-item identifier from a repo and
// issue/PR number. This is the framework-level convention for cross-run
// correlation: every trace carries this as a span attribute so consumers
// can query for all traces related to a work item at read time.
//
// Format: "owner/repo#123"
func WorkItemID(repo string, number int) string {
	if repo == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("%s#%d", repo, number)
}

// WorkItemIDFromEnv attempts to construct a work_item_id from standard
// environment variables set by fullsend dispatch workflows.
func WorkItemIDFromEnv() string {
	repo := os.Getenv("FULLSEND_SOURCE_REPO")
	if repo == "" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}

	// Try issue number first, then PR number. Validate that the value
	// looks numeric to avoid producing IDs like "owner/repo#not-a-number".
	for _, key := range []string{
		"FULLSEND_ISSUE_NUMBER",
		"GITHUB_ISSUE_NUMBER",
		"FULLSEND_PR_NUMBER",
	} {
		if num := os.Getenv(key); num != "" && isNumeric(num) {
			if repo != "" {
				return repo + "#" + num
			}
		}
	}

	// Fall back to URL-based extraction.
	for _, key := range []string{"GITHUB_ISSUE_URL", "GITHUB_PR_URL", "ORIGINATING_URL"} {
		if u := os.Getenv(key); u != "" {
			if wid := workItemFromURL(u); wid != "" {
				return wid
			}
		}
	}
	return ""
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// workItemFromURL extracts "owner/repo#N" from a GitHub issue or PR URL.
func workItemFromURL(u string) string {
	// Expected: https://github.com/owner/repo/issues/123
	//       or: https://github.com/owner/repo/pull/123
	parts := strings.Split(strings.TrimRight(u, "/"), "/")
	if len(parts) < 5 {
		return ""
	}
	n := len(parts)
	number := parts[n-1]
	kind := parts[n-2] // "issues" or "pull"
	if kind != "issues" && kind != "pull" {
		return ""
	}
	if !isNumeric(number) {
		return ""
	}
	repo := parts[n-4] + "/" + parts[n-3]
	return repo + "#" + number
}
