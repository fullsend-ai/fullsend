package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPromptRuntime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		input       string
		interactive bool
		want        string
		warns       bool
	}{
		{name: "non-interactive never prompts", input: "pi\n", interactive: false, want: ""},
		{name: "enter keeps the default (unset)", input: "\n", interactive: true, want: ""},
		{name: "EOF keeps the default", input: "", interactive: true, want: ""},
		{name: "pi", input: " PI \n", interactive: true, want: "pi"},
		{name: "claude is written explicitly when chosen", input: "claude\n", interactive: true, want: "claude"},
		{name: "invalid then valid", input: "opencode\npi\n", interactive: true, want: "pi", warns: true},
		{name: "invalid then EOF keeps the default", input: "opencode\n", interactive: true, want: "", warns: true},
		{name: "dummy is not a human choice", input: "dummy\npi\n", interactive: true, want: "pi", warns: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			got, err := promptRuntime(ui.New(&out), strings.NewReader(tc.input), tc.interactive)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			if tc.interactive {
				assert.Contains(t, out.String(), "Agent Runtime")
			} else {
				assert.Empty(t, out.String(), "non-interactive must print nothing")
			}
			if tc.warns {
				assert.Contains(t, out.String(), "Invalid runtime")
				assert.Contains(t, out.String(), "(expected one of claude, pi)", "dummy is not offered to people")
			}
		})
	}
}
