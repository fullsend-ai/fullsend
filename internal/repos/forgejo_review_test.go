package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitForgejoSelectors_BareGlobWithUnrelatedGitHubEntry(t *testing.T) {
	m := parseTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: gh/app
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: fj/app
`)
	sel, err := m.SplitForgejoSelectors([]string{"fj/*"})
	require.NoError(t, err)
	assert.Equal(t, []string{"forgejo:home/fj/app"}, selectors(sel.Targets))
	assert.False(t, sel.RunOthers)
	assert.Empty(t, sel.Rest)

	// A glob that also selects the github declaration stays ambiguous.
	_, err = m.SplitForgejoSelectors([]string{"*/app"})
	assert.ErrorContains(t, err, "matches a github/gitlab declaration")
}

func TestSplitForgejoSelectors_BareGlobCoveredByGitHubGlobEntry(t *testing.T) {
	m := parseTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: fj/*
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: fj/app
`)
	_, err := m.SplitForgejoSelectors([]string{"fj/app"})
	assert.ErrorContains(t, err, "matches a github/gitlab declaration")
}

func TestForgejoLifecycle_ConcurrentReplacementIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	const path = "org/app/.fullsend/config.yaml"
	targets := []ForgejoTarget{{Alias: "home", BaseURL: "https://git.example.com/forgejo", Owner: "org", Repo: "app"}}
	env := map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"}

	setup := func(t *testing.T) (*ForgejoLifecycle, *memForgejo) {
		home := &memForgejo{files: map[string]string{}}
		lc, _ := newTestLifecycle(t, vendoredTwoInstanceManifest, map[string]*memForgejo{"https://git.example.com/forgejo": home}, env)
		res, err := lc.Install(ctx, targets)
		require.NoError(t, err)
		require.Equal(t, ForgejoStateInstalled, res[0].State)
		return lc, home
	}
	replace := func(files map[string]string, key string) { files[key] = "hand: written\n" }

	t.Run("update", func(t *testing.T) {
		lc, home := setup(t)
		home.files[path] += "extra: true\n"
		writes := home.writes
		home.afterGet = replace
		res, err := lc.Install(ctx, targets)
		require.Error(t, err)
		assert.Equal(t, ForgejoStateError, res[0].State)
		assert.Equal(t, "hand: written\n", home.files[path])
		assert.Equal(t, writes, home.writes)
	})

	t.Run("delete", func(t *testing.T) {
		lc, home := setup(t)
		writes := home.writes
		home.afterGet = replace
		res, err := lc.Uninstall(ctx, targets)
		require.Error(t, err)
		assert.Equal(t, ForgejoStateError, res[0].State)
		assert.Equal(t, "hand: written\n", home.files[path])
		assert.Equal(t, writes, home.writes)
	})
}
