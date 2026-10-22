package scaffold

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestScaffoldProviderCredentialsMatchProfiles pins every scaffold provider's
// credential keys to the env_vars its profile declares (#7883). OpenShell
// 0.1.2 rejects a mismatch in both directions at `provider create`:
//
//   - the profile requires a credential the provider does not pass:
//     "no credentials resolved for provider type '<type>'"
//   - the provider passes a credential the profile does not declare:
//     "provider credentials are not declared by profile '<type>': <KEY>"
//
// Comparing the sets both ways also stops GH_TOKEN being added to a
// credential-less GitHub provider such as github-artifacts, whose endpoints
// are reached through pre-signed URLs, not a token of their own.
func TestScaffoldProviderCredentialsMatchProfiles(t *testing.T) {
	type profileDoc struct {
		ID          string `yaml:"id"`
		Credentials []struct {
			EnvVars  []string `yaml:"env_vars"`
			Required bool     `yaml:"required"`
		} `yaml:"credentials"`
	}
	type providerDoc struct {
		Name        string            `yaml:"name"`
		Type        string            `yaml:"type"`
		Credentials map[string]string `yaml:"credentials"`
	}

	readYAML := func(dir string, fn func(name string, data []byte)) {
		entries, err := fs.ReadDir(content, path.Join("fullsend-repo", dir))
		require.NoError(t, err)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			data, err := FullsendRepoFile(path.Join(dir, e.Name()))
			require.NoError(t, err)
			fn(e.Name(), data)
		}
	}

	profileEnv := map[string][]string{}
	profileRequired := map[string]bool{} // "<profile id>/<env var>"
	providerCreds := map[string]map[string]string{}
	readYAML("profiles", func(name string, data []byte) {
		var p profileDoc
		require.NoError(t, yaml.Unmarshal(data, &p), name)
		require.NotEmpty(t, p.ID, name)
		vars := []string{}
		for _, c := range p.Credentials {
			vars = append(vars, c.EnvVars...)
			for _, v := range c.EnvVars {
				profileRequired[p.ID+"/"+v] = c.Required
			}
		}
		sort.Strings(vars)
		profileEnv[p.ID] = vars
	})

	providers := 0
	readYAML("providers", func(name string, data []byte) {
		providers++
		var p providerDoc
		require.NoError(t, yaml.Unmarshal(data, &p), name)
		providerCreds[name] = p.Credentials
		want, ok := profileEnv[p.Type]
		if !assert.True(t, ok, "providers/%s: type %q has no profile in profiles/", name, p.Type) {
			return
		}
		got := []string{}
		for k := range p.Credentials {
			got = append(got, k)
		}
		sort.Strings(got)
		assert.Equal(t, want, got,
			"providers/%s credential keys must equal the env_vars profile %q declares", name, p.Type)
	})
	require.NotZero(t, providers, "no scaffold providers found")

	// Profile ids are gateway-wide, and fullsend-ai/agents ships
	// fullsend-github-ro with a required GH_TOKEN (fullsend-ai/agents#1513).
	// A scaffold copy that drifts from that shape cannot share a gateway with
	// the fleet's. fullsend-github follows the same shape so that the coder
	// role's provider and the read-only one stay interchangeable.
	for _, id := range []string{"fullsend-github-ro", "fullsend-github"} {
		assert.Equal(t, []string{"GH_TOKEN"}, profileEnv[id],
			"profile %q must declare GH_TOKEN", id)
		assert.True(t, profileRequired[id+"/GH_TOKEN"],
			"profile %q must mark GH_TOKEN required", id)
	}
	for _, name := range []string{"github-ro.yaml", "github.yaml"} {
		assert.Equal(t, "${GH_TOKEN}", providerCreds[name]["GH_TOKEN"],
			"providers/%s must expand GH_TOKEN from the runner environment", name)
	}
}
