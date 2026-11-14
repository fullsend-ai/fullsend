package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// renderedJiraProfile is the part of a rendered per-site profile the tests
// inspect.
type renderedJiraProfile struct {
	ID        string `yaml:"id"`
	Endpoints []struct {
		Host   string `yaml:"host"`
		Path   string `yaml:"path"`
		Access string `yaml:"access"`
	} `yaml:"endpoints"`
}

func parseRenderedJiraProfile(t *testing.T, data []byte) renderedJiraProfile {
	t.Helper()
	var p renderedJiraProfile
	require.NoError(t, yaml.Unmarshal(data, &p))
	return p
}

// TestRenderJiraSiteProfile: the embedded profile's gateway endpoint gets
// the site's path and the id a hash of the cloud ID; the site URL endpoint
// and everything else are left as they are.
func TestRenderJiraSiteProfile(t *testing.T) {
	data, id, err := renderJiraSiteProfile(testJiraCloudID)
	require.NoError(t, err)
	assert.Equal(t, jiraSiteProfileID(testJiraCloudID), id)
	assert.Regexp(t, `^fullsend-jira-[0-9a-f]{12}$`, id)
	assert.True(t, jiraSiteProfileIDPattern.MatchString(id), "a rendered id is reserved")

	p := parseRenderedJiraProfile(t, data)
	assert.Equal(t, id, p.ID)
	require.Len(t, p.Endpoints, 2)
	for _, ep := range p.Endpoints {
		assert.Equal(t, "read-only", ep.Access)
		switch ep.Host {
		case jiraAPIHost:
			assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/**", ep.Path)
		case "*.atlassian.net":
			assert.Empty(t, ep.Path, "the site URL endpoint is unchanged")
		default:
			t.Errorf("unexpected endpoint %q", ep.Host)
		}
	}
	assert.Contains(t, string(data), "\nid: "+id+"\n", "the id line the openshell stub and gateway read")
	assert.Contains(t, string(data), "**/curl")

	other := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	_, otherID, err := renderJiraSiteProfile(other)
	require.NoError(t, err)
	assert.NotEqual(t, id, otherID, "two sites never share a profile id")
}

// TestRenderJiraSiteProfile_EmbeddedTemplateUnchanged: the embedded profile
// itself keeps no path, so static-token mode is unchanged.
func TestRenderJiraSiteProfile_EmbeddedTemplateUnchanged(t *testing.T) {
	data, err := scaffold.FullsendRepoFile("profiles/" + jiraProviderType + ".yaml")
	require.NoError(t, err)
	p := parseRenderedJiraProfile(t, data)
	assert.Equal(t, jiraProviderType, p.ID)
	for _, ep := range p.Endpoints {
		assert.Empty(t, ep.Path, "endpoint %s", ep.Host)
	}
}

func TestRenderJiraSiteProfileFrom_RefusesBadTemplates(t *testing.T) {
	const head = "id: fullsend-jira\ncategory: data\nendpoints:\n"
	ep := func(host string) string {
		return "  - host: " + host + "\n    port: 443\n    protocol: rest\n    access: read-only\n"
	}
	for name, tmpl := range map[string]string{
		"no gateway endpoint":      head + ep(`"*.atlassian.net"`),
		"two gateway endpoints":    head + ep(`"api.atlassian.com"`) + ep("api.atlassian.com"),
		"case variant second":      head + ep(`"api.atlassian.com"`) + ep("API.Atlassian.com"),
		"gateway already has path": head + "  - host: api.atlassian.com\n    path: /ex/jira/**\n",
		"wildcard admits gateway":  head + ep(`"api.atlassian.com"`) + ep(`"*.atlassian.com"`),
		"catch-all host":           head + ep(`"api.atlassian.com"`) + ep(`"*"`),
		"trailing wildcard":        head + ep(`"api.atlassian.com"`) + ep(`"api.atlassian.*"`),
		"double-star wildcard":     head + ep(`"api.atlassian.com"`) + ep(`"api.**"`),
		"inner wildcard":           head + ep(`"api.atlassian.com"`) + ep(`"api.atl*ian.com"`),
		"endpoint without host":    head + "  - port: 443\n" + ep(`"api.atlassian.com"`),
		"merge-key endpoint":       "id: fullsend-jira\nwide: &w\n  host: \"*.atlassian.com\"\nendpoints:\n" + ep(`"api.atlassian.com"`) + "  - <<: *w\n",
		"duplicate key":            head + ep(`"api.atlassian.com"`) + "    host: other.example.com\n",
		"second document":          head + ep(`"api.atlassian.com"`) + "---\nid: other\n",
		"wrong id":                 "id: other\nendpoints:\n" + ep(`"api.atlassian.com"`),
		"no id":                    "endpoints:\n" + ep(`"api.atlassian.com"`),
		"no endpoints":             "id: fullsend-jira\n",
		"endpoints not a list":     "id: fullsend-jira\nendpoints: {}\n",
		"endpoint not a mapping":   head + "  - api.atlassian.com\n",
		"not a mapping":            "- id\n",
		"not YAML":                 "id: [\n",
		"empty":                    "",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := renderJiraSiteProfileFrom([]byte(tmpl), testJiraCloudID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be limited to one Jira site")
		})
	}
	// A wildcard that does not match the gateway host is kept.
	data, _, err := renderJiraSiteProfileFrom([]byte(head+ep(`"*.atlassian.net"`)+ep(`"api.atlassian.com"`)), testJiraCloudID)
	require.NoError(t, err)
	assert.Contains(t, string(data), "/ex/jira/"+testJiraCloudID+"/**")
}

func TestRenderJiraSiteProfile_RefusesInvalidCloudIDs(t *testing.T) {
	for name, id := range map[string]string{
		"empty":              "",
		"upper case":         "AAAAAAAA-bbbb-cccc-dddd-eeeeeeeeeeee",
		"path characters":    "11111111-2222-3333-4444-555555555555/../x",
		"glob":               "11111111-2222-3333-4444-55555555555*",
		"newline":            testJiraCloudID + "\n",
		"embedded newline":   "11111111-2222-3333-4444-\n55555555555",
		"yaml quote":         `11111111-2222-3333-4444-55555555555"`,
		"not a UUID":         "not-a-cloud-id",
		"too long":           testJiraCloudID + "0",
		"percent-encoded":    "11111111-2222-3333-4444-5555555555%2f",
		"surrounding spaces": " " + testJiraCloudID + " ",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := renderJiraSiteProfile(id)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lowercase UUID")
			if id != "" {
				assert.NotContains(t, err.Error(), id, "the value is never echoed")
			}
		})
	}
}

func TestEnsureJiraSiteProfile_ImportsRenderedProfile(t *testing.T) {
	prev := importJiraProfileFn
	t.Cleanup(func() { importJiraProfileFn = prev })
	var gotID string
	var gotData []byte
	importJiraProfileFn = func(_ context.Context, id string, data []byte, _ *ui.Printer) error {
		gotID, gotData = id, data
		return nil
	}
	id, err := ensureJiraSiteProfile(context.Background(), testJiraCloudID, ui.New(&syncBuffer{}))
	require.NoError(t, err)
	assert.Equal(t, testJiraSiteProfileID, id)
	assert.Equal(t, testJiraSiteProfileID, gotID)
	assert.Contains(t, string(gotData), "/ex/jira/"+testJiraCloudID+"/**")

	importJiraProfileFn = func(context.Context, string, []byte, *ui.Printer) error { return errors.New("import failed") }
	_, err = ensureJiraSiteProfile(context.Background(), testJiraCloudID, ui.New(&syncBuffer{}))
	require.ErrorContains(t, err, "import failed")

	importJiraProfileFn = func(context.Context, string, []byte, *ui.Printer) error {
		t.Error("an invalid cloud ID is never imported")
		return nil
	}
	_, err = ensureJiraSiteProfile(context.Background(), "BAD", ui.New(&syncBuffer{}))
	require.Error(t, err)
}

func TestRejectJiraSiteProfileIDs(t *testing.T) {
	for _, id := range []string{testJiraSiteProfileID, "fullsend-jira-0123456789ab", "fullsend-jira-0123456789AB", "FULLSEND-JIRA-0123456789ab"} {
		err := rejectJiraSiteProfileIDs([]resolve.ResolvedProfile{{ID: "other"}, {ID: id}})
		require.Error(t, err, id)
		assert.Contains(t, err.Error(), "reserved for the per-site Jira profile")
	}
	for _, id := range []string{"fullsend-jira", "fullsend-jira-ro", "fullsend-jira-0123456789abc", "fullsend-jira-0123456789a", "fullsend-jira-0123456789ag", "x-fullsend-jira-0123456789ab"} {
		assert.NoError(t, rejectJiraSiteProfileIDs([]resolve.ResolvedProfile{{ID: id}}), id)
	}
	assert.NoError(t, rejectJiraSiteProfileIDs(nil))
}

// gatewayURLFor is the JIRA_API_URL resolveJiraRunEnv returns for a site.
func gatewayURLFor(cloudID string) string {
	return jira.DefaultAPIURL + "/ex/jira/" + cloudID
}

func noJiraLookup(t *testing.T) jiraGatewayDiscoverer {
	return func(context.Context, jira.ScopedConfig) (string, error) {
		t.Error("no cloud-ID lookup expected")
		return "", nil
	}
}

// TestResolveJiraMintCloudID_JiraSourcedReusesGatewayURL: a Jira-sourced
// run takes the cloud ID from the JIRA_API_URL already resolved.
func TestResolveJiraMintCloudID_JiraSourcedReusesGatewayURL(t *testing.T) {
	for _, apiURL := range []string{"", jira.DefaultAPIURL, jira.DefaultAPIURL + "/"} {
		id, err := resolveJiraMintCloudID(context.Background(), gatewayURLFor(testJiraCloudID), jiraTracking(testJiraSite, apiURL), true, noJiraLookup(t))
		require.NoError(t, err, apiURL)
		assert.Equal(t, testJiraCloudID, id)
	}
}

// TestResolveJiraMintCloudID_LooksUpFromBaseURL: any other minting run
// looks the cloud ID up from tracker.jira.base_url.
func TestResolveJiraMintCloudID_LooksUpFromBaseURL(t *testing.T) {
	var got jira.ScopedConfig
	calls := 0
	discover := func(ctx context.Context, cfg jira.ScopedConfig) (string, error) {
		calls++
		got = cfg
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "the lookup is bounded")
		return gatewayURLFor(testJiraCloudID), nil
	}
	id, err := resolveJiraMintCloudID(context.Background(), "", jiraTracking(testJiraSite+"/", ""), false, discover)
	require.NoError(t, err)
	assert.Equal(t, testJiraCloudID, id)
	assert.Equal(t, 1, calls)
	assert.Equal(t, jira.ScopedConfig{BaseURL: testJiraSite, APIURL: jira.DefaultAPIURL}, got)
}

func TestResolveJiraMintCloudID_FailsClosed(t *testing.T) {
	lookupErr := func(context.Context, jira.ScopedConfig) (string, error) {
		return "", errors.New("jira: discover cloud ID: unexpected status 404")
	}
	returns := func(u string) jiraGatewayDiscoverer {
		return func(context.Context, jira.ScopedConfig) (string, error) { return u, nil }
	}
	for _, tt := range []struct {
		name     string
		apiURL   string
		tracking *config.TrackingConfig
		offline  bool
		discover jiraGatewayDiscoverer
		want     string
	}{
		{name: "nil tracking", want: "tracker.jira.base_url is not configured"},
		{name: "no jira block", tracking: &config.TrackingConfig{}, want: "tracker.jira.base_url is not configured"},
		{name: "empty base_url", tracking: jiraTracking("", ""), want: "tracker.jira.base_url is not configured"},
		{name: "empty base_url, jira-sourced", apiURL: gatewayURLFor(testJiraCloudID), tracking: jiraTracking("", ""), want: "tracker.jira.base_url is not configured"},
		{name: "credentials in base_url", tracking: jiraTracking("https://user:s3cret@example.atlassian.net", ""), want: "invalid Jira connection settings"},
		{name: "http base_url", tracking: jiraTracking("http://example.atlassian.net", ""), want: "invalid Jira connection settings"},
		{name: "custom api_url", tracking: jiraTracking(testJiraSite, "https://gateway.example.com"), want: "tracker.jira.api_url must be unset"},
		{name: "custom api_url, jira-sourced", apiURL: "https://gateway.example.com/ex/jira/" + testJiraCloudID, tracking: jiraTracking(testJiraSite, "https://gateway.example.com"), want: "tracker.jira.api_url must be unset"},
		{name: "offline", tracking: jiraTracking(testJiraSite, ""), offline: true, want: "--offline"},
		{name: "lookup error", tracking: jiraTracking(testJiraSite, ""), discover: lookupErr, want: "could not resolve the Jira cloud ID for example.atlassian.net"},
		{name: "empty lookup", tracking: jiraTracking(testJiraSite, ""), discover: returns(""), want: "empty gateway URL"},
		{name: "other gateway host", tracking: jiraTracking(testJiraSite, ""), discover: returns("https://s3cret.example.com/ex/jira/" + testJiraCloudID), want: "is not under"},
		{name: "upper-case cloud ID", tracking: jiraTracking(testJiraSite, ""), discover: returns(gatewayURLFor("AAAAAAAA-bbbb-cccc-dddd-eeeeeeeeeeee")), want: "lowercase UUID"},
		{name: "path in cloud ID", tracking: jiraTracking(testJiraSite, ""), discover: returns(gatewayURLFor(testJiraCloudID + "/s3cret")), want: "lowercase UUID"},
		{name: "jira-sourced URL with bad cloud ID", apiURL: gatewayURLFor("s3cret"), tracking: jiraTracking(testJiraSite, ""), want: "lowercase UUID"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			discover := tt.discover
			if discover == nil {
				discover = noJiraLookup(t)
			}
			id, err := resolveJiraMintCloudID(context.Background(), tt.apiURL, tt.tracking, tt.offline, discover)
			require.Error(t, err)
			assert.Empty(t, id)
			assert.Contains(t, err.Error(), tt.want)
			assert.Contains(t, err.Error(), "minting the Jira access token")
			assert.NotContains(t, err.Error(), "s3cret", "no raw URL in the error")
		})
	}
}

// TestRunAgent_JiraMintImportsSiteProfile: the minting run looks the
// cloud ID up once, imports the per-site profile instead of the embedded
// one, and creates the run-scoped provider with the per-site id as its
// type (stubJiraProvider refuses any other type).
func TestRunAgent_JiraMintImportsSiteProfile(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", nil)

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Jira token file", "the run gets as far as the token file seed")
	assert.Equal(t, int32(1), calls.lookups.Load())
	require.Len(t, calls.creates, 1)
	imports := linesWithPrefix(lines, "provider profile import ")
	require.Len(t, imports, 1)
	assert.Contains(t, imports[0], string(filepath.Separator)+testJiraSiteProfileID+"-", "the per-site profile is imported")
}

// TestRunAgent_JiraMintWithoutSiteFailsBeforeMinting: a minting run with no
// tracker.jira.base_url stops before anything is minted or created.
func TestRunAgent_JiraMintWithoutSiteFailsBeforeMinting(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\nagents:\n  - harness/code.yaml\n"), 0o644))

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.jira.base_url is not configured")
	assert.Equal(t, int32(0), calls.lookups.Load())
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Empty(t, calls.creates)
	assert.Empty(t, linesWithPrefix(lines, "provider profile import "))
}

// TestRunAgent_JiraStaticTokenMakesNoLookup: a run that does not mint makes
// no cloud-ID lookup and keeps the embedded profile.
func TestRunAgent_JiraStaticTokenMakesNoLookup(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "static-jira-token-for-test")
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", nil)

	_, lines := runJiraAgent(t, dir)
	assert.Equal(t, int32(0), calls.lookups.Load())
	imports := linesWithPrefix(lines, "provider profile import ")
	require.Len(t, imports, 1)
	assert.NotContains(t, imports[0], testJiraSiteProfileID)
	assert.Contains(t, lines, "provider create --name jira --type fullsend-jira --credential JIRA_TOKEN")
}

// TestRunAgent_ListedJiraSiteProfileRefused: a listed profile under a
// per-site id is refused before any profile is imported, also with a
// static token.
func TestRunAgent_ListedJiraSiteProfileRefused(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "static-jira-token-for-test")
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"profiles/site.yaml": "id: " + testJiraSiteProfileID + "\ndisplay_name: squatter\ncategory: data\nendpoints:\n  - host: \"*\"\n    port: 443\n    access: read-write\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nopenshell:\n  profiles:\n    - profiles/site.yaml\nproviders:\n  - jira\n"), 0o644))

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved for the per-site Jira profile")
	assert.Empty(t, linesWithPrefix(lines, "provider profile import "), "nothing is imported")
	assert.Empty(t, linesWithPrefix(lines, "provider create "))
	assert.Equal(t, int32(0), calls.lookups.Load())
}
