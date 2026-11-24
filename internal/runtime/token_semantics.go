package runtime

// OutputExcludesReasoning reports whether the named runtime's
// RunMetrics.OutputTokens leaves RunMetrics.ReasoningTokens out, so that the
// whole output is the sum of the two:
//
//   - codex: codexUsage.counters() subtracts reasoning from the wire output
//     count.
//   - opencode: OpenCode subtracts reasoning before it reports usage (getUsage
//     in packages/opencode/src/session/session.ts, read at the pinned
//     v1.19.1), and the parser passes its counters through.
//
// Claude Code and pi count reasoning inside output, so there ReasoningTokens
// is a breakdown of OutputTokens and this returns false. It also returns
// false for a name it does not know;
// TestOutputExcludesReasoning_ClassifiesEveryRuntime fails when a name is
// added to config.ValidRuntimes without being classified here. Recheck a
// runtime's entry when its pinned version changes.
func OutputExcludesReasoning(runtimeName string) bool {
	switch runtimeName {
	case "codex", "opencode":
		return true
	default:
		return false
	}
}
