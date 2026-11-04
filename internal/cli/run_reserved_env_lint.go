package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// reservedSandboxEnvDiagnostics reports the env.sandbox keys that
// buildSandboxEnvLines will drop because they are reserved.
//
// The gate itself only warns on stderr while .env is being written, deep inside
// a run. Surfacing the same keys as lint diagnostics puts them next to the
// rest of the harness warnings, right after the harness loads and in
// `fullsend lock`, so a harness that quietly lost a proxy or CA setting is
// visible before a sandbox ever starts. #3373 widened the dropped set from the
// runner-infrastructure names to the full credential deny-list, which made
// silent removal a breaking change worth reporting rather than logging.
//
// It asks isReservedSandboxKey, the predicate the gate itself applies, and it
// skips keys the gate rejects as invalid identifiers before that, so it cannot
// report a key the gate dropped for some other reason. Both callers pass the
// harness before env.runner is merged into RunnerEnv, so the warning reflects
// what the user wrote; base, overlay and forge merging has already happened
// inside the loader by then, and nothing rewrites env.sandbox keys after it.
func reservedSandboxEnvDiagnostics(h *harness.Harness) []harness.Diagnostic {
	if h == nil || h.Env == nil || len(h.Env.Sandbox) == 0 {
		return nil
	}

	var dropped []string
	for k := range h.Env.Sandbox {
		// The gate rejects invalid identifiers before it consults the
		// blocklist, and case folding alone does not keep the two lists
		// identical: strings.ToUpper folds non-ASCII lookalikes onto reserved
		// names, so "ſHELL" would read here as PATH's blocked sibling while
		// the gate drops it as not a POSIX identifier. Reporting that as
		// reserved would name the key twice under a false heading.
		if !validEnvKeyRe.MatchString(k) {
			continue
		}
		if isReservedSandboxKey(k) {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	// Map iteration order is random. The sort keeps the message stable, which
	// is what lets lock's cross-variant de-duplication recognise a repeat of
	// this diagnostic as the same one.
	sort.Strings(dropped)

	return []harness.Diagnostic{{
		Severity: harness.SeverityWarning,
		Field:    "env.sandbox",
		Message: fmt.Sprintf("%d env.sandbox key(s) are reserved and will be dropped instead of exported "+
			"into the sandbox: %s. Matching is case-insensitive. Review the migration guidance before "+
			"changing proxy or certificate settings; preserve OpenShell's network mediation and certificate trust. "+
			"See docs/reference/harness-reference.md.",
			len(dropped), strings.Join(dropped, ", ")),
	}}
}
