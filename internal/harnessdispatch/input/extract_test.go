package input

import "testing"

func TestExtractCommentCommand_TrailingPunctuation(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantCommand     string
		wantInstruction string
	}{
		{"clean command", "/fs-fix", "/fs-fix", ""},
		{"single period", "/fs-fix.", "/fs-fix", ""},
		{"ellipsis", "/fs-fix...", "/fs-fix", ""},
		{"question-exclamation", "/fs-fix?!", "/fs-fix", ""},
		{"period with instruction", "/fs-fix. please rebase", "/fs-fix", "please rebase"},
		{"ellipsis with instruction", "/fs-triage... check this", "/fs-triage", "check this"},
		{"non-slash command", "just a comment", "just", "a comment"},
		{"empty body", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, instruction := extractCommentCommand(tt.body)
			if cmd != tt.wantCommand {
				t.Errorf("command = %q, want %q", cmd, tt.wantCommand)
			}
			if instruction != tt.wantInstruction {
				t.Errorf("instruction = %q, want %q", instruction, tt.wantInstruction)
			}
		})
	}
}
