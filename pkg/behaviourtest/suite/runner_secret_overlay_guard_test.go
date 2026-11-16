package suite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Acceptance tests for AISDLC-654: declare the
// runner-secret-overlay-guard behaviour capability so the gated refused
// scenario in e2e/behaviour/features/dispatch/runner-secret-overlay-guard.feature
// runs in the default behaviour suite (make behaviour-test) instead of
// being skipped, and the Makefile's capability comment stops naming the
// issue as pending work.
//
// The tests read the Makefile and the feature file as text and drive the
// same tag gate (SkipErrorForTagNames) the suite's Before hook uses, so
// they compile on a base with none of the story's changes and are red
// until the capability is declared.

const (
	runnerSecretOverlayGuardCapability = "runner-secret-overlay-guard"
	runnerSecretOverlayGuardFeatureRel = "e2e/behaviour/features/dispatch/runner-secret-overlay-guard.feature"
)

// repoRoot is the repository root as seen from this package's test
// working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	info, err := os.Stat(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	require.False(t, info.IsDir())
	return root
}

// makefileDefaultCapabilities parses the BEHAVIOUR_CAPABILITIES default
// from the Makefile: the list `make behaviour-test` runs the default
// behaviour suite with.
func makefileDefaultCapabilities(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^BEHAVIOUR_CAPABILITIES\s*\?=\s*(\S+)\s*$`)
	m := re.FindStringSubmatch(string(data))
	require.NotNil(t, m, "the Makefile must define BEHAVIOUR_CAPABILITIES ?= <list>")
	var caps []string
	for _, part := range strings.Split(m[1], ",") {
		if p := strings.TrimSpace(part); p != "" {
			caps = append(caps, p)
		}
	}
	require.NotEmpty(t, caps, "the default capability list must not be empty")
	return caps
}

// makefileCapabilityComment returns the contiguous # comment block
// directly above the BEHAVIOUR_CAPABILITIES assignment: the comment a
// maintainer reads to learn what each declared capability is and what
// work is still pending.
func makefileCapabilityComment(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	require.NoError(t, err)
	assignRe := regexp.MustCompile(`^BEHAVIOUR_CAPABILITIES\s*\?=`)
	lines := strings.Split(string(data), "\n")
	assign := -1
	for i, ln := range lines {
		if assignRe.MatchString(ln) {
			assign = i
			break
		}
	}
	require.NotEqual(t, -1, assign, "the Makefile must define BEHAVIOUR_CAPABILITIES")
	var block []string
	for i := assign - 1; i >= 0 && strings.HasPrefix(lines[i], "#"); i-- {
		block = append([]string{lines[i]}, block...)
	}
	require.NotEmpty(t, block, "the capability list needs its comment block")
	return strings.Join(block, "\n")
}

// featureScenario is one scenario of a parsed Gherkin feature file:
// its own tags, its step lines and its raw body lines (docstrings and
// tables included).
type featureScenario struct {
	name  string
	tags  []string
	steps []string
	body  []string
}

// parseFeature extracts each scenario's tags, step lines and body from a
// Gherkin feature file. Feature-level tags are returned separately:
// godog applies them to every scenario in the file, so the tag gate sees
// them alongside the scenario's own tags.
func parseFeature(t *testing.T, path string) (featureTags []string, scenarios []featureScenario) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var pending []string
	current := -1
	for _, ln := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "@"):
			pending = append(pending, strings.Fields(trimmed)...)
		case strings.HasPrefix(trimmed, "Feature:"):
			featureTags = append([]string{}, pending...)
			pending = nil
		case strings.HasPrefix(trimmed, "Scenario:"):
			scenarios = append(scenarios, featureScenario{
				name: strings.TrimSpace(strings.TrimPrefix(trimmed, "Scenario:")),
				tags: append([]string{}, pending...),
			})
			pending = nil
			current = len(scenarios) - 1
		default:
			if current < 0 {
				continue
			}
			scenarios[current].body = append(scenarios[current].body, ln)
			switch {
			case strings.HasPrefix(trimmed, "Given "),
				strings.HasPrefix(trimmed, "When "),
				strings.HasPrefix(trimmed, "Then "),
				strings.HasPrefix(trimmed, "And "),
				strings.HasPrefix(trimmed, "But "):
				scenarios[current].steps = append(scenarios[current].steps, trimmed)
			}
		}
	}
	require.NotEmpty(t, scenarios, "the feature file must hold scenarios")
	return featureTags, scenarios
}

// refusedOverlayGuardScenario finds the scenario gated on the
// runner-secret-overlay-guard capability, with the feature-level tags
// it also carries.
func refusedOverlayGuardScenario(t *testing.T) (featureTags []string, sc featureScenario) {
	t.Helper()
	featureTags, scenarios := parseFeature(t, filepath.Join(repoRoot(t), filepath.FromSlash(runnerSecretOverlayGuardFeatureRel)))
	gate := "@requires:capability:" + runnerSecretOverlayGuardCapability
	var found []featureScenario
	for _, s := range scenarios {
		for _, tag := range s.tags {
			if tag == gate {
				found = append(found, s)
				break
			}
		}
	}
	require.Len(t, found, 1, "exactly one scenario must be gated on the %s capability", runnerSecretOverlayGuardCapability)
	return featureTags, found[0]
}

// defaultRunnerConfigFromMakefile loads the runner config the default
// behaviour suite sees: `make behaviour-test` exports the Makefile's
// BEHAVIOUR_CAPABILITIES default over the suite's environment.
func defaultRunnerConfigFromMakefile(t *testing.T) env.RunnerConfig {
	t.Helper()
	t.Setenv("BEHAVIOUR_SCM", "github")
	t.Setenv("BEHAVIOUR_CI", "githubactions")
	t.Setenv("BEHAVIOUR_INSTALL_MODE", "per-repo")
	t.Setenv("ENVIRONMENT", "dev")
	t.Setenv("BEHAVIOUR_CAPABILITIES", strings.Join(makefileDefaultCapabilities(t), ","))
	cfg := env.LoadRunnerConfig()
	require.True(t, cfg.HasCapability(runnerSecretOverlayGuardCapability),
		"the Makefile's default capability list must declare %s", runnerSecretOverlayGuardCapability)
	return cfg
}

// TestRunnerSecretOverlayGuard_DeclaredInMakefileDefaultCapabilities
// locks ac_1: the Makefile's BEHAVIOUR_CAPABILITIES default includes
// runner-secret-overlay-guard, so `make behaviour-test` runs the default
// behaviour suite with the capability offered, and the target threads
// the variable through to the suite's environment.
func TestRunnerSecretOverlayGuard_DeclaredInMakefileDefaultCapabilities(t *testing.T) {
	assert.Contains(t, makefileDefaultCapabilities(t), runnerSecretOverlayGuardCapability,
		"the default behaviour suite must declare the %s capability so the gated refused scenario runs instead of being skipped",
		runnerSecretOverlayGuardCapability)

	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `BEHAVIOUR_CAPABILITIES="$(BEHAVIOUR_CAPABILITIES)"`,
		"the behaviour-test target must pass the default capabilities to the suite")
}

// TestRunnerSecretOverlayGuard_RefusedScenarioRunsInDefaultSuite locks
// ac_1: with the Makefile's default capabilities in the environment the
// suite runs on, the gate no longer skips the refused runner-secret
// overlay guard scenario.
func TestRunnerSecretOverlayGuard_RefusedScenarioRunsInDefaultSuite(t *testing.T) {
	featureTags, sc := refusedOverlayGuardScenario(t)
	cfg := defaultRunnerConfigFromMakefile(t)
	tags := append(append([]string{}, featureTags...), sc.tags...)
	err := SkipErrorForTagNames(tags, &world.World{Config: cfg})
	require.NoError(t, err,
		"on the default GitHub runner the refused scenario's tags must neither skip it nor fail it")
	assert.NotErrorIs(t, err, godog.ErrSkip,
		"the refused scenario must run in the default behaviour suite instead of being skipped")
}

// TestRunnerSecretOverlayGuard_RefusedScenarioFailsTheSuiteWhenTheGuardRegresses
// locks ac_2: with the capability enabled, the refused scenario runs in
// the default suite and asserts the harness run fails with the
// runner-secret guard error in its logs, so an overlay that sets a
// runner secret while reading event content beyond runtime.forge and
// config, and is not refused, fails the suite — the fails-with-log step
// errors when the run concludes successfully instead.
func TestRunnerSecretOverlayGuard_RefusedScenarioFailsTheSuiteWhenTheGuardRegresses(t *testing.T) {
	featureTags, sc := refusedOverlayGuardScenario(t)
	cfg := defaultRunnerConfigFromMakefile(t)
	tags := append(append([]string{}, featureTags...), sc.tags...)
	require.NoError(t, SkipErrorForTagNames(tags, &world.World{Config: cfg}),
		"with the capability enabled the refused scenario must run in the default suite; a guard regression must reach its steps, not be skipped")

	body := strings.Join(sc.body, "\n")
	assert.Contains(t, body, "${BEHAVIOUR_RUNNER_SECRET}",
		"the scenario's overlay must set a runner secret, the regression the guard refuses")
	assert.Contains(t, body, ".event",
		"the scenario's overlay when: must read event content beyond runtime.forge and config")

	failsRe := regexp.MustCompile(`^Then the harness "[^"]+" workflow fails with "(.*)" in its logs$`)
	var guardMsgs []string
	for _, step := range sc.steps {
		if m := failsRe.FindStringSubmatch(step); m != nil {
			guardMsgs = append(guardMsgs, m[1])
		}
	}
	require.NotEmpty(t, guardMsgs,
		"the refused scenario must assert the harness run fails with the guard error in its logs")
	assert.Contains(t, guardMsgs[0], "references runner secret",
		"the asserted failure message must be the runner-secret guard refusal, so an unrefused overlay fails the step")
}

// TestRunnerSecretOverlayGuard_MakefileCommentNoLongerNamesPendingWork
// locks ac_4: once the capability is declared, the Makefile's capability
// comment no longer names AISDLC-654 as pending work.
func TestRunnerSecretOverlayGuard_MakefileCommentNoLongerNamesPendingWork(t *testing.T) {
	assert.NotContains(t, makefileCapabilityComment(t), "AISDLC-654",
		"the capability comment must no longer name this issue as pending work")
}
