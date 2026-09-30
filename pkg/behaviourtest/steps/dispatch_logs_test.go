package steps

import "testing"

func TestHasAgentWorkflowEvidence(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		agent string
		want  bool
	}{
		{name: "current routed stage", line: "Routed to stage: triage", agent: "triage", want: true},
		{name: "current stage job", line: "Run review agent", agent: "review", want: true},
		{name: "all stage names", line: "Routed to stage: code", agent: "code", want: true},
		{name: "wrong stage", line: "Routed to stage: code", agent: "triage", want: false},
		{name: "unrelated agent text", line: "agent triage is configured", agent: "triage", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasAgentWorkflowEvidence(tt.line, tt.agent); got != tt.want {
				t.Fatalf("hasAgentWorkflowEvidence(%q, %q) = %v, want %v", tt.line, tt.agent, got, tt.want)
			}
		})
	}
}
