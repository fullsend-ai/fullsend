package harness

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DiagnosticSeverity indicates whether a diagnostic is a warning or an error.
type DiagnosticSeverity int

const (
	SeverityWarning DiagnosticSeverity = iota
	SeverityError
)

// String returns a human-readable description of the diagnostic severity.
func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	default:
		return fmt.Sprintf("DiagnosticSeverity(%d)", int(s))
	}
}

// preflightPathLike matches a bare script path — an optional leading
// "."/"/" then path characters, ending in a script extension — with no
// spaces, quotes, or shell operators. preflight_check is passed verbatim
// to `sh -c` on the host and, unlike pre_script/post_script, is never
// resolved through the fetch/cache pipeline (#7600). Referenced files are
// not delivered (Harness Resource Distribution Contract), so a bare
// path-shaped value cannot resolve in a fresh consumer checkout: the
// mistake is relying on undeclared host state, not a style preference.
// The interpreter form (`sh path.sh`) does not match and
// remains the intentional escape hatch.
var preflightPathLike = regexp.MustCompile(`^[./]?[\w./-]+\.(sh|py|rb|js)$`)

// issueURLRef matches an actual GITHUB_ISSUE_URL reference in a value:
// either expansion form os.Expand recognizes. A bare $name extends to
// the longest identifier, so $GITHUB_ISSUE_URLS is a different variable,
// and a literal substring ("see GITHUB_ISSUE_URL") is not a reference.
var issueURLRef = regexp.MustCompile(`\$\{GITHUB_ISSUE_URL\}|\$GITHUB_ISSUE_URL([^A-Za-z0-9_]|$)`)

// DeprecatedIssueURLWarning is emitted for env entries and host_files
// sources that reference GITHUB_ISSUE_URL (#6610). The caveat covers the
// reusable prioritize workflow, which does not yet export
// FULLSEND_WORK_ITEM_URL.
const DeprecatedIssueURLWarning = "GITHUB_ISSUE_URL is deprecated; use FULLSEND_WORK_ITEM_URL instead (see #6610); " +
	"keep GITHUB_ISSUE_URL for harnesses run by the reusable prioritize workflow, which does not export FULLSEND_WORK_ITEM_URL yet"

// Diagnostic represents a non-fatal issue found by Lint.
type Diagnostic struct {
	Severity DiagnosticSeverity
	Field    string
	Message  string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Field, d.Message)
}

// Lint returns non-fatal diagnostics for the harness. Call only after a
// successful Validate — Lint does not re-check structural validity, and its
// results are meaningless on an invalid harness.
// Returns nil when no diagnostics are found.
func (h *Harness) Lint() []Diagnostic {
	var diags []Diagnostic

	if len(h.RunnerEnv) > 0 {
		msg := "runner_env is deprecated; use env.runner instead (see ADR 0055)"
		if h.Env != nil && len(h.Env.Runner) > 0 {
			msg = "runner_env is deprecated and env.runner takes precedence; migrate to env.runner (see ADR 0055)"
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "runner_env",
			Message:  msg,
		})
	}

	// Warn when env.sandbox is present alongside host_files entries that
	// deliver .env files to .env.d/ with expand: true, since env.sandbox
	// takes precedence on key collision (may shadow host_files values).
	if h.Env != nil && len(h.Env.Sandbox) > 0 {
		for _, hf := range h.HostFiles {
			if hf.Expand && strings.Contains(hf.Dest, ".env.d/") {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    "env.sandbox",
					Message:  fmt.Sprintf("env.sandbox coexists with host_files entry %s (dest: %s); env.sandbox values take precedence on key collision", hf.Src, hf.Dest),
				})
				break // one warning is enough
			}
		}
	}

	// allowed_remote_resources used to start the runtime fetch service
	// implicitly; that implicit opt-in is deprecated (#7155). Mirror
	// shouldStartFetchService: URL-dir resources (skills/plugins) or an
	// explicit allow_runtime_fetch start the service for non-deprecated
	// reasons, so only a bare declaration warns.
	if len(h.AllowedRemoteResources) > 0 && !h.AllowRuntimeFetch && !h.HasURLDirResources() {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "allowed_remote_resources",
			Message:  "allowed_remote_resources is declared without allow_runtime_fetch: true; the runtime fetch service will start for backward compatibility, but this behavior is deprecated — add allow_runtime_fetch: true to the harness to silence this warning",
		})
	}

	if h.hadForgeBeforeResolve {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "forge",
			Message:  "forge is deprecated; use overlays with CEL when expressions instead (see ADR 0088)",
		})
	}

	if strings.TrimSpace(h.Trigger) != "" {
		if err := ValidateTriggerExpression(h.Trigger); err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Field:    "trigger",
				Message:  err.Error(),
			})
		}
	}

	// ADR 0128 Decision 2 (#7600): preflight_check is a literal sh -c
	// command, never a fetched script resource, so a bare path-like value
	// names a file the host will not have. One shared predicate covers the
	// top-level field and the nested validation_loop.preflight_check.
	// Non-fatal: lock/run print the diagnostic and continue.
	if d := lintPreflightCheckCommand("preflight_check", h.PreflightCheck); d != nil {
		diags = append(diags, *d)
	}
	if h.ValidationLoop != nil {
		if d := lintPreflightCheckCommand("validation_loop.preflight_check", h.ValidationLoop.PreflightCheck); d != nil {
			diags = append(diags, *d)
		}
		// ADR 0129 Decision 3: deprecate the nested field now that the
		// top-level replacement ships in the same version ("only once
		// the replacement is usable"). The warning is advisory — nested
		// checks still run during migration, and ADR 0129 Decision 5
		// requires keeping them until every pinned consumer is verified
		// to run a CLI that executes the top-level field, so the wording
		// says both things rather than urging immediate removal. Consumer
		// rollout and harness migration are external
		// (fullsend-ai/agents#1753); removal of the nested field needs a
		// separately decided version gate (ADR 0127).
		if h.ValidationLoop.PreflightCheck != "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    "validation_loop.preflight_check",
				Message:  "validation_loop.preflight_check is deprecated; prefer the top-level preflight_check, and keep the nested check in place until every pinned consumer runs a CLI that executes the top-level field (consumer rollout is tracked in fullsend-ai/agents#1753; see ADR 0129)",
			})
		}
	}

	diags = append(diags, h.lintDeprecatedIssueURL()...)

	return diags
}

// lintPreflightCheckCommand returns an error-level diagnostic when value, a
// preflight_check command, looks like a bare script path — usually meaning
// the author expected pre_script-style fetch/resolution (#7600). Referenced
// files are not delivered (Harness Resource Distribution Contract), so the
// value cannot resolve in a fresh consumer checkout: the mistake is relying
// on undeclared host state, not a style preference (ADR 0128 Decision 2).
// The interpreter form (`sh path.sh`) does not match and
// remains the intentional escape hatch. The field label is interpolated
// into the message so sibling preflight_check fields can reuse the guard
// (#8722). Returns nil for clean values.
func lintPreflightCheckCommand(field, value string) *Diagnostic {
	pc := strings.TrimSpace(value)
	if pc == "" || !preflightPathLike.MatchString(pc) {
		return nil
	}
	return &Diagnostic{
		Severity: SeverityError,
		Field:    field,
		Message:  fmt.Sprintf("%q looks like a script path, but %s is a literal sh -c command and is not fetched like pre_script; inline the probe instead (e.g. python3 -c 'import jsonschema') (see ADR 0128)", pc, field),
	}
}

// lintDeprecatedIssueURL reports env entries and host_files sources that
// reference the deprecated GITHUB_ISSUE_URL variable, either as the entry
// key itself or as a $GITHUB_ISSUE_URL / ${GITHUB_ISSUE_URL} expansion
// in the value (#6610). Checked across the top-level
// harness and unconsumed forge sections. Overlays are not scanned: every
// loader runs ResolveOverlays before Lint(), which merges matched overlays
// into the top-level fields (scanned there) and drops the rest (#8742).
// The offending entry key is quoted in the message so harness-controlled key text cannot inject
// control characters into diagnostic output.
func (h *Harness) lintDeprecatedIssueURL() []Diagnostic {
	var diags []Diagnostic

	checkEnv := func(m map[string]string, field string) {
		for _, k := range deprecatedIssueURLKeys(m) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    field,
				Message:  fmt.Sprintf("%s (entry %q)", DeprecatedIssueURLWarning, k),
			})
		}
	}
	checkHostFiles := func(hfs []HostFile, prefix string) {
		for i, hf := range hfs {
			if !issueURLRef.MatchString(hf.Src) {
				continue
			}
			field := fmt.Sprintf("host_files[%d].src", i)
			if prefix != "" {
				field = fmt.Sprintf("%s.host_files[%d].src", prefix, i)
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    field,
				Message:  DeprecatedIssueURLWarning,
			})
		}
	}
	checkForgeConfig := func(fc *ForgeConfig, prefix string) {
		if fc == nil {
			return
		}
		checkEnv(fc.RunnerEnv, prefix+".runner_env")
		if fc.Env != nil {
			checkEnv(fc.Env.Runner, prefix+".env.runner")
			checkEnv(fc.Env.Sandbox, prefix+".env.sandbox")
		}
		checkHostFiles(fc.HostFiles, prefix)
	}

	checkEnv(h.RunnerEnv, "runner_env")
	if h.Env != nil {
		checkEnv(h.Env.Runner, "env.runner")
		checkEnv(h.Env.Sandbox, "env.sandbox")
	}
	checkHostFiles(h.HostFiles, "")
	if len(h.Forge) > 0 {
		var forgeKeys []string
		for k := range h.Forge {
			forgeKeys = append(forgeKeys, k)
		}
		sort.Strings(forgeKeys)
		for _, k := range forgeKeys {
			checkForgeConfig(h.Forge[k], fmt.Sprintf("forge[%s]", k))
		}
	}
	return diags
}

// deprecatedIssueURLKeys returns the sorted keys of m that reference the
// deprecated GITHUB_ISSUE_URL variable: either the entry is named after
// it, or its value references it via a $ / ${} expansion.
func deprecatedIssueURLKeys(m map[string]string) []string {
	var keys []string
	for k, v := range m {
		if k == "GITHUB_ISSUE_URL" || issueURLRef.MatchString(v) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
