package security

// CanonicalClaudeTools is the set of tool names Claude Code exposes today.
// It is the canonical hook vocabulary (ADR 0090, #608): FULLSEND_TOOL_ALLOWLIST
// entries and HookGroup.Tools are written in these names, and a runtime adapter
// (pi: claudeToolForPi → fullsend-manifest.json hooks.toolNames →
// fullsend-hooks.js claudeToolName) translates its own names to them before
// any hook script runs. A name the adapter did not translate is still blocked
// (the allowlist is exact-match, fail-closed) but tool_allowlist_pretool.py
// reports it as an adapter gap rather than a forbidden tool.
//
// Verified 2026-08-23 against the live tools reference
// (https://code.claude.com/docs/en/tools-reference, which documents the
// latest release — 2.1.241 at the time); the CHANGELOG for 2.1.235–2.1.241
// records no tool additions or removals relative to the 2.1.234 pinned in
// images/sandbox/Containerfile. Re-check on every pin bump.
//
// MCP tool names (mcp__<server>__<tool>) are not canonical: they pass through
// verbatim and the allowlist matches them exactly, so the hook never treats
// their case variants as a translation gap.
//
// The hook script carries its own copy (CANONICAL_TOOLS / LEGACY_TOOLS in
// hooks/tool_allowlist_pretool.py) because the scripts have no access to Go;
// TestToolAllowlistHook_VocabularyMatchesGo keeps the two identical.
var CanonicalClaudeTools = map[string]bool{
	"Agent":                true,
	"Artifact":             true,
	"AskUserQuestion":      true,
	"Bash":                 true,
	"CronCreate":           true,
	"CronDelete":           true,
	"CronList":             true,
	"Edit":                 true,
	"EndConversation":      true,
	"EnterPlanMode":        true,
	"EnterWorktree":        true,
	"ExitPlanMode":         true,
	"ExitWorktree":         true,
	"Glob":                 true,
	"Grep":                 true,
	"ListAgents":           true,
	"ListMcpResourcesTool": true,
	"LSP":                  true,
	"Monitor":              true,
	"NotebookEdit":         true,
	"PowerShell":           true,
	"PushNotification":     true,
	"Read":                 true,
	"ReadMcpResourceTool":  true,
	"RemoteTrigger":        true,
	"ReportFindings":       true,
	"ScheduleWakeup":       true,
	"SendMessage":          true,
	"SendUserFile":         true,
	"ShareOnboardingGuide": true,
	"Skill":                true,
	"TaskCreate":           true,
	"TaskGet":              true,
	"TaskList":             true,
	"TaskOutput":           true,
	"TaskStop":             true,
	"TaskUpdate":           true,
	"TodoWrite":            true,
	"ToolSearch":           true,
	"WaitForMcpServers":    true,
	"WebFetch":             true,
	"WebSearch":            true,
	"Workflow":             true,
	"Write":                true,
}

// LegacyClaudeTools are tool names Claude Code no longer exposes but which
// still appear in agent `tools:` frontmatter and in runtime adapter maps (the
// pi adapter reports pi's `ls` as LS and accepts MultiEdit for its edit tool).
// Claude Code itself never sends these to a hook; an adapter may. The value is
// the current replacement, "" when there is none.
var LegacyClaudeTools = map[string]string{
	"LS":           "",
	"MultiEdit":    "Edit",
	"NotebookRead": "Read",
	"Task":         "Agent",
	"TodoRead":     "",
}

// KnownClaudeTool reports whether name is a canonical or legacy Claude tool
// name — i.e. a name the hook scripts can be expected to see from a runtime
// adapter.
func KnownClaudeTool(name string) bool {
	if CanonicalClaudeTools[name] {
		return true
	}
	_, legacy := LegacyClaudeTools[name]
	return legacy
}
