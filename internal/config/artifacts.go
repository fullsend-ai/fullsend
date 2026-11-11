package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Run artifact content levels (ADR 0161). A level limits what the run
// artifact publishes: everything in output/ (all), only the run-level
// metadata files (metadata), or no artifact at all (none).
const (
	ArtifactContentAll      = "all"
	ArtifactContentMetadata = "metadata"
	ArtifactContentNone     = "none"
)

// validArtifactContentLevels lists the levels from least to most strict.
var validArtifactContentLevels = []string{ArtifactContentAll, ArtifactContentMetadata, ArtifactContentNone}

// ValidArtifactContentLevels returns the accepted artifacts.content and
// artifacts.by_source values, from least to most strict.
func ValidArtifactContentLevels() []string {
	out := make([]string, len(validArtifactContentLevels))
	copy(out, validArtifactContentLevels)
	return out
}

// ArtifactsConfig is the artifacts: block of config.yaml (ADR 0161).
//
// Content applies to every run; empty inherits the parent layer (code
// default "all"). BySource maps a run source — the normalized event's
// source.system ("github", "gitlab", "jira", "manual", "schedule", or any
// later source) — to a level for runs started there. Keys are open
// strings; only the levels are validated. BySource merges per key through
// the layers.
type ArtifactsConfig struct {
	Content  string            `yaml:"content,omitempty"`
	BySource map[string]string `yaml:"by_source,omitempty"`
}

// IsZero reports whether the block sets nothing.
func (a *ArtifactsConfig) IsZero() bool {
	return a == nil || (a.Content == "" && len(a.BySource) == 0)
}

// artifactContentRank orders levels by strictness: all < metadata < none.
// An unknown level ranks strictest, so a comparison never treats it as a
// relaxation target or lets it lower an effective level.
func artifactContentRank(level string) int {
	switch level {
	case ArtifactContentAll:
		return 0
	case ArtifactContentMetadata:
		return 1
	default:
		return 2
	}
}

// StricterArtifactContent returns the stricter of two levels. An empty
// level counts as "all".
func StricterArtifactContent(a, b string) string {
	if a == "" {
		a = ArtifactContentAll
	}
	if b == "" {
		b = ArtifactContentAll
	}
	if artifactContentRank(b) > artifactContentRank(a) {
		return b
	}
	return a
}

func validArtifactContent(level string) bool {
	return slices.Contains(validArtifactContentLevels, level)
}

// validateArtifacts checks a layer's own artifacts block: content and
// every by_source value must be a known level. Keys are open strings and
// only need to be non-empty.
func validateArtifacts(a *ArtifactsConfig) error {
	if a == nil {
		return nil
	}
	levels := strings.Join(validArtifactContentLevels, ", ")
	if a.Content != "" && !validArtifactContent(a.Content) {
		return fmt.Errorf("invalid artifacts.content %q: must be one of %s", a.Content, levels)
	}
	for _, k := range sortedKeys(a.BySource) {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("artifacts.by_source: source key must not be empty")
		}
		if v := a.BySource[k]; !validArtifactContent(v) {
			return fmt.Errorf("invalid artifacts.by_source.%s %q: must be one of %s", k, v, levels)
		}
	}
	return nil
}

// ResolveArtifactContent returns the run artifact level for a run started
// from source: the stricter of the effective artifacts.content and the
// effective artifacts.by_source entry for source. A source with no entry
// (including an empty source) gets the content level. A nil reader (no
// configuration) resolves to "all". An invalid level in any layer is an
// error, so callers can fail closed.
func ResolveArtifactContent(r PerRepoConfigReader, source string) (string, error) {
	if r == nil {
		return ArtifactContentAll, nil
	}
	content := r.ConfigArtifactsContent()
	if content == "" {
		content = ArtifactContentAll
	}
	if !validArtifactContent(content) {
		return "", fmt.Errorf("invalid artifacts.content %q: must be one of %s", content, strings.Join(validArtifactContentLevels, ", "))
	}
	bySource := r.ConfigArtifactsBySource()
	for _, k := range sortedKeys(bySource) {
		if v := bySource[k]; !validArtifactContent(v) {
			return "", fmt.Errorf("invalid artifacts.by_source.%s %q: must be one of %s", k, v, strings.Join(validArtifactContentLevels, ", "))
		}
	}
	if source == "" {
		return content, nil
	}
	return StricterArtifactContent(content, bySource[source]), nil
}

// ConfigArtifactsContent returns the effective artifacts.content: the
// nearest layer that sets it, else the parent chain (code default "all").
func (c *perRepoConfig) ConfigArtifactsContent() string {
	if c.Artifacts != nil && c.Artifacts.Content != "" {
		return c.Artifacts.Content
	}
	if c.parent != nil {
		return c.parent.ConfigArtifactsContent()
	}
	return ArtifactContentAll
}

// ConfigArtifactsBySource returns the effective artifacts.by_source map,
// merged per key through the parent chain: a local key overrides the same
// key from the parent, and unstated keys inherit the parent's value.
func (c *perRepoConfig) ConfigArtifactsBySource() map[string]string {
	var parent map[string]string
	if c.parent != nil {
		parent = c.parent.ConfigArtifactsBySource()
	}
	if c.Artifacts == nil || len(c.Artifacts.BySource) == 0 {
		return parent
	}
	merged := make(map[string]string, len(parent)+len(c.Artifacts.BySource))
	maps.Copy(merged, parent)
	maps.Copy(merged, c.Artifacts.BySource)
	return merged
}

// cloneArtifacts deep-copies an artifacts block.
func cloneArtifacts(src *ArtifactsConfig) *ArtifactsConfig {
	if src == nil {
		return nil
	}
	out := &ArtifactsConfig{Content: src.Content}
	if src.BySource != nil {
		out.BySource = maps.Clone(src.BySource)
	}
	return out
}

// mergeArtifacts layers child over parent: an explicit child content
// overrides, and by_source merges per key.
func mergeArtifacts(parent, child *ArtifactsConfig) *ArtifactsConfig {
	if child.IsZero() {
		return cloneArtifacts(parent)
	}
	out := cloneArtifacts(parent)
	if out == nil {
		out = &ArtifactsConfig{}
	}
	if child.Content != "" {
		out.Content = child.Content
	}
	if len(child.BySource) > 0 {
		if out.BySource == nil {
			out.BySource = make(map[string]string, len(child.BySource))
		}
		maps.Copy(out.BySource, child.BySource)
	}
	return out
}

// artifactsRelaxations reports effective artifact levels the candidate
// lowers toward "all" without declaring the lowering locally (ADR 0161,
// ADR 0122 safety gate). Lowering content needs a local artifacts.content;
// lowering a source's effective level by dropping or lowering its
// by_source entry needs a local artifacts.by_source entry for that source.
func artifactsRelaxations(current PerRepoConfigReader, candidate PerRepoConfigWriter, local *perRepoConfig) []SafetyRelaxation {
	var localArtifacts *ArtifactsConfig
	if local != nil {
		localArtifacts = local.Artifacts
	}
	var out []SafetyRelaxation

	curContent := StricterArtifactContent(current.ConfigArtifactsContent(), "")
	candContent := StricterArtifactContent(candidate.ConfigArtifactsContent(), "")
	if artifactContentRank(candContent) < artifactContentRank(curContent) &&
		(localArtifacts == nil || localArtifacts.Content == "") {
		out = append(out, SafetyRelaxation{
			Key:       "artifacts.content",
			Current:   curContent,
			Candidate: candContent,
		})
	}

	curBySource := current.ConfigArtifactsBySource()
	candBySource := candidate.ConfigArtifactsBySource()
	keys := make(map[string]struct{}, len(curBySource)+len(candBySource))
	for k := range curBySource {
		keys[k] = struct{}{}
	}
	for k := range candBySource {
		keys[k] = struct{}{}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		curEff := StricterArtifactContent(curContent, curBySource[k])
		candEff := StricterArtifactContent(candContent, candBySource[k])
		if artifactContentRank(candEff) >= artifactContentRank(curEff) {
			continue
		}
		curEntry := StricterArtifactContent(curBySource[k], "")
		candEntry := StricterArtifactContent(candBySource[k], "")
		if artifactContentRank(candEntry) >= artifactContentRank(curEntry) {
			// The entry did not drop; the content check above covers it.
			continue
		}
		if localArtifacts != nil {
			if _, declared := localArtifacts.BySource[k]; declared {
				continue
			}
		}
		out = append(out, SafetyRelaxation{
			Key:       "artifacts.by_source." + k,
			Current:   curEff,
			Candidate: candEff,
		})
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}
