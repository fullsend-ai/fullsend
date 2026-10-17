package suite

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const routedFeature = `Feature: routed
  Scenario: first
    Given a routed probe step

  Scenario: second
    Given a routed probe step

  @skip:gitlab
  Scenario: skipped on gitlab
    Given a routed probe step

  Scenario Outline: outline <n>
    Given a routed probe step

    Examples:
      | n |
      | 1 |
      | 2 |
`

// runRouted runs routedFeature with InitRoutedScenario plus a probe step
// that leases a repo from the routed World's Driver and records the
// World it ran with.
func runRouted(t *testing.T, base *world.World, route TemplateRouter) (status int, seen []*world.World) {
	t.Helper()
	var mu sync.Mutex
	suiteRunner := godog.TestSuite{
		Name: "routed",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			InitRoutedScenario(sc, base, route)
			sc.Step(`^a routed probe step$`, func(ctx context.Context) error {
				w := world.FromContext(ctx)
				if w == nil {
					return errors.New("no world in context")
				}
				name, err := w.Driver.AllocateRepo(ctx)
				if err != nil {
					return err
				}
				w.LeasedRepoName = name
				mu.Lock()
				seen = append(seen, w)
				mu.Unlock()
				return nil
			})
		},
		Options: &godog.Options{
			Format:          "progress",
			Output:          io.Discard,
			Concurrency:     4,
			FeatureContents: []godog.Feature{{Name: "routed.feature", Contents: []byte(routedFeature)}},
		},
	}
	return suiteRunner.Run(), seen
}

func TestInitRoutedScenario_RoutesEachScenarioToItsTemplate(t *testing.T) {
	cfg := env.RunnerConfig{SCM: "gitlab", InstallMode: "per-repo"}
	base := &world.World{Config: cfg}
	drivers := map[string]*fakeDriver{"org-a": newFakeDriver(4), "org-b": newFakeDriver(4)}
	templates := map[string]*world.World{}
	for org, d := range drivers {
		templates[org] = &world.World{Config: cfg, Org: org, RepoOwner: org, Token: "token-" + org, Driver: d}
	}

	var mu sync.Mutex
	var routed []string
	next := 0
	route := func(sc *godog.Scenario) (*world.World, error) {
		mu.Lock()
		defer mu.Unlock()
		routed = append(routed, sc.Name)
		org := []string{"org-a", "org-b"}[next%2]
		next++
		return templates[org], nil
	}

	status, seen := runRouted(t, base, route)
	require.Equal(t, 0, status)

	// The @skip:gitlab scenario is skipped before routing.
	assert.ElementsMatch(t, []string{"first", "second", "outline 1", "outline 2"}, routed)
	require.Len(t, seen, 4)
	for _, w := range seen {
		require.NotNil(t, w.Driver)
		d := drivers[w.Org]
		assert.Same(t, d, w.Driver, "scenario %q must use its org's driver", w.ScenarioName)
		assert.Equal(t, "token-"+w.Org, w.Token)
		assert.Equal(t, w.Org, w.RepoOwner)
		assert.NotSame(t, templates[w.Org], w, "each scenario gets its own clone")
	}
	for org, d := range drivers {
		assert.Equal(t, 2, d.allocated, "%s allocations", org)
		assert.Equal(t, d.allocated, d.deallocated, "%s: the After hook must deallocate through the routed driver", org)
		assert.Empty(t, d.outstanding, org)
	}
}

func TestInitRoutedScenario_MalformedTagIsNotRouted(t *testing.T) {
	const feature = `Feature: malformed
  @requires:capability:
  Scenario: bad tag
    Given a routed probe step
`
	base := &world.World{Config: env.RunnerConfig{SCM: "github", InstallMode: "per-repo"}}
	var routed []string
	var stepRan bool
	suiteRunner := godog.TestSuite{
		Name: "malformed",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			InitRoutedScenario(sc, base, func(s *godog.Scenario) (*world.World, error) {
				routed = append(routed, s.Name)
				return nil, errors.New("must not be routed")
			})
			sc.Step(`^a routed probe step$`, func() error {
				stepRan = true
				return nil
			})
		},
		Options: &godog.Options{
			Format:          "progress",
			Output:          io.Discard,
			FeatureContents: []godog.Feature{{Name: "malformed.feature", Contents: []byte(feature)}},
		},
	}
	assert.NotEqual(t, 0, suiteRunner.Run(), "a malformed tag fails the suite")
	assert.Empty(t, routed, "a malformed-tag scenario must not consume org capacity")
	assert.False(t, stepRan)
}

func TestInitRoutedScenario_RoutingErrorFailsScenario(t *testing.T) {
	base := &world.World{Config: env.RunnerConfig{SCM: "github", InstallMode: "per-repo"}}
	route := func(sc *godog.Scenario) (*world.World, error) {
		return nil, errors.New("no org capacity left")
	}
	status, seen := runRouted(t, base, route)
	assert.NotEqual(t, 0, status, "a routing error must fail the suite")
	assert.Empty(t, seen, "no step runs without a routed World")
}

func TestInitGatedRoutedScenario_GateRunsBeforeTagsAndRouting(t *testing.T) {
	const feature = `Feature: gated
  Scenario: mine
    Given a gated probe step

  Scenario: theirs
    Given a gated probe step

  @requires:capability:
  Scenario: theirs with a bad tag
    Given a gated probe step

  Scenario: unknown
    Given a gated probe step
`
	base := &world.World{Config: env.RunnerConfig{SCM: "github", InstallMode: "per-repo"}}
	template := &world.World{Config: base.Config, Org: "org-a", Driver: newFakeDriver(4)}
	var mu sync.Mutex
	var routed, ran []string
	gate := func(sc *godog.Scenario) error {
		switch sc.Name {
		case "mine":
			return nil
		case "unknown":
			return errors.New("not in the shard plan")
		default:
			return godog.ErrSkip
		}
	}
	suiteRunner := godog.TestSuite{
		Name: "gated",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			InitGatedRoutedScenario(sc, base, gate, func(s *godog.Scenario) (*world.World, error) {
				mu.Lock()
				routed = append(routed, s.Name)
				mu.Unlock()
				return template, nil
			})
			sc.Step(`^a gated probe step$`, func(ctx context.Context) error {
				mu.Lock()
				ran = append(ran, world.FromContext(ctx).ScenarioName)
				mu.Unlock()
				return nil
			})
		},
		Options: &godog.Options{
			Format:          "progress",
			Output:          io.Discard,
			Concurrency:     2,
			FeatureContents: []godog.Feature{{Name: "gated.feature", Contents: []byte(feature)}},
		},
	}
	assert.NotEqual(t, 0, suiteRunner.Run(), "a gate error fails the suite")
	assert.Equal(t, []string{"mine"}, routed, "skipped and failed scenarios are never routed")
	assert.Equal(t, []string{"mine"}, ran)
}
