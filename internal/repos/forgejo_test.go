package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const twoInstanceManifest = `version: 1
forgejo:
  instances:
    - alias: home
      url: https://git.example.com/forgejo
      repos:
        - name: org/app
        - name: org/lib
    - alias: code-berg
      url: https://codeberg.org
      repos:
        - name: org/app
        - name: team/tool
`

// vendoredTwoInstanceManifest vendors the workflow bundle into every
// repository, so lifecycle tests focused on the managed config need no
// workflow host.
var vendoredTwoInstanceManifest = strings.Replace(twoInstanceManifest, "version: 1\n", "version: 1\ndefaults:\n  vendor: true\n", 1)

func parseTestManifest(t *testing.T, data string) *Manifest {
	t.Helper()
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(data), &m))
	require.NoError(t, m.Validate())
	return &m
}

func selectors(ts []ForgejoTarget) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Selector())
	}
	return out
}

func TestForgejoManifest_ParsesInstances(t *testing.T) {
	m := parseTestManifest(t, twoInstanceManifest)
	require.True(t, m.HasForgejo())
	require.Len(t, m.Forgejo.Instances, 2)
	assert.Equal(t, "https://git.example.com/forgejo", m.Forgejo.Instances[0].URL)

	out, err := m.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(out), "alias: code-berg")
}

func TestForgejoManifest_Validation(t *testing.T) {
	tests := []struct {
		name, instances, want string
	}{
		{"bad alias", "- alias: -x\n      url: https://a.example\n      repos: []", "alias"},
		{"duplicate alias", "- alias: a\n      url: https://a.example\n      repos: []\n    - alias: A\n      url: https://b.example\n      repos: []", "duplicates alias"},
		{"env collision", "- alias: a.b\n      url: https://a.example\n      repos: []\n    - alias: a_b\n      url: https://b.example\n      repos: []", `share the credential variable "FULLSEND_FORGEJO_A_B_TOKEN"`},
		{"http remote", "- alias: a\n      url: http://a.example\n      repos: []", "must use https"},
		{"no scheme", "- alias: a\n      url: a.example\n      repos: []", "absolute https URL"},
		{"ftp", "- alias: a\n      url: ftp://a.example\n      repos: []", "absolute https URL"},
		{"credentials in url", "- alias: a\n      url: https://u:p@a.example\n      repos: []", "credentials"},
		{"query", "- alias: a\n      url: https://a.example?x=1\n      repos: []", "query"},
		{"bad repo path", "- alias: a\n      url: https://a.example\n      repos:\n        - name: just-repo", "owner/repo path"},
		{"nested path", "- alias: a\n      url: https://a.example\n      repos:\n        - name: a/b/c", "owner/repo path"},
		{"dot path", "- alias: a\n      url: https://a.example\n      repos:\n        - name: ../b", "owner/repo path"},
		{"duplicate repo", "- alias: a\n      url: https://a.example\n      repos:\n        - name: o/r\n        - name: O/R", "declared more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Manifest
			require.NoError(t, parseManifestBytes([]byte("version: 1\nforgejo:\n  instances:\n    "+tt.instances+"\n"), &m))
			assert.ErrorContains(t, m.Validate(), tt.want)
		})
	}
}

func TestForgejoManifest_LoopbackHTTPAccepted(t *testing.T) {
	parseTestManifest(t, "version: 1\nforgejo:\n  instances:\n    - alias: dev\n      url: http://127.0.0.1:3000\n      repos: []\n    - alias: dev2\n      url: http://localhost:3000/\n      repos: []\n")
}

func TestForgejoManifest_RejectsTokenField(t *testing.T) {
	var m Manifest
	err := parseManifestBytes([]byte("version: 1\nforgejo:\n  instances:\n    - alias: a\n      url: https://a.example\n      token: x\n      repos: []\n"), &m)
	assert.ErrorContains(t, err, "token")
}

func TestForgejoTokenEnvVar(t *testing.T) {
	assert.Equal(t, "FULLSEND_FORGEJO_HOME_TOKEN", ForgejoTokenEnvVar("home"))
	assert.Equal(t, "FULLSEND_FORGEJO_CODE_BERG_TOKEN", ForgejoTokenEnvVar("code-berg"))
	assert.Equal(t, "FULLSEND_FORGEJO_MY_GIT_2_TOKEN", ForgejoTokenEnvVar("my..git.2"))
}

func TestSplitForgejoSelectors(t *testing.T) {
	m := parseTestManifest(t, twoInstanceManifest)

	t.Run("no selectors selects all", func(t *testing.T) {
		sel, err := m.SplitForgejoSelectors(nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"forgejo:home/org/app", "forgejo:home/org/lib", "forgejo:code-berg/org/app", "forgejo:code-berg/team/tool"}, selectors(sel.Targets))
		assert.False(t, sel.RunOthers)
	})
	t.Run("unique bare path", func(t *testing.T) {
		sel, err := m.SplitForgejoSelectors([]string{"team/tool", "ORG/LIB"})
		require.NoError(t, err)
		assert.Equal(t, []string{"forgejo:code-berg/team/tool", "forgejo:home/org/lib"}, selectors(sel.Targets))
		assert.Equal(t, "https://codeberg.org", sel.Targets[0].BaseURL)
	})
	t.Run("ambiguous bare path lists choices", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"org/app"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
		assert.Contains(t, err.Error(), "forgejo:code-berg/org/app, forgejo:home/org/app")
	})
	t.Run("ambiguous glob", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"org/*"})
		assert.ErrorContains(t, err, "ambiguous")
	})
	t.Run("unambiguous glob", func(t *testing.T) {
		sel, err := m.SplitForgejoSelectors([]string{"team/*"})
		require.NoError(t, err)
		assert.Equal(t, []string{"forgejo:code-berg/team/tool"}, selectors(sel.Targets))
	})
	t.Run("qualified selectors and dedup", func(t *testing.T) {
		sel, err := m.SplitForgejoSelectors([]string{"forgejo:home/org/app", "forgejo:HOME/org/*", "org/lib"})
		require.NoError(t, err)
		assert.Equal(t, []string{"forgejo:home/org/app", "forgejo:home/org/lib"}, selectors(sel.Targets))
	})
	t.Run("unknown alias", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"forgejo:other/org/app"})
		assert.ErrorContains(t, err, `unknown Forgejo instance "other"; declared aliases: home, code-berg`)
	})
	t.Run("undeclared qualified target", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"forgejo:home/team/tool"})
		assert.ErrorContains(t, err, `not declared in repos.yaml: "forgejo:home/team/tool"`)
	})
	t.Run("malformed qualified selector", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"forgejo:home/app"})
		assert.ErrorContains(t, err, "must have the form")
	})
	t.Run("undeclared bare selector without other forges", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"nope/nope"})
		assert.ErrorContains(t, err, "not declared in repos.yaml: nope/nope")
	})
	t.Run("bad glob", func(t *testing.T) {
		_, err := m.SplitForgejoSelectors([]string{"org/["})
		assert.ErrorContains(t, err, "invalid glob")
		_, err = m.SplitForgejoSelectors([]string{"forgejo:home/org/["})
		assert.ErrorContains(t, err, "invalid glob")
	})
}

func TestSplitForgejoSelectors_MixedManifest(t *testing.T) {
	m := parseTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: gh/app
    - name: home/lib
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: home/lib
        - name: fj/app
`)
	sel, err := m.SplitForgejoSelectors(nil)
	require.NoError(t, err)
	assert.True(t, sel.RunOthers)
	assert.Len(t, sel.Targets, 2)

	sel, err = m.SplitForgejoSelectors([]string{"gh/app", "fj/app"})
	require.NoError(t, err)
	assert.Equal(t, []string{"forgejo:home/fj/app"}, selectors(sel.Targets))
	assert.Equal(t, []string{"gh/app"}, sel.Rest)
	assert.True(t, sel.RunOthers)

	sel, err = m.SplitForgejoSelectors([]string{"forgejo:home/fj/app"})
	require.NoError(t, err)
	assert.False(t, sel.RunOthers)

	_, err = m.SplitForgejoSelectors([]string{"home/lib"})
	assert.ErrorContains(t, err, "matches a github/gitlab declaration and forgejo:home/home/lib")
}

func TestSplitForgejoSelectors_IntersectingGlobs(t *testing.T) {
	m := parseTestManifest(t, `version: 1
github:
  repos:
    - name: org/b*
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: org/az
`)
	// org/*z matches org/az on Forgejo and, through the declared org/b*
	// glob, can match org/bz on GitHub, though neither pattern matches the
	// other's text.
	_, err := m.SplitForgejoSelectors([]string{"org/*z"})
	assert.ErrorContains(t, err, "ambiguous")

	// A glob whose fixed prefix cannot meet the declared glob's does not
	// overlap.
	sel, err := m.SplitForgejoSelectors([]string{"org/a*"})
	require.NoError(t, err)
	assert.Equal(t, []string{"forgejo:home/org/az"}, selectors(sel.Targets))
	assert.False(t, sel.RunOthers)
}

func TestSplitForgejoSelectors_DisjointCharacterClasses(t *testing.T) {
	m := parseTestManifest(t, `version: 1
github:
  repos:
    - name: org/[cd]
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: org/a
`)
	// org/[ab] shares its literal prefix and suffix with the declared
	// org/[cd] but can never select a repository that glob covers.
	sel, err := m.SplitForgejoSelectors([]string{"org/[ab]"})
	require.NoError(t, err)
	assert.Equal(t, []string{"forgejo:home/org/a"}, selectors(sel.Targets))
	assert.False(t, sel.RunOthers)
}

func TestSplitForgejoSelectors_SelectorMatchingGlobEntryText(t *testing.T) {
	m := parseTestManifest(t, `version: 1
github:
  repos:
    - name: org/[a-c]
forgejo:
  instances:
    - alias: home
      url: https://git.example.com
      repos:
        - name: org/a-b
`)
	// org/*-* matches the literal text of the declared org/[a-c] glob, but
	// that glob selects only org/a, org/b and org/c, so there is no overlap.
	sel, err := m.SplitForgejoSelectors([]string{"org/*-*"})
	require.NoError(t, err)
	assert.Equal(t, []string{"forgejo:home/org/a-b"}, selectors(sel.Targets))
	assert.False(t, sel.RunOthers)
}

func TestGlobsMayIntersect_RepeatedClassRanges(t *testing.T) {
	class := "[" + strings.Repeat("a-c", 20000) + "]"
	start := time.Now()
	assert.True(t, globsMayIntersect("org/"+class, "org/b"))
	assert.False(t, globsMayIntersect("org/"+class, "org/d"))
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestGlobsMayIntersect(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"org/*z", "org/b*", true},
		{"org/a*", "org/b*", false},
		{"org/*z", "org/*y", false},
		{"org/*", "org/b*", true},
		{"org/[ab]x", "org/b*", true},
		{"other/*", "org/*", false},
		{`org/a\*`, "org/b*", false},
		{`org/*\z`, "org/*y", false},
		{`org/*\z`, "org/*z", true},
		// Identical literal prefix and suffix, but disjoint classes.
		{"org/[ab]", "org/[cd]", false},
		{"org/[ab]*", "org/[cd]*", false},
		{"org/[a-c]*", "org/[b-d]*", true},
		{"org/[^a]*", "org/a*", false},
		{"org/[^a]*", "org/[ab]*", true},
		// `?` fixes a length and `*` does not cross a separator.
		{"org/a?", "org/a??", false},
		{"org/a?", "org/a?", true},
		{"org/a*", "org/a??", true},
		{"org/*", "org/a/b", false},
		{"org/*x*", "org/y*z", true},
		// A malformed pattern is treated as possibly intersecting.
		{"org/[", "org/b*", true},
		{`org/a\`, "org/b*", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, globsMayIntersect(tt.a, tt.b), "%s vs %s", tt.a, tt.b)
		assert.Equal(t, tt.want, globsMayIntersect(tt.b, tt.a), "%s vs %s", tt.b, tt.a)
	}
}

func TestSplitForgejoSelectors_NoForgejoSection(t *testing.T) {
	m := &Manifest{Version: 1}
	sel, err := m.SplitForgejoSelectors([]string{"a/b"})
	require.NoError(t, err)
	assert.Empty(t, sel.Targets)
	assert.Equal(t, []string{"a/b"}, sel.Rest)
	assert.True(t, sel.RunOthers)
	assert.False(t, m.HasForgejo())
	sel, err = m.SplitForgejoSelectors(nil)
	require.NoError(t, err)
	assert.True(t, sel.RunOthers)
	_, err = m.SplitForgejoSelectors([]string{"forgejo:x/a/b"})
	assert.ErrorContains(t, err, "declared aliases: (none)")
}

func TestRemoveForgejoRepos(t *testing.T) {
	m := parseTestManifest(t, twoInstanceManifest)
	removed := m.RemoveForgejoRepos([]ForgejoTarget{{Alias: "home", Owner: "org", Repo: "app"}})
	assert.Equal(t, []string{"forgejo:home/org/app"}, removed)
	assert.Equal(t, []ForgejoRepoEntry{{Name: "org/lib"}}, m.Forgejo.Instances[0].Repos)
	assert.Len(t, m.Forgejo.Instances[1].Repos, 2, "the same path on another instance is kept")
	assert.Nil(t, (&Manifest{}).RemoveForgejoRepos(nil))
}

func TestResolveForgejoCredentials(t *testing.T) {
	targets := []ForgejoTarget{{Alias: "home"}, {Alias: "home"}, {Alias: "code-berg"}}
	env := map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "h", "FULLSEND_FORGEJO_CODE_BERG_TOKEN": " c\n"}
	var looked []string
	lookup := func(k string) (string, bool) {
		looked = append(looked, k)
		v, ok := env[k]
		return v, ok
	}
	ctx := context.Background()
	tokens, err := ForgejoCredentials{Lookup: lookup}.Resolve(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"home": "h", "code-berg": "c"}, tokens)
	assert.Equal(t, []string{"FULLSEND_FORGEJO_HOME_TOKEN", "FULLSEND_FORGEJO_CODE_BERG_TOKEN"}, looked, "each selected alias is looked up once")

	delete(env, "FULLSEND_FORGEJO_CODE_BERG_TOKEN")
	env["FULLSEND_FORGEJO_HOME_TOKEN"] = "h"
	_, err = ForgejoCredentials{Lookup: lookup}.Resolve(ctx, targets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code-berg (set FULLSEND_FORGEJO_CODE_BERG_TOKEN)")
	assert.NotContains(t, err.Error(), "home (")
	assert.Contains(t, err.Error(), "oauth.client_id", "an instance without a client ID is told how to enable browser login")
	assert.Contains(t, err.Error(), "full Forgejo user permissions")
}

// fakeForgejoOAuth records which instances the OAuth fallback was asked
// about.
type fakeForgejoOAuth struct {
	cached   map[string]string
	cacheErr error
	loginTok map[string]string
	loginErr error
	checked  []string
	logins   []ForgejoOAuthRequest
}

func (f *fakeForgejoOAuth) CachedToken(_ context.Context, req ForgejoOAuthRequest) (string, string, error) {
	f.checked = append(f.checked, req.Alias)
	if f.cacheErr != nil {
		return "", "", f.cacheErr
	}
	if tok := f.cached[req.Alias]; tok != "" {
		return tok, "", nil
	}
	return "", "no cached OAuth token", nil
}

func (f *fakeForgejoOAuth) Login(_ context.Context, req ForgejoOAuthRequest) (string, error) {
	f.logins = append(f.logins, req)
	if f.loginErr != nil {
		return "", f.loginErr
	}
	return f.loginTok[req.Alias], nil
}

func TestForgejoCredentials_OAuthFallback(t *testing.T) {
	ctx := context.Background()
	home := ForgejoTarget{Alias: "home", BaseURL: "https://git.example.com/forgejo", ClientID: "home-client", Owner: "org", Repo: "app"}
	cb := ForgejoTarget{Alias: "code-berg", BaseURL: "https://codeberg.org", ClientID: "cb-client", Owner: "org", Repo: "app"}
	noOAuth := ForgejoTarget{Alias: "plain", BaseURL: "https://plain.example", Owner: "org", Repo: "app"}
	envOf := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	}

	t.Run("environment token wins over cache and login", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{cached: map[string]string{"home": "cached"}}
		tokens, err := ForgejoCredentials{Lookup: envOf(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "env"}), OAuth: oauth, Interactive: true}.Resolve(ctx, []ForgejoTarget{home})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"home": "env"}, tokens)
		assert.Empty(t, oauth.checked, "the keyring is not consulted")
		assert.Empty(t, oauth.logins)
	})
	t.Run("cached token is used without a prompt, even non-interactively", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{cached: map[string]string{"home": "cached"}}
		tokens, err := ForgejoCredentials{Lookup: envOf(nil), OAuth: oauth}.Resolve(ctx, []ForgejoTarget{home, home})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"home": "cached"}, tokens)
		assert.Equal(t, []string{"home"}, oauth.checked)
		assert.Empty(t, oauth.logins)
	})
	t.Run("only the selected instance lacking a token logs in", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{loginTok: map[string]string{"code-berg": "fresh"}}
		tokens, err := ForgejoCredentials{Lookup: envOf(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "env"}), OAuth: oauth, Interactive: true}.Resolve(ctx, []ForgejoTarget{home, cb})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"home": "env", "code-berg": "fresh"}, tokens)
		require.Len(t, oauth.logins, 1)
		assert.Equal(t, ForgejoOAuthRequest{Alias: "code-berg", BaseURL: "https://codeberg.org", ClientID: "cb-client", TokenEnvVar: "FULLSEND_FORGEJO_CODE_BERG_TOKEN"}, oauth.logins[0])
	})
	t.Run("non-interactive without a token fails naming the variable", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{}
		_, err := ForgejoCredentials{Lookup: envOf(nil), OAuth: oauth}.Resolve(ctx, []ForgejoTarget{home})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "home (no cached OAuth token; set FULLSEND_FORGEJO_HOME_TOKEN, or rerun in a terminal to log in with OAuth)")
		assert.Contains(t, err.Error(), "nothing was changed")
		assert.Empty(t, oauth.logins, "no browser login without a terminal")
	})
	t.Run("an instance that cannot log in fails the selection before any login", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{loginTok: map[string]string{"home": "fresh"}}
		_, err := ForgejoCredentials{Lookup: envOf(nil), OAuth: oauth, Interactive: true}.Resolve(ctx, []ForgejoTarget{home, noOAuth})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "plain (set FULLSEND_FORGEJO_PLAIN_TOKEN)")
		assert.Empty(t, oauth.logins, "no prompt when the command fails anyway")
		assert.NotContains(t, oauth.checked, "plain")
	})
	t.Run("cache check failure stops resolution", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{cacheErr: errors.New("connection refused")}
		_, err := ForgejoCredentials{Lookup: envOf(nil), OAuth: oauth, Interactive: true}.Resolve(ctx, []ForgejoTarget{home})
		require.ErrorContains(t, err, `forgejo instance "home": checking the cached OAuth token: connection refused`)
		assert.Empty(t, oauth.logins)
	})
	t.Run("login failure names the token alternative", func(t *testing.T) {
		oauth := &fakeForgejoOAuth{loginErr: errors.New("authorization was not granted (access_denied)")}
		_, err := ForgejoCredentials{Lookup: envOf(nil), OAuth: oauth, Interactive: true}.Resolve(ctx, []ForgejoTarget{home})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OAuth login failed: authorization was not granted (access_denied)")
		assert.Contains(t, err.Error(), "Set FULLSEND_FORGEJO_HOME_TOKEN")
	})
	t.Run("without an OAuth fallback a client ID is not enough", func(t *testing.T) {
		_, err := ForgejoCredentials{Lookup: envOf(nil), Interactive: true}.Resolve(ctx, []ForgejoTarget{home})
		require.ErrorContains(t, err, "home (set FULLSEND_FORGEJO_HOME_TOKEN)")
		assert.NotContains(t, err.Error(), "oauth.client_id", "the instance already configures a client ID")
	})
}

func TestForgejoManifest_OAuthClientID(t *testing.T) {
	m := parseTestManifest(t, "version: 1\nforgejo:\n  instances:\n    - alias: home\n      url: https://git.example.com\n      oauth:\n        client_id: 7d2b0c1e-0000-4000-8000-000000000001\n      repos:\n        - name: org/app\n    - alias: plain\n      url: https://plain.example\n      repos:\n        - name: org/app\n")
	sel, err := m.SplitForgejoSelectors(nil)
	require.NoError(t, err)
	require.Len(t, sel.Targets, 2)
	assert.Equal(t, "7d2b0c1e-0000-4000-8000-000000000001", sel.Targets[0].ClientID)
	assert.Empty(t, sel.Targets[1].ClientID)

	data, err := yaml.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(data), "client_id: 7d2b0c1e-0000-4000-8000-000000000001")
	assert.NotContains(t, string(data), "token", "no credential field is written to the manifest")

	for name, client := range map[string]string{"empty": `""`, "spaces": `"a b"`, "slash": "a/b"} {
		t.Run(name, func(t *testing.T) {
			var bad Manifest
			require.NoError(t, parseManifestBytes([]byte("version: 1\nforgejo:\n  instances:\n    - alias: a\n      url: https://a.example\n      oauth:\n        client_id: "+client+"\n      repos: []\n"), &bad))
			assert.ErrorContains(t, bad.Validate(), "forgejo.instances[0].oauth.client_id")
		})
	}
	var secret Manifest
	err = parseManifestBytes([]byte("version: 1\nforgejo:\n  instances:\n    - alias: a\n      url: https://a.example\n      oauth:\n        client_id: x\n        client_secret: s\n      repos: []\n"), &secret)
	assert.ErrorContains(t, err, "client_secret", "a client secret is never accepted in the manifest")
}

// memForgejo is an in-memory ForgejoFileClient.
type memForgejo struct {
	files  map[string]string
	writes int
	failOn string
	// commits holds the files at each commit ChangeFiles or CreateTag
	// recorded; tags maps owner/repo@tag to a commit.
	commits map[string]map[string]string
	tags    map[string]string
	// afterGet runs after a successful GetFile, to simulate a concurrent
	// change between the ownership-checked read and the write.
	afterGet func(files map[string]string, key string)
}

// memSHA is the revision identifier memForgejo reports for content.
func memSHA(content string) string { return "sha:" + content }

func (c *memForgejo) GetFile(_ context.Context, owner, repo, path string) (*ForgejoFile, error) {
	if c.failOn == "get" {
		return nil, errors.New("boom")
	}
	v, ok := c.files[owner+"/"+repo+"/"+path]
	if !ok {
		return nil, fmt.Errorf("get: %w", forge.ErrNotFound)
	}
	if c.afterGet != nil {
		defer c.afterGet(c.files, owner+"/"+repo+"/"+path)
	}
	return &ForgejoFile{Content: []byte(v), SHA: memSHA(v)}, nil
}

func (c *memForgejo) CreateFile(_ context.Context, owner, repo, path, _ string, content []byte) error {
	if c.failOn == "write" {
		return errors.New("boom")
	}
	key := owner + "/" + repo + "/" + path
	if _, exists := c.files[key]; exists {
		return errors.New("create: file already exists")
	}
	c.writes++
	c.files[key] = string(content)
	return nil
}

func (c *memForgejo) UpdateFile(_ context.Context, owner, repo, path, _ string, content []byte, sha string) error {
	if c.failOn == "write" {
		return errors.New("boom")
	}
	key := owner + "/" + repo + "/" + path
	if cur, exists := c.files[key]; !exists || memSHA(cur) != sha {
		return errors.New("update: sha conflict")
	}
	c.writes++
	c.files[key] = string(content)
	return nil
}

func (c *memForgejo) DeleteFile(_ context.Context, owner, repo, path, _, sha string) error {
	if c.failOn == "delete" {
		return errors.New("boom")
	}
	key := owner + "/" + repo + "/" + path
	if cur, exists := c.files[key]; !exists || memSHA(cur) != sha {
		return errors.New("delete: sha conflict")
	}
	c.writes++
	delete(c.files, key)
	return nil
}

// snapshot records the current files as a commit and returns its SHA.
func (c *memForgejo) snapshot() string {
	if c.commits == nil {
		c.commits = map[string]map[string]string{}
	}
	sha := fmt.Sprintf("%040x", len(c.commits)+1)
	snap := make(map[string]string, len(c.files))
	for k, v := range c.files {
		snap[k] = v
	}
	c.commits[sha] = snap
	return sha
}

func (c *memForgejo) GetFileAt(_ context.Context, owner, repo, path, ref string) (*ForgejoFile, error) {
	if c.failOn == "getat" {
		return nil, errors.New("boom")
	}
	snap, ok := c.commits[ref]
	if !ok {
		return nil, fmt.Errorf("ref: %w", forge.ErrNotFound)
	}
	v, ok := snap[owner+"/"+repo+"/"+path]
	if !ok {
		return nil, fmt.Errorf("get at: %w", forge.ErrNotFound)
	}
	return &ForgejoFile{Content: []byte(v), SHA: memSHA(v)}, nil
}

func (c *memForgejo) ChangeFiles(_ context.Context, owner, repo, _ string, changes []ForgejoFileChange) (string, error) {
	if c.failOn == "write" || c.failOn == "change" {
		return "", errors.New("boom")
	}
	for _, ch := range changes {
		cur, exists := c.files[owner+"/"+repo+"/"+ch.Path]
		switch ch.Operation {
		case ForgejoFileCreate:
			if exists {
				return "", errors.New("change: file already exists")
			}
		default:
			if !exists || memSHA(cur) != ch.SHA {
				return "", errors.New("change: sha conflict")
			}
		}
	}
	for _, ch := range changes {
		key := owner + "/" + repo + "/" + ch.Path
		if ch.Operation == ForgejoFileDelete {
			delete(c.files, key)
		} else {
			c.files[key] = string(ch.Content)
		}
	}
	c.writes++
	return c.snapshot(), nil
}

func (c *memForgejo) GetTag(_ context.Context, owner, repo, tag string) (string, error) {
	if c.failOn == "tag" {
		return "", errors.New("boom")
	}
	sha, ok := c.tags[owner+"/"+repo+"@"+tag]
	if !ok {
		return "", fmt.Errorf("tag: %w", forge.ErrNotFound)
	}
	return sha, nil
}

func (c *memForgejo) CreateTag(_ context.Context, owner, repo, tag, target, _ string) (string, error) {
	if c.failOn == "createtag" {
		return "", errors.New("boom")
	}
	if c.tags == nil {
		c.tags = map[string]string{}
	}
	key := owner + "/" + repo + "@" + tag
	if _, exists := c.tags[key]; exists {
		return "", errors.New("tag exists")
	}
	if target == "" {
		target = c.snapshot()
	}
	c.tags[key] = target
	c.writes++
	return target, nil
}

func newTestLifecycle(t *testing.T, manifest string, clients map[string]*memForgejo, env map[string]string) (*ForgejoLifecycle, *[]string) {
	t.Helper()
	var contacted []string
	return &ForgejoLifecycle{
		Manifest: parseTestManifest(t, manifest),
		NewClient: func(baseURL, token string) (ForgejoFileClient, error) {
			contacted = append(contacted, baseURL+"|"+token)
			c, ok := clients[baseURL]
			if !ok {
				return nil, errors.New("unexpected instance")
			}
			return c, nil
		},
		Lookup: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	}, &contacted
}

func TestForgejoLifecycle_InstallStatusUninstall(t *testing.T) {
	ctx := context.Background()
	home := &memForgejo{files: map[string]string{}}
	cb := &memForgejo{files: map[string]string{}}
	clients := map[string]*memForgejo{"https://git.example.com/forgejo": home, "https://codeberg.org": cb}
	env := map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "home-tok", "FULLSEND_FORGEJO_CODE_BERG_TOKEN": "cb-tok"}
	lc, contacted := newTestLifecycle(t, vendoredTwoInstanceManifest, clients, env)

	sel, err := lc.Manifest.SplitForgejoSelectors([]string{"forgejo:home/org/app"})
	require.NoError(t, err)

	st, err := lc.Status(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateNotInstalled, st[0].State)
	assert.Zero(t, home.writes, "status is read-only")
	assert.Equal(t, []string{"https://git.example.com/forgejo|home-tok"}, *contacted, "only the selected instance is contacted, with its own token")

	res, err := lc.Install(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	installed := home.files["org/app/.fullsend/config.yaml"]
	assert.True(t, hasManagedConfigMarker([]byte(installed)))
	assert.Empty(t, cb.files)

	res, err = lc.Install(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpToDate, res[0].State)
	assert.Equal(t, 2, home.writes, "second install makes no change")
	assert.Contains(t, home.files, "org/app/"+forgejoDispatchPath, "the vendored repository carries the bundle")
	assert.Contains(t, home.files["org/app/"+forgejoShimPath], "uses: ./"+forgejoDispatchPath)

	home.files["org/app/.fullsend/config.yaml"] = installed + "extra: true\n"
	st, err = lc.Status(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateDrift, st[0].State)
	assert.Contains(t, st[0].Detail, "run repos install")

	res, err = lc.Install(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpgraded, res[0].State)
	assert.Equal(t, installed, home.files["org/app/.fullsend/config.yaml"])

	res, err = lc.Uninstall(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateRemoved, res[0].State)
	assert.Empty(t, home.files)

	res, err = lc.Uninstall(ctx, sel.Targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateNotInstalled, res[0].State)
}

func TestForgejoLifecycle_UnmanagedFileIsNeverOverwrittenOrDeleted(t *testing.T) {
	ctx := context.Background()
	home := &memForgejo{files: map[string]string{"org/lib/.fullsend/config.yaml": "hand: written\n"}}
	lc, _ := newTestLifecycle(t, vendoredTwoInstanceManifest, map[string]*memForgejo{"https://git.example.com/forgejo": home},
		map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	targets := []ForgejoTarget{{Alias: "home", BaseURL: "https://git.example.com/forgejo", Owner: "org", Repo: "lib"}}

	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUnmanaged, st[0].State)

	res, err := lc.Install(ctx, targets)
	require.Error(t, err)
	assert.Equal(t, ForgejoStateError, res[0].State)
	assert.Contains(t, err.Error(), "forgejo:home/org/lib")
	assert.Contains(t, err.Error(), "ownership marker")

	res, err = lc.Uninstall(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUnmanaged, res[0].State)
	assert.Equal(t, "hand: written\n", home.files["org/lib/.fullsend/config.yaml"])
	assert.Zero(t, home.writes)
}

func TestForgejoLifecycle_MissingTokenFailsBeforeAnyForgeCall(t *testing.T) {
	ctx := context.Background()
	lc, contacted := newTestLifecycle(t, vendoredTwoInstanceManifest, nil, map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	sel, err := lc.Manifest.SplitForgejoSelectors(nil)
	require.NoError(t, err)
	for name, run := range map[string]func(context.Context, []ForgejoTarget) ([]ForgejoRepoResult, error){
		"install": lc.Install, "status": lc.Status, "uninstall": lc.Uninstall,
	} {
		_, err := run(ctx, sel.Targets)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "FULLSEND_FORGEJO_CODE_BERG_TOKEN", name)
	}
	assert.Empty(t, *contacted, "no client is built when a selected instance lacks a token")
}

func TestForgejoLifecycle_DryRunAndErrors(t *testing.T) {
	ctx := context.Background()
	home := &memForgejo{files: map[string]string{}}
	lc, _ := newTestLifecycle(t, vendoredTwoInstanceManifest, map[string]*memForgejo{"https://git.example.com/forgejo": home},
		map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	targets := []ForgejoTarget{{Alias: "home", BaseURL: "https://git.example.com/forgejo", Owner: "org", Repo: "app"}}

	lc.DryRun = true
	res, err := lc.Install(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	assert.Zero(t, home.writes)

	lc.DryRun = false
	home.failOn = "write"
	_, err = lc.Install(ctx, targets)
	assert.ErrorContains(t, err, "writing .fullsend/config.yaml")

	home.failOn = ""
	_, err = lc.Install(ctx, targets)
	require.NoError(t, err)
	home.failOn = "delete"
	_, err = lc.Uninstall(ctx, targets)
	assert.ErrorContains(t, err, "removing .fullsend/config.yaml")

	lc.DryRun = true
	home.failOn = ""
	res, err = lc.Uninstall(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateRemoved, res[0].State)
	assert.NotEmpty(t, home.files)

	home.failOn = "get"
	for _, run := range []func(context.Context, []ForgejoTarget) ([]ForgejoRepoResult, error){lc.Install, lc.Uninstall} {
		_, err = run(ctx, targets)
		assert.ErrorContains(t, err, "boom")
	}
	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateError, st[0].State)

	lc.NewClient = func(string, string) (ForgejoFileClient, error) { return nil, errors.New("bad url") }
	_, err = lc.Status(ctx, targets)
	assert.ErrorContains(t, err, `forgejo instance "home": bad url`)
}

func TestForgejoStatusDetail(t *testing.T) {
	assert.True(t, strings.HasSuffix(forgejoStatusDetail(ForgejoStateNotInstalled), "run repos install"))
	assert.Empty(t, forgejoStatusDetail(ForgejoStateUpToDate))
}
