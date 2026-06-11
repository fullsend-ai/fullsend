package runtime

import "github.com/fullsend-ai/fullsend/internal/security"

// ClaudeHooksBootstrap is an optional extension for Claude Code sandbox tool hooks.
// ClaudeRuntime.Bootstrap type-asserts for this; other runtimes ignore it.
type ClaudeHooksBootstrap interface {
	ClaudeSandboxHooks() security.ClaudeSandboxHooks
}
