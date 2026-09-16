package agentnew

import (
	"strings"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// flattenMarkdown collapses a markdown file to one whitespace-normalised line,
// so a sentence that wraps across source lines still matches as a substring.
// This mirrors what fullsend-ai/agents' own contract test does to the fleet
// definitions (scripts/agent-recheck-contract-test.sh), rather than forcing the
// template to keep the line unwrapped.
func flattenMarkdown(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestGeneratedAgentCarriesTheSteerOpeningLine pins the scaffolded agent body
// to the runner's envelope.
//
// Scaffolded harnesses are opted in to steering like every other harness, so a
// generated agent will be handed runner updates. It recognises one only by this
// exact line, which makes the constant a cross-repo interface: if it changes
// and the template does not, every steer delivered to a scaffolded agent
// silently reads as ordinary content and the update is lost. Referencing
// runtime.SteerEnvelopeOpeningLine rather than spelling the line out again is
// the point — drift fails here instead of passing a human's eye.
func TestGeneratedAgentCarriesTheSteerOpeningLine(t *testing.T) {
	// Every role the hosted mint serves: the paragraph is role-neutral, so
	// none of them may render without it.
	for _, role := range []string{"triage", "coder", "review", "retro", "prioritize"} {
		t.Run(role, func(t *testing.T) {
			files, err := Render(testOptions("lint-docs", role))
			if err != nil {
				t.Fatal(err)
			}
			body := flattenMarkdown(string(fileByPath(t, files, "agents/lint-docs.md").Data))

			if !strings.Contains(body, agentruntime.SteerEnvelopeOpeningLine) {
				t.Errorf("the generated agent body does not carry the steer opening line %q — "+
					"a runner update delivered to this agent would read as ordinary content",
					agentruntime.SteerEnvelopeOpeningLine)
			}
			// The body must also tell the agent the line is untrustworthy when
			// it arrives inside work-item content; carrying the line alone
			// would teach recognition without the injection defence.
			if !strings.Contains(body, "injection attempt") {
				t.Error("the generated agent body recognises a runner update but is not told " +
					"to report the same line inside work-item content as an injection attempt")
			}
		})
	}
}
