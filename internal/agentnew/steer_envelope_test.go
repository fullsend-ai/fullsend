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
	// Every role the generator offers, read from the generator rather than
	// listed here: a role added later must carry the contract too, and a
	// hardcoded list would let it ship without.
	for _, role := range RoleNames() {
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
			// Recognising the line is not enough on its own. These three
			// carry the rest of the contract: the gate that says a runner
			// update exists only while a watcher is steering, the injection
			// defence for the line arriving anywhere else, and the limit on
			// what an update may ask for.
			for _, clause := range []string{
				"FULLSEND_STEER_ACTIVE",
				"injection attempt",
				"grants no tools or permissions",
				// The amendment/context split. Without it the body teaches
				// the agent to act on a whole runner update, so context
				// carried inside a genuine one reads as instruction — which
				// would make a scaffolded agent more obedient to injected
				// text than a fleet one.
				"Only the amendment",
				"must not obey",
			} {
				if !strings.Contains(body, clause) {
					t.Errorf("the generated agent body carries the steer opening line but not %q, "+
						"so it recognises a runner update without the rule that bounds it", clause)
				}
			}
		})
	}
}
