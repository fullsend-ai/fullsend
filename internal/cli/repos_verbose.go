package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/spf13/cobra"
)

// reposVerboseFlag is the persistent flag on the repos command group that
// shows the underlying API error text and logs each GitLab API request.
const reposVerboseFlag = "verbose"

// reposVerboseContext applies the repos --verbose flag. When it is set, API
// failures include the server's scrubbed error text instead of withholding
// it, and every GitLab API request is logged to stderr as method, path and
// status. secrets lists credential values the command was given; together
// with GITLAB_TOKEN they are scrubbed from verbose error text, followed by
// the shared pattern redactor, which also masks GitHub token shapes. The
// returned function restores the default behavior and must be called when
// the command finishes.
func reposVerboseContext(cmd *cobra.Command, secrets ...string) (context.Context, func()) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if !reposVerbose(cmd) {
		return ctx, func() {}
	}
	known := append([]string{os.Getenv("GITLAB_TOKEN")}, secrets...)
	restore := repos.EnableVerboseAPIErrors(newVerboseScrubber(known))
	return gl.WithRequestLogger(ctx, newRequestLogWriter(cmd.ErrOrStderr())), restore
}

// reposVerbose reports whether the repos group's persistent --verbose flag
// was set; cobra merges it into the subcommand's flag set when parsing.
func reposVerbose(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool(reposVerboseFlag)
	return v
}

// newVerboseScrubber returns a scrubber that masks each known credential
// value, in literal, query-escaped and path-escaped form, and then every credential shape the shared SecretRedactor knows.
func newVerboseScrubber(known []string) func(string) string {
	var values []string
	for _, v := range known {
		if strings.TrimSpace(v) != "" {
			values = append(values, v, url.QueryEscape(v), url.PathEscape(v))
		}
	}
	// Longest first so an escaped form is never partly consumed by a
	// shorter form of the same value.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	redactor := security.NewSecretRedactor()
	return func(text string) string {
		for _, v := range values {
			text = strings.ReplaceAll(text, v, "[REDACTED]")
		}
		if res := redactor.Scan(text); res.Sanitized != "" {
			text = res.Sanitized
		}
		return text
	}
}

// newRequestLogWriter returns a GitLab request logger that writes one line
// per request attempt to w. Writes are serialized because repos commands
// run per-repository work concurrently.
func newRequestLogWriter(w io.Writer) gl.RequestLogger {
	var mu sync.Mutex
	return func(method, path string, status int) {
		result := "no response"
		if status != 0 {
			result = fmt.Sprint(status)
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "gitlab api: %s %s -> %s\n", method, path, result)
	}
}

// roleTokenValues returns the credential values of --gitlab-role-token
// entries (role=token) so verbose output can scrub them.
func roleTokenValues(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, v, ok := strings.Cut(e, "="); ok {
			out = append(out, v)
		}
	}
	return out
}
