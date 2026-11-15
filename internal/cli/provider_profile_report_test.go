package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func embeddedProviderDef(t *testing.T, name string) harness.ProviderDef {
	t.Helper()
	data, err := scaffold.FullsendRepoFile("providers/" + name + ".yaml")
	require.NoError(t, err)
	def, err := harness.ParseProviderDef(data)
	require.NoError(t, err)
	return def
}

// TestPrintProviderProfiles_EmbeddedGolden pins the provider lines for the
// profiles embedded in this binary.
func TestPrintProviderProfiles_EmbeddedGolden(t *testing.T) {
	var defs []harness.ProviderDef
	for _, name := range []string{"github", "github-ro", "package-registries", "vertex-ai", "openai"} {
		defs = append(defs, embeddedProviderDef(t, name))
	}
	reports := resolveProviderProfiles(defs, nil, nil)

	var buf bytes.Buffer
	printProviderProfiles(reports, ui.New(&buf))

	want := "" +
		"    provider github: profile fullsend-github (embedded) -> api.github.com rest read-write, github.com rest read-write; binaries gh, git, node, pre-commit\n" +
		"    provider github-ro: profile fullsend-github-ro (embedded) -> api.github.com rest read-only, api.github.com graphql read-only, github.com rest read-only; binaries gh, node\n" +
		"    provider package-registries: profile fullsend-package-registries (embedded) -> 7 endpoints: registry.npmjs.org, registry.yarnpkg.com, pypi.org, files.pythonhosted.org, proxy.golang.org, sum.golang.org, storage.googleapis.com (read-only); binaries npm, npx, yarn, yarnpkg, pnpm, node, pip, pip3, python, python3, python3.*, uv, uvx, go, pre-commit\n" +
		"    provider vertex-ai: profile fullsend-vertex-ai (embedded) -> api.anthropic.com rest read-write, *.googleapis.com rest read-write; binaries claude, claude.exe, node, opencode, pi\n" +
		"    provider openai: profile fullsend-openai (embedded) -> api.openai.com rest rules POST; binaries node, codex\n"
	assert.Equal(t, want, buf.String())

	write := map[string]bool{}
	inference := map[string]bool{}
	for _, r := range reports {
		write[r.provider] = r.writeCapable
		inference[r.provider] = r.inference
		assert.False(t, r.unknown, r.provider)
	}
	assert.Equal(t, map[string]bool{
		"github":             false,
		"github-ro":          false,
		"package-registries": false,
		"vertex-ai":          true,
		"openai":             true,
	}, inference, "inference profiles are recognised by their category")
	assert.Equal(t, map[string]bool{
		"github":             true,
		"github-ro":          false,
		"package-registries": false,
		"vertex-ai":          true,
		"openai":             true,
	}, write)
}

func TestResolveProviderProfiles_HarnessListed(t *testing.T) {
	dir := t.TempDir()
	listedPath := filepath.Join(dir, "fullsend-github.yaml")
	require.NoError(t, os.WriteFile(listedPath, []byte(`id: fullsend-github
endpoints:
  - host: api.github.com
    protocol: rest
    access: read-only
binaries: ["**/gh"]
`), 0o600))
	customPath := filepath.Join(dir, "myorg-api.yaml")
	require.NoError(t, os.WriteFile(customPath, []byte(`id: myorg-api
endpoints:
  - host: api.example.invalid
    protocol: rest
    rules:
      - allow:
          method: GET
      - allow:
          method: head
`), 0o600))
	listed := []resolve.ResolvedProfile{
		{ID: "fullsend-github", LocalPath: listedPath},
		{ID: "myorg-api", LocalPath: customPath, FromURL: true},
		{ID: "myorg-broken", LocalPath: filepath.Join(dir, "missing.yaml")},
	}
	defs := []harness.ProviderDef{
		{Name: "github", Type: "fullsend-github"},
		{Name: "api", Type: "myorg-api"},
		{Name: "broken", Type: "myorg-broken"},
		{Name: "elsewhere", Type: "myorg-on-gateway"},
		{Name: "skipped", Type: "fullsend-openai"},
		{Name: "jira", Type: "FULLSEND-JIRA"},
	}
	reports := resolveProviderProfiles(defs, listed, map[string]struct{}{"skipped": {}})
	require.Len(t, reports, 5)

	assert.Equal(t, "provider github: profile fullsend-github (harness) -> api.github.com rest read-only; binaries gh", reports[0].line)
	assert.False(t, reports[0].writeCapable, "the listed copy, not the embedded one, decides")

	assert.Equal(t, "provider api: profile myorg-api (harness) -> api.example.invalid rest rules GET/HEAD; no binaries listed", reports[1].line)
	assert.False(t, reports[1].writeCapable)

	assert.Equal(t, profileSourceHarness, reports[2].source)
	assert.Contains(t, reports[2].line, "provider broken: profile myorg-broken (harness) could not be read:")
	assert.False(t, reports[2].writeCapable)
	assert.True(t, reports[2].unknown, "an unreadable profile is unknown, not read-only")

	assert.Equal(t, "", reports[3].source)
	assert.Equal(t, "provider elsewhere: profile myorg-on-gateway (not imported by this run; what it permits is not known to the runner)", reports[3].line)
	assert.True(t, reports[3].unknown, "a profile the run did not import is unknown")

	assert.Equal(t, "fullsend-jira", reports[4].profile, "the type is reported in its canonical form")
	assert.Equal(t, profileSourceEmbedded, reports[4].source)
}

func TestProviderProfileEndpoint_WriteCapable(t *testing.T) {
	allow := func(m string) providerProfileRule {
		r := providerProfileRule{}
		r.Allow = &struct {
			Method string `yaml:"method"`
		}{Method: m}
		return r
	}
	tests := []struct {
		name     string
		endpoint providerProfileEndpoint
		want     bool
		access   string
	}{
		{"read-only", providerProfileEndpoint{Access: "read-only"}, false, "read-only"},
		{"read-write", providerProfileEndpoint{Access: "read-write"}, true, "read-write"},
		{"full", providerProfileEndpoint{Access: "full"}, true, "full"},
		{"no preset, no rules", providerProfileEndpoint{}, true, "unspecified"},
		{"GET rule", providerProfileEndpoint{Rules: []providerProfileRule{allow("GET")}}, false, "rules GET"},
		{"POST rule", providerProfileEndpoint{Rules: []providerProfileRule{allow("GET"), allow("post")}}, true, "rules GET/POST"},
		{"any-method rule", providerProfileEndpoint{Rules: []providerProfileRule{allow("")}}, true, "rules *"},
		{"deny-only rule", providerProfileEndpoint{Rules: []providerProfileRule{{}}}, true, "unspecified"},
		{"read-only preset with a deny-only rule", providerProfileEndpoint{Access: "read-only", Rules: []providerProfileRule{{}}}, false, "read-only"},
		{"read-only preset with a POST rule", providerProfileEndpoint{Access: "read-only", Rules: []providerProfileRule{allow("POST")}}, true, "read-only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.endpoint.writeCapable())
			assert.Equal(t, tt.access, tt.endpoint.access())
		})
	}
}

func TestDescribeProfileEndpointsAndBinaries_Empty(t *testing.T) {
	assert.Equal(t, "no endpoints", describeProfileEndpoints(nil))
	assert.Equal(t, "no binaries listed", describeProfileBinaries(nil))
}

// TestDescribeProfileBinaries_GlobsShortenedAbsolutePathsKept: a "**/"
// glob prints as its base name, an absolute path prints as is, so the two
// stay distinguishable.
func TestDescribeProfileBinaries_GlobsShortenedAbsolutePathsKept(t *testing.T) {
	assert.Equal(t, "binaries gate-query, /usr/local/bin/gate-query",
		describeProfileBinaries([]string{"**/gate-query", "/usr/local/bin/gate-query", "**/gate-query"}))
}

func TestPrintableLine_ReplacesControlCharacters(t *testing.T) {
	assert.Equal(t, "api.example.invalid?::error::x?[31m", printableLine("api.example.invalid\n::error::x\x1b[31m"))
	assert.Equal(t, "provider github: profile fullsend-github", printableLine("provider github: profile fullsend-github"))

	var buf bytes.Buffer
	printProviderProfiles([]providerProfileReport{{line: "provider x: profile y (harness) -> a\nb rest read-only"}}, ui.New(&buf))
	assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "a value with a newline stays on one output line")
}

// TestDurableCredentialWriteWarnings covers the four combinations of a
// durable credential host file and a write-capable provider.
func TestDurableCredentialWriteWarnings(t *testing.T) {
	writer := providerProfileReport{provider: "github", profile: "fullsend-github", writeCapable: true}
	reader := providerProfileReport{provider: "github-ro", profile: "fullsend-github-ro"}
	tests := []struct {
		name    string
		dests   []string
		reports []providerProfileReport
		warn    bool
	}{
		{"credential file and write provider", []string{"/tmp/sa.json"}, []providerProfileReport{reader, writer}, true},
		{"credential file and read-only provider", []string{"/tmp/sa.json"}, []providerProfileReport{reader}, false},
		{"no credential file and write provider", nil, []providerProfileReport{writer}, false},
		{"no credential file and read-only provider", nil, []providerProfileReport{reader}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durableCredentialWriteWarnings(tt.dests, tt.reports)
			if !tt.warn {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Contains(t, got[0], "/tmp/sa.json")
			assert.Contains(t, got[0], "provider github (profile fullsend-github) permits write methods")
			assert.NotContains(t, got[0], "github-ro (profile")
			assert.Contains(t, got[0], "access: read-only")
			assert.Contains(t, got[0], "A profile is a declaration")
		})
	}
}

// TestDurableCredentialWriteWarnings_InferenceAndUnknown: an inference
// profile does not trigger the warning; a profile the run could not read
// does, and is named as unchecked rather than as a writer.
func TestDurableCredentialWriteWarnings_InferenceAndUnknown(t *testing.T) {
	dests := []string{"/tmp/sa.json"}
	vertex := providerProfileReport{provider: "vertex-ai", profile: "fullsend-vertex-ai", writeCapable: true, inference: true}
	assert.Empty(t, durableCredentialWriteWarnings(dests, []providerProfileReport{vertex}), "inference alone does not warn")

	unknown := providerProfileReport{provider: "api", profile: "myorg-api", unknown: true}
	got := durableCredentialWriteWarnings(dests, []providerProfileReport{vertex, unknown})
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "provider api (profile myorg-api) could not be checked")
	assert.NotContains(t, got[0], "vertex-ai")
	assert.NotContains(t, got[0], "permits write methods")
	assert.Contains(t, got[0], "Prefer a short-lived credential that does not need allow_durable or listing the unchecked profile in openshell.profiles", "an unchecked-only warning points at listing the profile")
	assert.NotContains(t, got[0], "github-ro instead of github", "no read-only advice without a writer")

	github := providerProfileReport{provider: "github", profile: "fullsend-github", writeCapable: true}
	jira := providerProfileReport{provider: "atl", profile: "fullsend-atlassian-cloud", writeCapable: true}
	got = durableCredentialWriteWarnings(dests, []providerProfileReport{github, jira, unknown})
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "providers github (profile fullsend-github), atl (profile fullsend-atlassian-cloud) permit write methods")
	assert.Contains(t, got[0], "; provider api (profile myorg-api) could not be checked")
	assert.Contains(t, got[0], "allow_durable, a provider whose profile is access: read-only (for example github-ro instead of github), or listing the unchecked profile")

	other := providerProfileReport{provider: "api2", profile: "myorg-api2", unknown: true}
	got = durableCredentialWriteWarnings(dests, []providerProfileReport{unknown, other})
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "providers api (profile myorg-api), api2 (profile myorg-api2) could not be checked")
	assert.True(t, strings.HasSuffix(got[0], "this run does not check that."), got[0])

	got = durableCredentialWriteWarnings([]string{"/tmp/a.json", "/tmp/b.json"}, []providerProfileReport{github})
	require.Len(t, got, 2, "one warning per durable destination")
}

func TestWarnDurableCredentialWriteProviders(t *testing.T) {
	sa := writeHostFileFixture(t, "service_account")
	wif := writeHostFileFixture(t, "external_account")
	h := &harness.Harness{HostFiles: []harness.HostFile{
		{Src: sa, Dest: "/sandbox/sa.json", AllowDurable: true},
		{Src: wif, Dest: "/sandbox/wif.json"},
		{Src: filepath.Join(t.TempDir(), "absent"), Dest: "/sandbox/absent", Optional: true},
	}}
	assert.Equal(t, []string{"/sandbox/sa.json"}, durableHostFileDests(h))

	writer := []providerProfileReport{{provider: "github", profile: "fullsend-github", writeCapable: true}}

	var buf bytes.Buffer
	warnDurableCredentialWriteProviders(h, writer, ui.New(&buf))
	out := buf.String()
	assert.Contains(t, out, "host file dest /sandbox/sa.json is a durable credential")
	assert.NotContains(t, out, "/sandbox/wif.json")

	buf.Reset()
	warnDurableCredentialWriteProviders(h, nil, ui.New(&buf))
	assert.Empty(t, buf.String(), "no providers, no warning")

	buf.Reset()
	warnDurableCredentialWriteProviders(&harness.Harness{}, writer, ui.New(&buf))
	assert.Empty(t, buf.String(), "no credential host file, no warning")
}
