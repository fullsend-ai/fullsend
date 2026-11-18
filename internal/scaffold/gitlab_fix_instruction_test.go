package scaffold

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitlabMultilineInstruction is the instruction a human typed after /fs-fix
// in the multiline fixtures; HUMAN_INSTRUCTION must equal it exactly.
const gitlabMultilineInstruction = "only address the nil check\n\n- keep the public API\n- add a regression test"

// runAgentJobSection returns the run-agent-job.sh text from the line that
// starts with start through the end of the first following line that
// equals end.
func runAgentJobSection(t *testing.T, script, start, end string) string {
	t.Helper()
	i := strings.Index(script, "\n"+start)
	require.NotEqual(t, -1, i, "section start %q not found", start)
	rest := script[i+1:]
	j := strings.Index(rest, "\n"+end+"\n")
	require.NotEqual(t, -1, j, "section end %q not found after %q", end, start)
	return rest[:j+1+len(end)+1]
}

// runCommentContextSnippet runs the comment-body helpers and the fix-stage
// HUMAN_INSTRUCTION and retro RETRO_COMMENT blocks of run-agent-job.sh, as
// shipped, for a decoded event payload, and returns the values they export.
func runCommentContextSnippet(t *testing.T, stage, isBot string, payload map[string]any) (humanInstruction, retroComment, output string) {
	t.Helper()
	content, err := GitLabPerRepoFile(gitlabRunAgentJobScriptPath)
	require.NoError(t, err)
	script := string(content)

	snippet := strings.Join([]string{
		runAgentJobSection(t, script, "fullsend_payload_comment_body() {", "}"),
		runAgentJobSection(t, script, "fullsend_fix_instruction() {", "}"),
		runAgentJobSection(t, script, `if [ "${STAGE}" = "retro" ] && [ -n "${EVENT_PAYLOAD_B64:-}" ]; then`, "fi"),
		`if [ "${STAGE}" = "fix" ]; then`,
		runAgentJobSection(t, script, `  HUMAN_INSTRUCTION="none"`, "  export HUMAN_INSTRUCTION"),
		"fi",
		`printf '%s' "${HUMAN_INSTRUCTION-UNSET}" > "${OUT_DIR}/human"`,
		`printf '%s' "${RETRO_COMMENT-UNSET}" > "${OUT_DIR}/retro"`,
	}, "\n")

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	outDir := t.TempDir()
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+snippet)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"STAGE=" + stage,
		"_IS_BOT_TRIGGER=" + isBot,
		"EVENT_PAYLOAD_B64=" + base64.StdEncoding.EncodeToString(raw),
		"OUT_DIR=" + outDir,
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "snippet output: %s", out)
	human, err := os.ReadFile(filepath.Join(outDir, "human"))
	require.NoError(t, err)
	retro, err := os.ReadFile(filepath.Join(outDir, "retro"))
	require.NoError(t, err)
	return string(human), string(retro), string(out)
}

// canonicalCommentPayload builds a GitLab dispatch payload whose raw comment
// body sits at the canonical normalized path.
func canonicalCommentPayload(eventType, body string) map[string]any {
	return map[string]any{
		"type": eventType,
		"iid":  3,
		"_normalized_event": map[string]any{
			"transition": map[string]any{
				"kind":    "comment_added",
				"comment": map[string]any{"body": body},
			},
		},
	}
}

func TestRunAgentJobScript_FixHumanInstructionFromCanonicalCommentBody(t *testing.T) {
	multilineCommand := "/fs-fix " + gitlabMultilineInstruction

	tests := []struct {
		name    string
		isBot   string
		payload map[string]any
		want    string
	}{
		{
			name:    "scheduled poller payload",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", multilineCommand),
			want:    gitlabMultilineInstruction,
		},
		{
			name:  "webhook payload with raw webhook note alongside",
			isBot: "false",
			payload: func() map[string]any {
				p := canonicalCommentPayload("mr_note", multilineCommand)
				p["object_attributes"] = map[string]any{"note": "/fs-fix raw webhook text must not be used"}
				return p
			}(),
			want: gitlabMultilineInstruction,
		},
		{
			name:  "component review redispatch with full normalized event",
			isBot: "false",
			payload: map[string]any{
				"comment": map[string]any{"body": "/fs-fix top-level comment must not be used"},
				"_normalized_event": map[string]any{
					"repo":   "group/project",
					"entity": map[string]any{"kind": "change_proposal", "id": 3, "url": "https://gitlab.example.com/group/project/-/merge_requests/3"},
					"transition": map[string]any{
						"kind": "comment_added",
						"comment": map[string]any{
							"body":        multilineCommand,
							"command":     "/fs-fix",
							"instruction": gitlabMultilineInstruction,
						},
					},
					"source": map[string]any{"system": "gitlab", "raw_type": "merge_request"},
				},
			},
			want: gitlabMultilineInstruction,
		},
		{
			name:    "leading whitespace and multiline after the token",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", "  /fs-fix\n"+gitlabMultilineInstruction+"\n"),
			want:    gitlabMultilineInstruction,
		},
		{
			name:    "bare /fs-fix",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", "/fs-fix"),
			want:    "none",
		},
		{
			name:  "legacy note_body only",
			isBot: "false",
			payload: map[string]any{
				"type":      "mr_note",
				"iid":       3,
				"note_body": multilineCommand,
			},
			want: "none",
		},
		{
			name:  "standalone normalized instruction without raw body",
			isBot: "false",
			payload: map[string]any{
				"type":        "mr_note",
				"instruction": "top-level instruction must not be used",
				"_normalized_event": map[string]any{
					"transition": map[string]any{
						"kind":    "comment_added",
						"comment": map[string]any{"command": "/fs-fix", "instruction": "unpaired instruction must not be used"},
					},
				},
			},
			want: "none",
		},
		{
			name:  "instruction paired with mismatched raw body",
			isBot: "false",
			payload: map[string]any{
				"_normalized_event": map[string]any{
					"transition": map[string]any{
						"kind":    "comment_added",
						"comment": map[string]any{"body": "/fs-review look again", "command": "/fs-fix", "instruction": "mismatched instruction must not be used"},
					},
				},
			},
			want: "none",
		},
		{
			name:    "command token with suffix",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", "/fs-fix-extra do something else"),
			want:    "none",
		},
		{
			name:    "command token glued to text",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", "/fs-fixplease do it"),
			want:    "none",
		},
		{
			name:    "command not at start of body",
			isBot:   "false",
			payload: canonicalCommentPayload("mr_note", "please run /fs-fix now"),
			want:    "none",
		},
		{
			name:    "non-object normalized event",
			isBot:   "false",
			payload: map[string]any{"_normalized_event": "/fs-fix string must not be used"},
			want:    "none",
		},
		{
			name:    "bot review redispatch",
			isBot:   "true",
			payload: canonicalCommentPayload("mr_note", "/fs-fix bot body must not become an instruction"),
			want:    "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			human, _, _ := runCommentContextSnippet(t, "fix", tt.isBot, tt.payload)
			assert.Equal(t, tt.want, human)
		})
	}
}

func TestRunAgentJobScript_FixMismatchedCommandWarns(t *testing.T) {
	human, _, out := runCommentContextSnippet(t, "fix", "false", canonicalCommentPayload("mr_note", "/fs-fix-extra x"))
	assert.Equal(t, "none", human)
	assert.Contains(t, out, "does not start with an exact /fs-fix command")
}

func TestRunAgentJobScript_RetroCommentFromCanonicalCommentBody(t *testing.T) {
	body := "/fs-retro what went wrong?\n\nThe fix missed the edge case."

	_, retro, _ := runCommentContextSnippet(t, "retro", "false", canonicalCommentPayload("issue_note", body))
	assert.Equal(t, body, retro)

	_, legacy, _ := runCommentContextSnippet(t, "retro", "false", map[string]any{"type": "issue_note", "note_body": body})
	assert.Empty(t, legacy, "retro must not fall back to the legacy note_body field")
}
