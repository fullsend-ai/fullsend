package steps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const logTS = "2026-10-31T12:00:00.1234567Z "

func logLines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func TestPersonaBlocks(t *testing.T) {
	complete := logLines(
		logTS+"Uploading skills",
		logTS+"subagents: correctness → google-vertex/claude-haiku (from subagents.correctness)",
		logTS+"Warning: persona \"bash-only\" skipped: a Bash(...) allowlist ...",
		logTS+"subagents: security → <parent's model> (from parent)\r",
		logTS+"Starting pi",
	)
	assert.Equal(t, []personaBlock{{Names: []string{"correctness", "security"}, Complete: true}}, personaBlocks(complete))

	// No timestamp prefix (GitLab traces) parses the same way.
	plain := logLines("before", "subagents: correctness → x (from y)", "after")
	assert.Equal(t, []personaBlock{{Names: []string{"correctness"}, Complete: true}}, personaBlocks(plain))

	// Cut at the end of the fetched log: nothing after it.
	cutEnd := logLines(logTS+"Uploading skills", logTS+"subagents: correctness → x (from y)")
	assert.Equal(t, []personaBlock{{Names: []string{"correctness"}, Complete: false}}, personaBlocks(cutEnd))
	cutEndNoNewline := logTS + "Uploading skills\n" + logTS + "subagents: corr"
	assert.False(t, personaBlocks(cutEndNoNewline)[0].Complete)

	// Cut by the client's truncation note.
	truncated := logLines(logTS+"Uploading skills", logTS+"subagents: correctness → x", "[log truncated: job 7 exceeds 1048576 bytes]")
	assert.False(t, personaBlocks(truncated)[0].Complete)

	// Nothing before it.
	assert.False(t, personaBlocks(logLines("subagents: correctness → x", "after"))[0].Complete)

	assert.Empty(t, personaBlocks(logLines(logTS+"no personas here", logTS+"Warning: persona x skipped: y")))
}

func TestCheckPersonaRegistration(t *testing.T) {
	ok := logLines(logTS+"Uploading skills", logTS+"subagents: correctness → x", logTS+"subagents: security → y", logTS+"Starting pi") +
		"[log truncated: job 7 exceeds 1048576 bytes]\n"
	require.NoError(t, checkPersonaRegistration(ok, "correctness", "user-experience"), "truncation elsewhere does not matter")

	err := checkPersonaRegistration(logLines("a", "b"), "correctness", "user-experience")
	require.ErrorContains(t, err, "no \"subagents:\" persona registration lines")

	err = checkPersonaRegistration(logLines("a", "subagents: correctness → x"), "correctness", "user-experience")
	require.ErrorContains(t, err, "cut off")

	err = checkPersonaRegistration(logLines("a", "subagents: security → x", "b"), "correctness", "user-experience")
	require.ErrorContains(t, err, `registers security, not "correctness"`)

	err = checkPersonaRegistration(logLines("a", "subagents: correctness → x", "subagents: user-experience → x", "b"), "correctness", "user-experience")
	require.ErrorContains(t, err, `unexpectedly registers "user-experience"`)
}

func TestThenHarnessLogsRegisterPersona(t *testing.T) {
	run := &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"}
	newWorld := func(logs string, logsErr error) *world.World {
		return &world.World{Org: "org", RepoName: "repo", WorkflowRun: run, CI: &fakeDebugCI{logs: logs, logsErr: logsErr}}
	}
	require.NoError(t, thenHarnessLogsRegisterPersona(newWorld(logLines("a", "subagents: correctness → x", "b"), nil), "pi", "correctness", "user-experience"))

	err := thenHarnessLogsRegisterPersona(newWorld(logLines("a"), nil), "pi", "correctness", "user-experience")
	require.ErrorContains(t, err, `harness "pi" workflow logs (run 9)`)

	err = thenHarnessLogsRegisterPersona(newWorld("", fmt.Errorf("HTTP 502")), "pi", "correctness", "user-experience")
	require.ErrorContains(t, err, "HTTP 502")

	err = thenHarnessLogsRegisterPersona(&world.World{Org: "org", RepoName: "repo", CI: &fakeDebugCI{}}, "pi", "x", "y")
	require.ErrorContains(t, err, "no workflow trigger time recorded")
}
