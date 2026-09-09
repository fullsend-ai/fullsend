package runtime

import (
	"fmt"
	"os"
)

// EffectiveModel returns the model a run will actually ask the runtime for:
// the value the runner resolved (flag > env > agents: entry > harness
// `model:`), or, when the runner resolved none, the agent definition's
// frontmatter `model:`. An empty result means neither named one and the
// runtime's own default applies.
//
// This is the single fallback chain. PiRuntime.Run builds `--model` from it
// and NeedsOpenAIProvider decides from it whether the run needs the OpenAI
// run-scoped provider, so the two cannot disagree about which model a run
// will call — a disagreement would either strand a frontmatter-pinned
// OpenAI agent without a credential or attach a live OpenAI credential to a
// run that never calls OpenAI (#6920).
func EffectiveModel(runModel, agentModel string) string {
	if runModel != "" {
		return runModel
	}
	return agentModel
}

// AgentDefinitionModel reads the frontmatter `model:` from a Claude-style
// agent definition. It returns "" when the file has no frontmatter, names no
// model, or cannot be read or parsed: the caller then falls back to the
// runtime default, which is what the run does today when nothing names a
// model. A definition that cannot be read at all fails the run later, in
// Bootstrap, with a better message than this function could give.
func AgentDefinitionModel(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	def, err := parsePiAgent(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent model resolution: skipped for %s: %v\n", path, err)
		return ""
	}
	return def.Model
}
