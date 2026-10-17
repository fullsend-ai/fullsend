package behaviourtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const demandFeature = `Feature: demand
  Background:
    Given the enrolled test repository

  Scenario: provisions through background
    Then something happens

  @skip:gitlab
  Scenario: skipped on gitlab
    Then something happens

  @smoke
  Scenario Outline: outline row <n>
    Then row <n> happens

    Examples:
      | n |
      | 1 |
      | 2 |
      | 3 |
`

const noProvisionFeature = `Feature: no provision
  Scenario: needs the org but no repo
    Given the suite's mint address

  @requires:capability:future
  Scenario: gated
    Given the enrolled test repository

  Rule: grouped
    Scenario: provisions inside a rule
      Given the enrolled test repository
`

func writeFeatures(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return dir
}

func TestPickleProvisions(t *testing.T) {
	assert.True(t, pickleProvisions([]*messages.PickleStep{{Text: "an issue"}, {Text: "the enrolled test repository"}}))
	assert.False(t, pickleProvisions([]*messages.PickleStep{{Text: "the enrolled test repository with extras"}}))
	assert.False(t, pickleProvisions([]*messages.PickleStep{nil, {Text: "an enrolled repository \"o/r\""}}))
	assert.False(t, pickleProvisions(nil))
}

func TestLoadScenarioInstances_ExpandsOutlinesBackgroundsAndRules(t *testing.T) {
	dir := writeFeatures(t, map[string]string{
		"a/demand.feature":       demandFeature,
		"b/no-provision.feature": noProvisionFeature,
		"b/notes.txt":            "not a feature",
	})

	instances, err := loadScenarioInstances([]string{dir}, "")
	require.NoError(t, err)
	require.Len(t, instances, 8)

	byName := map[string]scenarioInstance{}
	for _, inst := range instances {
		byName[inst.Name] = inst
	}
	assert.True(t, byName["provisions through background"].Provisions)
	assert.True(t, byName["outline row 2"].Provisions, "outline rows inherit the background step")
	assert.True(t, byName["provisions inside a rule"].Provisions)
	assert.False(t, byName["needs the org but no repo"].Provisions)
	assert.Equal(t, []string{"@skip:gitlab"}, byName["skipped on gitlab"].Tags)
	assert.Equal(t, filepath.Join(dir, "a/demand.feature"), byName["outline row 1"].URI)
}

func TestLoadScenarioInstances_TagFilterLineSelectionAndDuplicates(t *testing.T) {
	dir := writeFeatures(t, map[string]string{"demand.feature": demandFeature})
	file := filepath.Join(dir, "demand.feature")

	smoke, err := loadScenarioInstances([]string{dir}, "@smoke")
	require.NoError(t, err)
	assert.Len(t, smoke, 3, "the tag filter keeps only the outline rows")

	// Line 13 declares the Scenario Outline: all of its rows are selected.
	outline, err := loadScenarioInstances([]string{file + ":13"}, "")
	require.NoError(t, err)
	assert.Len(t, outline, 3)

	dup, err := loadScenarioInstances([]string{dir, file}, "")
	require.NoError(t, err)
	assert.Len(t, dup, 5, "a file reached twice is read once")
}

func TestLoadScenarioInstances_Errors(t *testing.T) {
	_, err := loadScenarioInstances([]string{filepath.Join(t.TempDir(), "missing")}, "")
	require.Error(t, err)

	dir := writeFeatures(t, map[string]string{"bad.feature": "Scenario: no feature line\n"})
	_, err = loadScenarioInstances([]string{dir}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad.feature")
}

func TestSplitFeaturePathLine(t *testing.T) {
	p, l := splitFeaturePathLine("features/a.feature:12")
	assert.Equal(t, "features/a.feature", p)
	assert.Equal(t, 12, l)
	p, l = splitFeaturePathLine("features")
	assert.Equal(t, "features", p)
	assert.Equal(t, -1, l)
	p, l = splitFeaturePathLine("features/a.feature:99999999999999999999")
	assert.Equal(t, "features/a.feature:99999999999999999999", p)
	assert.Equal(t, -1, l)
}

func TestMatchTagFilter(t *testing.T) {
	tags := []string{"@smoke", "@triage"}
	for _, tc := range []struct {
		filter string
		want   bool
	}{
		{"", true},
		{"@smoke", true},
		{"smoke", true},
		{"@code", false},
		{"~@code", true},
		{"~@smoke", false},
		{"@smoke && @triage", true},
		{"@smoke && ~@triage", false},
		{"@code,@triage", true},
		{"@code,", false},
		{"@smoke && ", false},
	} {
		assert.Equal(t, tc.want, matchTagFilter(tc.filter, tags), "filter %q", tc.filter)
	}
}

func TestSummarizeDemand(t *testing.T) {
	instances := []scenarioInstance{
		{Name: "prov", Provisions: true},
		{Name: "plain"},
		{Name: "skipped", Tags: []string{"@skip"}, Provisions: true},
		{Name: "malformed", Tags: []string{"@bad"}, Provisions: true},
	}
	d := summarizeDemand(instances, func(tags []string) error {
		for _, tag := range tags {
			switch tag {
			case "@skip":
				return godog.ErrSkip
			case "@bad":
				return errors.New("malformed tag")
			}
		}
		return nil
	})
	assert.Equal(t, suiteDemand{Selected: 4, Skipped: 1, Eligible: 3, Provisioning: 1}, d,
		"a malformed-tag scenario runs (and fails) before it can provision")
}

// TestLoadScenarioInstances_RepositoryFeatures sizes this repository's
// own suite the way RunSuite does under the default DEV configuration.
func TestLoadScenarioInstances_RepositoryFeatures(t *testing.T) {
	instances, err := loadScenarioInstances([]string{"../../e2e/behaviour/features"}, "")
	require.NoError(t, err)
	base := &world.World{Config: env.RunnerConfig{SCM: "github", CI: "githubactions", InstallMode: "per-repo", Environment: "dev"}}
	d := summarizeDemand(instances, func(tags []string) error { return suite.SkipErrorForTagNames(tags, base) })
	t.Logf("repository suite: %+v (orgs needed: %d)", d, orgsNeeded(d.Provisioning, maxProvisioningPerOrg))
	assert.Positive(t, d.Provisioning)
	assert.LessOrEqual(t, d.Provisioning, d.Eligible)
	assert.Equal(t, d.Selected, d.Eligible+d.Skipped)
}

// orgsNeeded is the distinct-org count full-budget orgs need for n
// provisioning instances: ceil(n/perOrg).
func orgsNeeded(n, perOrg int) int {
	return (n + perOrg - 1) / perOrg
}
