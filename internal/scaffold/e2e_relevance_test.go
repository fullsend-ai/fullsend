package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// reusablePrioritizePath is the reusable workflow a change to which must run
// the live behaviour and playback suites: their scenarios dispatch
// prioritize.yml, which calls it (AISDLC-659).
const reusablePrioritizePath = ".github/workflows/reusable-prioritize.yml"

// e2eRelevanceFilterRe matches the `grep -qE '<filter>'; then` line of the
// changes step in e2e.yml — the same line scripts/check-live-test-relevance-test.sh
// extracts its filters from.
var e2eRelevanceFilterRe = regexp.MustCompile(`grep -qE '([^']+)'; then`)

// e2eRelevanceFilters returns the behaviour and playback relevance filters
// from the repo's own e2e.yml. There must be exactly two, one per suite, and
// they must be identical — the invariant the regression script also enforces.
func e2eRelevanceFilters(t *testing.T) []string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "e2e.yml"))
	require.NoError(t, err)
	matches := e2eRelevanceFilterRe.FindAllStringSubmatch(string(content), -1)
	filters := make([]string, 0, len(matches))
	for _, m := range matches {
		filters = append(filters, m[1])
	}
	require.Len(t, filters, 2, "expected one behaviour and one playback relevance filter")
	assert.Equal(t, filters[0], filters[1], "the behaviour and playback relevance filters must stay identical")
	return filters
}

// matchesFilter reports whether path is relevant under one of e2e.yml's
// relevance filters, the way the changes step greps a PR's file list.
func matchesFilter(t *testing.T, filter, path string) bool {
	t.Helper()
	re, err := regexp.Compile(filter)
	require.NoError(t, err, "relevance filter must compile: %s", filter)
	return re.MatchString(path)
}

// TestE2ERelevanceFiltersRunSuitesForReusablePrioritize is the acceptance
// test for criterion 1 of AISDLC-659: a PR that changes only
// reusable-prioritize.yml must run the behaviour and playback suites. Each
// suite's changes step greps the PR's file list with these filters and skips
// the suite on no match, so the path must match both.
func TestE2ERelevanceFiltersRunSuitesForReusablePrioritize(t *testing.T) {
	for i, filter := range e2eRelevanceFilters(t) {
		assert.True(t, matchesFilter(t, filter, reusablePrioritizePath),
			"a PR changing only %s must run the suites: relevance filter %d does not match it",
			reusablePrioritizePath, i)
	}
}

// e2eTriggerConfig decodes just the push trigger of e2e.yml.
type e2eTriggerConfig struct {
	On struct {
		Push struct {
			Paths []string `yaml:"paths"`
		} `yaml:"push"`
	} `yaml:"on"`
}

// TestE2EPushPathsIncludeReusablePrioritize is the acceptance test for the
// push leg of criterion 1 of AISDLC-659: a push to main that changes only
// reusable-prioritize.yml must start the e2e workflow at all — the relevance
// greps only run once the workflow has started — so on.push.paths, which the
// workflow keeps in sync with the filters, must list it.
func TestE2EPushPathsIncludeReusablePrioritize(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "e2e.yml"))
	require.NoError(t, err)
	var cfg e2eTriggerConfig
	require.NoError(t, yaml.Unmarshal(content, &cfg))
	assert.Contains(t, cfg.On.Push.Paths, reusablePrioritizePath,
		"on.push.paths must include %s so a main push changing only it runs the suites",
		reusablePrioritizePath)
}

// TestLiveTestRelevanceScriptListsReusablePrioritize is the acceptance test
// for criterion 2 of AISDLC-659: scripts/check-live-test-relevance-test.sh
// (run by `make script-test`) must keep reusable-prioritize.yml in the
// single-file change list it checks against the filters it extracts from
// e2e.yml, and its check for that path must pass.
func TestLiveTestRelevanceScriptListsReusablePrioritize(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "check-live-test-relevance-test.sh"))
	require.NoError(t, err)
	script := string(content)

	// The positive list is the `for path in \` … `; do` block of the loop
	// that expects relevance for each listed single-file change.
	start := strings.Index(script, "for path in \\")
	require.NotEqual(t, -1, start, "the regression script must keep its positive path list")
	relEnd := strings.Index(script[start:], "; do")
	require.NotEqual(t, -1, relEnd, "the positive path list must be terminated by '; do'")
	assert.Contains(t, script[start:start+relEnd], reusablePrioritizePath,
		"the regression script's relevance list must include %s", reusablePrioritizePath)

	for i, filter := range e2eRelevanceFilters(t) {
		assert.True(t, matchesFilter(t, filter, reusablePrioritizePath),
			"the regression script's check for %s must pass: relevance filter %d does not match it",
			reusablePrioritizePath, i)
	}
}
