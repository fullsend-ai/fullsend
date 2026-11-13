package retryselect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
)

const labelsFeature = `Feature: Labels

  Scenario: adds a label
    Given a repo
    Then the label exists

  Scenario: removes a label
    Given a repo
    Then the label is gone

  Rule: dispatch

    Scenario: dispatches the agent
      Given a repo
      Then the agent runs

  Scenario Outline: <actor> labels the issue
    Given a repo
    Then <actor> can label

    Examples:
      | actor  |
      | writer |
      | reader |
`

const mintFeature = `Feature: Mint

  Scenario: mints a token
    Given a mint
`

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"features/triage/labels.feature": {Data: []byte(labelsFeature)},
		"features/platform/mint.feature": {Data: []byte(mintFeature)},
	}
}

// writeReport writes report as JSON and returns its path.
func writeReport(t *testing.T, report behaviourfail.Report) string {
	t.Helper()
	data, err := json.Marshal(report)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "failure-class.json.attempt-1")
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func infraReport(failures ...behaviourfail.FailureRecord) behaviourfail.Report {
	return behaviourfail.Report{
		Class:                  behaviourfail.Infrastructure,
		InfrastructureFailures: len(failures),
		Failures:               failures,
	}
}

func scenarioFailure(feature, name string) behaviourfail.FailureRecord {
	return behaviourfail.FailureRecord{
		Source:   "scenario",
		Feature:  feature,
		Scenario: name,
		Class:    behaviourfail.Infrastructure,
		Message:  "rate limited",
	}
}

var suitePaths = []string{"features"}

func TestSelectNotARetryRunsSuitePaths(t *testing.T) {
	sel, err := Select(testFS(), suitePaths, "")
	require.NoError(t, err)
	assert.Equal(t, Selection{Paths: suitePaths}, sel)
}

func TestSelectTargetsFailedScenarios(t *testing.T) {
	report := writeReport(t, infraReport(
		scenarioFailure("features/triage/labels.feature", "removes a label"),
		scenarioFailure("features/triage/labels.feature", "dispatches the agent"),
		scenarioFailure("features/platform/mint.feature", "mints a token"),
		// A teardown failure is recorded beside the scenario's own
		// failure under the same name; it must not duplicate the path.
		scenarioFailure("features/platform/mint.feature", "mints a token"),
	))

	sel, err := Select(testFS(), suitePaths, report)
	require.NoError(t, err)
	assert.True(t, sel.Targeted)
	assert.Empty(t, sel.Reason)
	assert.Equal(t, []string{
		"features/platform/mint.feature:3",
		"features/triage/labels.feature:7",
		"features/triage/labels.feature:13",
	}, sel.Paths)
}

func TestSelectOutlineExampleSelectsTheOutline(t *testing.T) {
	report := writeReport(t, infraReport(
		scenarioFailure("features/triage/labels.feature", "reader labels the issue"),
	))

	sel, err := Select(testFS(), suitePaths, report)
	require.NoError(t, err)
	assert.Equal(t, []string{"features/triage/labels.feature:17"}, sel.Paths)
}

func TestSelectUnknownNameRerunsTheFile(t *testing.T) {
	report := writeReport(t, infraReport(
		scenarioFailure("features/triage/labels.feature", "adds a label"),
		scenarioFailure("features/triage/labels.feature", "adds a [REDACTED]"),
	))

	sel, err := Select(testFS(), suitePaths, report)
	require.NoError(t, err)
	assert.True(t, sel.Targeted)
	assert.Equal(t, []string{"features/triage/labels.feature"}, sel.Paths)
}

func TestSelectFallsBackToFullSuite(t *testing.T) {
	tests := map[string]behaviourfail.Report{
		"no failures": infraReport(),
		"bootstrap failure": infraReport(
			scenarioFailure("features/platform/mint.feature", "mints a token"),
			behaviourfail.FailureRecord{Source: "bootstrap", Class: behaviourfail.Infrastructure, Message: "mint download"},
		),
		"scenario without a feature file": infraReport(
			scenarioFailure("", "mints a token"),
		),
	}
	for name, report := range tests {
		t.Run(name, func(t *testing.T) {
			sel, err := Select(testFS(), suitePaths, writeReport(t, report))
			require.NoError(t, err)
			assert.False(t, sel.Targeted)
			assert.NotEmpty(t, sel.Reason)
			assert.Equal(t, suitePaths, sel.Paths)
		})
	}
}

func TestSelectRejectsReports(t *testing.T) {
	scenarioClass := infraReport(scenarioFailure("features/platform/mint.feature", "mints a token"))
	scenarioClass.Class = behaviourfail.Scenario

	tests := map[string]string{
		"missing file":     filepath.Join(t.TempDir(), "absent.json"),
		"malformed json":   writeRaw(t, "{not json"),
		"scenario class":   writeReport(t, scenarioClass),
		"outside paths":    writeReport(t, infraReport(scenarioFailure("other/x.feature", "x"))),
		"escaping path":    writeReport(t, infraReport(scenarioFailure("features/../secret.feature", "x"))),
		"missing feature":  writeReport(t, infraReport(scenarioFailure("features/gone.feature", "x"))),
		"unparseable file": writeReport(t, infraReport(scenarioFailure("features/bad.feature", "x"))),
	}
	fsys := testFS()
	fsys["features/bad.feature"] = &fstest.MapFile{Data: []byte("Scenario without a feature\n  Given")}
	for name, report := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Select(fsys, suitePaths, report)
			assert.Error(t, err)
		})
	}
}

func writeRaw(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestUnderAny(t *testing.T) {
	assert.True(t, underAny("features/a.feature", []string{"features"}))
	assert.True(t, underAny("features/a.feature", []string{"features/"}))
	assert.True(t, underAny("features/a.feature", []string{"features/a.feature"}))
	assert.True(t, underAny("features/a.feature", []string{"."}))
	assert.False(t, underAny("featuresx/a.feature", []string{"features"}))
	assert.False(t, underAny("/etc/a.feature", []string{"features"}))
	assert.False(t, underAny("features/../a.feature", []string{"features"}))
}
