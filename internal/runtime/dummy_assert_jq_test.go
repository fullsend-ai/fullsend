package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteBehaviourOp_AssertJQ(t *testing.T) {
	t.Parallel()

	const filter = `any(.personas[]; .path == "sub-agents/security.md" and .origin == "override")`

	var gotCmd string
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		gotCmd = cmd
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_jq",
		Args: ".fullsend/personas.json, " + filter,
	})
	require.NoError(t, err)
	assert.Equal(t, "jq -e "+shellQuote(filter)+" "+shellQuote("/sandbox/workspace/.fullsend/personas.json")+" >/dev/null", gotCmd)
}

func TestExecuteBehaviourOp_AssertJQFilterWithCommas(t *testing.T) {
	t.Parallel()

	var gotCmd string
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		gotCmd = cmd
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_jq",
		Args: "out.json, [.a, .b] | length == 2",
	})
	require.NoError(t, err)
	assert.Contains(t, gotCmd, shellQuote("[.a, .b] | length == 2"))
}

func TestExecuteBehaviourOp_AssertJQFalse(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(string, string, time.Duration) (string, string, int, error) {
		return "", "", 1, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_jq",
		Args: "out.json, .ok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "false or null")
}

func TestExecuteBehaviourOp_AssertJQStderr(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(string, string, time.Duration) (string, string, int, error) {
		return "", "jq: error: syntax error\n", 3, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_jq",
		Args: "out.json, .ok ==",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "syntax error")
}

func TestExecuteBehaviourOp_AssertJQRejects(t *testing.T) {
	t.Parallel()

	never := DummyRuntime{ExecFn: func(string, string, time.Duration) (string, string, int, error) {
		t.Error("exec must not run for a rejected op")
		return "", "", 0, nil
	}}
	for name, tc := range map[string]struct{ args, want string }{
		"no comma":     {"out.json", "path, jq_filter"},
		"empty filter": {"out.json, ", "path, jq_filter"},
		"empty path":   {" , .ok", "path, jq_filter"},
		"newline":      {"out.json, .a\n.b", "single line"},
		"env":          {"out.json, env.GH_TOKEN", `"env"`},
		"ENV":          {"out.json, $ENV.GH_TOKEN", `"$ENV"`},
		"input":        {"out.json, input_filename", `"input_filename"`},
		"stderr":       {"out.json, . | stderr", `"stderr"`},
		"halt_error":   {"out.json, .x | halt_error", `"halt_error"`},
		"escape":       {"../../etc/passwd, .", "escapes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := executeBehaviourOp(never, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_jq", Args: tc.args})
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.want), "error %q should contain %q", err, tc.want)
		})
	}
}

func TestAssertJQDeniedPatternAllowsFieldNames(t *testing.T) {
	t.Parallel()

	// Field names that merely contain a denied word are not builtins.
	for _, f := range []string{`.environment`, `.inputs_count`, `.personas[].debugger`, `.envelope.env_name`} {
		assert.Empty(t, assertJQDeniedPattern.FindString(f), f)
	}
}
