package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// assertJQDeniedPattern matches the jq builtins that reach past the input
// document: the environment, other inputs, module loading, and the ones that
// print to stderr. The op's error carries jq's stderr into
// behaviour-results.json, so a filter must not be able to put a sandbox
// variable there.
var assertJQDeniedPattern = regexp.MustCompile(`\$ENV|\$__loc__|\b(env|input|inputs|input_filename|debug|stderr|halt|halt_error|import|include|modulemeta|get_search_list|get_prog_origin|get_jq_origin)\b`)

// executeAssertJQ runs the assert_jq op: `path, filter` evaluates the jq
// filter against the JSON file at path (relative to the sandbox workspace)
// with `jq -e`, so the op succeeds only when the filter's last output is
// neither false nor null. Unlike assert_json, which only takes a dotted
// field path, the filter may select array elements and compare fields, for
// example `any(.personas[]; .path == "sub-agents/security.md")`. Everything
// after the first comma is the filter.
func executeAssertJQ(rt DummyRuntime, sandboxName, args string) error {
	parts := strings.SplitN(args, ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("assert_jq args must be path, jq_filter")
	}
	path := strings.TrimSpace(parts[0])
	filter := strings.TrimSpace(parts[1])
	if path == "" || filter == "" {
		return fmt.Errorf("assert_jq args must be path, jq_filter")
	}
	if strings.ContainsAny(filter, "\x00\n\r") {
		return fmt.Errorf("assert_jq filter must be a single line")
	}
	if m := assertJQDeniedPattern.FindString(filter); m != "" {
		return fmt.Errorf("assert_jq filter may not use %q", m)
	}
	remotePath, err := resolveSandboxPath(sandbox.SandboxWorkspace, path)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("jq -e %s %s >/dev/null", shellQuote(filter), shellQuote(remotePath))
	_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("assert_jq exec: %w", err)
	}
	if exitCode != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = fmt.Sprintf("filter result was false or null (jq exit %d)", exitCode)
		}
		return fmt.Errorf("assert_jq %s at %s: %s", filter, path, detail)
	}
	return nil
}
