package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Live progress log levels (ADR 0163). A level limits what `fullsend run`
// prints to the job log while an agent runs: everything (full), everything
// but reasoning and reply text (tools), or only lines fullsend writes from
// its own fields (quiet).
const (
	ProgressVerbosityFull  = "full"
	ProgressVerbosityTools = "tools"
	ProgressVerbosityQuiet = "quiet"
)

// validProgressVerbosities lists the levels from least to most strict.
var validProgressVerbosities = []string{ProgressVerbosityFull, ProgressVerbosityTools, ProgressVerbosityQuiet}

// ValidProgressVerbosities returns the accepted progress.verbosity and
// progress.by_source values, from least to most strict.
func ValidProgressVerbosities() []string {
	out := make([]string, len(validProgressVerbosities))
	copy(out, validProgressVerbosities)
	return out
}

// ProgressConfig is the progress: block of config.yaml (ADR 0163).
//
// Verbosity applies to every run; empty inherits the parent layer (code
// default "full"). BySource maps a run source — the normalized event's
// source.system ("github", "gitlab", "jira", "manual", "schedule", or any
// later source) — to a level for runs started there. Keys are open
// strings; only the levels are validated. BySource merges per key through
// the layers.
type ProgressConfig struct {
	Verbosity string            `yaml:"verbosity,omitempty"`
	BySource  map[string]string `yaml:"by_source,omitempty"`
}

// IsZero reports whether the block sets nothing.
func (p *ProgressConfig) IsZero() bool {
	return p == nil || (p.Verbosity == "" && len(p.BySource) == 0)
}

// progressVerbosityRank orders levels by strictness: full < tools < quiet.
// An empty level ranks as full, as in the other helpers. An unknown level
// ranks strictest, so a comparison never treats it as a relaxation target
// or lets it lower an effective level.
func progressVerbosityRank(level string) int {
	switch level {
	case "", ProgressVerbosityFull:
		return 0
	case ProgressVerbosityTools:
		return 1
	default:
		return 2
	}
}

// StricterProgressVerbosity returns the stricter of two levels. An empty
// level counts as "full".
func StricterProgressVerbosity(a, b string) string {
	if a == "" {
		a = ProgressVerbosityFull
	}
	if b == "" {
		b = ProgressVerbosityFull
	}
	if progressVerbosityRank(b) > progressVerbosityRank(a) {
		return b
	}
	return a
}

// ProgressHidesText reports whether a level leaves reasoning and reply
// text out of the live log: tools, quiet, or any unknown non-empty level.
// Empty counts as full.
func ProgressHidesText(level string) bool {
	return level != "" && level != ProgressVerbosityFull
}

// ProgressIsQuiet reports whether a level prints only lines fullsend
// writes from its own fields. An unknown non-empty level counts as quiet,
// so a bad value never prints more. Empty counts as full.
func ProgressIsQuiet(level string) bool {
	return ProgressHidesText(level) && level != ProgressVerbosityTools
}

func validProgressVerbosity(level string) bool {
	return slices.Contains(validProgressVerbosities, level)
}

// validateProgress checks a layer's own progress block: verbosity and
// every by_source value must be a known level. Keys are open strings, but
// must be non-empty, lowercase and without surrounding spaces: source
// names are lowercase (event.source.system), and a key that differs only
// in case would silently never match.
func validateProgress(p *ProgressConfig) error {
	if p == nil {
		return nil
	}
	levels := strings.Join(validProgressVerbosities, ", ")
	if p.Verbosity != "" && !validProgressVerbosity(p.Verbosity) {
		return fmt.Errorf("invalid progress.verbosity %q: must be one of %s", p.Verbosity, levels)
	}
	for _, k := range sortedKeys(p.BySource) {
		norm := strings.ToLower(strings.TrimSpace(k))
		if norm == "" {
			return fmt.Errorf("progress.by_source: source key must not be empty")
		}
		if k != norm {
			return fmt.Errorf("invalid progress.by_source key %q: source keys are lowercase with no surrounding spaces; use %q", k, norm)
		}
		if v := p.BySource[k]; !validProgressVerbosity(v) {
			return fmt.Errorf("invalid progress.by_source.%s %q: must be one of %s", k, v, levels)
		}
	}
	return nil
}

// ResolveProgressVerbosity returns the live progress log level for a run
// started from source: the stricter of the effective progress.verbosity
// and the effective progress.by_source entry for source. A source with no
// entry gets the verbosity level. The source is trimmed and lowercased
// before the lookup; a by_source key that is not lowercase is an error.
// An empty source means the run's source is unknown: it gets the
// strictest of verbosity and every by_source entry, so a source level is
// never skipped because the source could not be read. A nil reader (no
// configuration) resolves to "full". An invalid level in any layer is an
// error; callers log at "quiet" when it fails (ADR 0163).
func ResolveProgressVerbosity(r PerRepoConfigReader, source string) (string, error) {
	if r == nil {
		return ProgressVerbosityFull, nil
	}
	verbosity := r.ConfigProgressVerbosity()
	if verbosity == "" {
		verbosity = ProgressVerbosityFull
	}
	if !validProgressVerbosity(verbosity) {
		return "", fmt.Errorf("invalid progress.verbosity %q: must be one of %s", verbosity, strings.Join(validProgressVerbosities, ", "))
	}
	bySource := r.ConfigProgressBySource()
	// The run-time config load does not run validateProgress, so check
	// keys and values here: a key that is not lowercase would never match
	// and must not resolve to a weaker level.
	if err := validateProgress(&ProgressConfig{BySource: bySource}); err != nil {
		return "", err
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		level := verbosity
		for _, v := range bySource {
			level = StricterProgressVerbosity(level, v)
		}
		return level, nil
	}
	return StricterProgressVerbosity(verbosity, bySource[source]), nil
}

// ConfigProgressVerbosity returns the effective progress.verbosity: the
// nearest layer that sets it, else the parent chain (code default "full").
func (c *perRepoConfig) ConfigProgressVerbosity() string {
	if c.Progress != nil && c.Progress.Verbosity != "" {
		return c.Progress.Verbosity
	}
	if c.parent != nil {
		return c.parent.ConfigProgressVerbosity()
	}
	return ProgressVerbosityFull
}

// ConfigProgressBySource returns the effective progress.by_source map,
// merged per key through the parent chain: a local key overrides the same
// key from the parent, and unstated keys inherit the parent's value.
func (c *perRepoConfig) ConfigProgressBySource() map[string]string {
	var parent map[string]string
	if c.parent != nil {
		parent = c.parent.ConfigProgressBySource()
	}
	if c.Progress == nil || len(c.Progress.BySource) == 0 {
		return parent
	}
	merged := make(map[string]string, len(parent)+len(c.Progress.BySource))
	maps.Copy(merged, parent)
	maps.Copy(merged, c.Progress.BySource)
	return merged
}

// cloneProgress deep-copies a progress block.
func cloneProgress(src *ProgressConfig) *ProgressConfig {
	if src == nil {
		return nil
	}
	out := &ProgressConfig{Verbosity: src.Verbosity}
	if src.BySource != nil {
		out.BySource = maps.Clone(src.BySource)
	}
	return out
}

// mergeProgress layers child over parent: an explicit child verbosity
// overrides, and by_source merges per key.
func mergeProgress(parent, child *ProgressConfig) *ProgressConfig {
	if child.IsZero() {
		return cloneProgress(parent)
	}
	out := cloneProgress(parent)
	if out == nil {
		out = &ProgressConfig{}
	}
	if child.Verbosity != "" {
		out.Verbosity = child.Verbosity
	}
	if len(child.BySource) > 0 {
		if out.BySource == nil {
			out.BySource = make(map[string]string, len(child.BySource))
		}
		maps.Copy(out.BySource, child.BySource)
	}
	return out
}

// progressRelaxations reports effective progress levels the candidate
// lowers toward "full" without declaring the lowering locally (ADR 0163,
// ADR 0122 safety gate). Lowering verbosity needs a local
// progress.verbosity; lowering a source's effective level by dropping or
// lowering its by_source entry needs a local progress.by_source entry for
// that source.
func progressRelaxations(current PerRepoConfigReader, candidate PerRepoConfigWriter, local *perRepoConfig) []SafetyRelaxation {
	var localProgress *ProgressConfig
	if local != nil {
		localProgress = local.Progress
	}
	var out []SafetyRelaxation

	curVerbosity := StricterProgressVerbosity(current.ConfigProgressVerbosity(), "")
	candVerbosity := StricterProgressVerbosity(candidate.ConfigProgressVerbosity(), "")
	if progressVerbosityRank(candVerbosity) < progressVerbosityRank(curVerbosity) &&
		(localProgress == nil || localProgress.Verbosity == "") {
		out = append(out, SafetyRelaxation{
			Key:       "progress.verbosity",
			Current:   curVerbosity,
			Candidate: candVerbosity,
		})
	}

	curBySource := current.ConfigProgressBySource()
	candBySource := candidate.ConfigProgressBySource()
	keys := make(map[string]struct{}, len(curBySource)+len(candBySource))
	for k := range curBySource {
		keys[k] = struct{}{}
	}
	for k := range candBySource {
		keys[k] = struct{}{}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		curEff := StricterProgressVerbosity(curVerbosity, curBySource[k])
		candEff := StricterProgressVerbosity(candVerbosity, candBySource[k])
		if progressVerbosityRank(candEff) >= progressVerbosityRank(curEff) {
			continue
		}
		curEntry := StricterProgressVerbosity(curBySource[k], "")
		candEntry := StricterProgressVerbosity(candBySource[k], "")
		if progressVerbosityRank(candEntry) >= progressVerbosityRank(curEntry) {
			// The entry did not drop; the verbosity check above covers it.
			continue
		}
		if localProgress != nil {
			if _, declared := localProgress.BySource[k]; declared {
				continue
			}
		}
		out = append(out, SafetyRelaxation{
			Key:       "progress.by_source." + k,
			Current:   curEff,
			Candidate: candEff,
		})
	}
	return out
}
