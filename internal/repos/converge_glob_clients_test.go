package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Converge expands glob entries through GlobClients when it is set, so a
// repository that the converge clients list but GlobClients does not (one
// created after the install preflights listed the org) is not converged
// (#9571). Without GlobClients the converge clients expand the globs.
func TestConverge_GlobClientsExpandGlobs(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web")
	preflightListing := forge.NewFakeClient()
	preflightListing.Repos = []forge.Repository{{FullName: "acme/api", Name: "api", DefaultBranch: "main"}}

	cases := []struct {
		name        string
		globClients ForgeClientFactory
		want        []string
	}{
		{name: "GlobClients set", globClients: newTestClientFactory(preflightListing), want: []string{"acme/api"}},
		{name: "GlobClients unset", want: []string{"acme/api", "acme/web"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := convergeCfgWithDefaults(newConvergeManifest("acme/*"))
			cfg.DryRun = true
			cfg.GlobClients = tc.globClients

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
			require.NoError(t, err)
			var got []string
			for _, r := range result.Results {
				got = append(got, r.Owner+"/"+r.Repo)
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}
