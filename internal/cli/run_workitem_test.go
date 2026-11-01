package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func jiraEventMap(key, url string) map[string]any {
	return map[string]any{
		"source": map[string]any{"system": "jira"},
		"entity": map[string]any{"kind": "work_item", "id": float64(10042), "key": key, "url": url},
	}
}

func noEnv(string) string { return "" }

func TestResolveWorkItemIdentity_Jira(t *testing.T) {
	ev := jiraEventMap("PROJ-123", "https://example.atlassian.net/browse/PROJ-123")
	// Inherited values (e.g. a GitHub URL or the numeric Jira ID) must not
	// survive for a Jira-sourced event.
	getenv := func(k string) string {
		return map[string]string{
			envWorkItemSource: "github",
			envWorkItemKey:    "10042",
			envWorkItemURL:    "https://github.com/o/r/issues/10042",
		}[k]
	}

	got, err := resolveWorkItemIdentity(ev, "jira", "github", getenv)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		envWorkItemSource: "jira",
		envWorkItemKey:    "PROJ-123",
		envWorkItemURL:    "https://example.atlassian.net/browse/PROJ-123",
	}, got)
}

func TestResolveWorkItemIdentity_JiraMalformedKey(t *testing.T) {
	for _, key := range []string{"", "10042", "proj-1", "PROJ-0", "PROJ-", "-1", "P-1", "PROJ-1 x", "PROJ-1\n", "../PROJ-1", "PROJ-1;id"} {
		t.Run(key, func(t *testing.T) {
			_, err := resolveWorkItemIdentity(jiraEventMap(key, "https://example.atlassian.net/browse/PROJ-1"), "jira", "github", noEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "malformed entity.key")
		})
	}
}

func TestResolveWorkItemIdentity_JiraMalformedURL(t *testing.T) {
	for _, u := range []string{"", "http://example.atlassian.net/browse/PROJ-1", "https://user@example.atlassian.net/browse/PROJ-1", "browse/PROJ-1", "https:///browse/PROJ-1"} {
		t.Run(u, func(t *testing.T) {
			_, err := resolveWorkItemIdentity(jiraEventMap("PROJ-1", u), "jira", "github", noEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "malformed entity.url")
		})
	}
}

func TestResolveWorkItemIdentity_JiraMissingEvent(t *testing.T) {
	_, err := resolveWorkItemIdentity(nil, "jira", "github", noEnv)
	require.Error(t, err)
}

func TestResolveWorkItemIdentity_Forge(t *testing.T) {
	ghEvent := map[string]any{
		"source": map[string]any{"system": "github"},
		"entity": map[string]any{"kind": "work_item", "id": float64(42), "url": "https://github.com/o/r/issues/42"},
	}
	tests := []struct {
		name          string
		event         map[string]any
		trackerSource string
		forgePlatform string
		env           map[string]string
		want          map[string]string
	}{
		{
			name:          "github event sets source only",
			event:         ghEvent,
			trackerSource: "github",
			forgePlatform: "github",
			want:          map[string]string{envWorkItemSource: "github"},
		},
		{
			name:          "no event falls back to forge platform",
			forgePlatform: "gitlab",
			want:          map[string]string{envWorkItemSource: "gitlab"},
		},
		{
			name:          "caller-provided source is kept",
			event:         ghEvent,
			trackerSource: "github",
			forgePlatform: "github",
			env:           map[string]string{envWorkItemSource: "github"},
		},
		{
			name: "nothing known exports nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			got, err := resolveWorkItemIdentity(tt.event, tt.trackerSource, tt.forgePlatform, getenv)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			// Forge runs keep the workflow-exported key and URL untouched.
			assert.NotContains(t, got, envWorkItemKey)
			assert.NotContains(t, got, envWorkItemURL)
		})
	}
}

func TestIssueNumberFlagEnv(t *testing.T) {
	tests := []struct {
		name    string
		sOpts   statusOpts
		want    string
		wantSet bool
	}{
		{name: "github status number", sOpts: statusOpts{statusNum: 42, trackerSource: "github"}, want: "42", wantSet: true},
		{name: "unset source keeps status number", sOpts: statusOpts{statusNum: 7}, want: "7", wantSet: true},
		{name: "no status number", sOpts: statusOpts{}, wantSet: false},
		{name: "jira clears issue number", sOpts: statusOpts{statusNum: 123, trackerSource: "jira", trackerProject: "PROJ"}, want: "", wantSet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := issueNumberFlagEnv(tt.sOpts)
			assert.Equal(t, tt.wantSet, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// invalidJiraPayload embeds a Jira-sourced normalized event that fails
// validation (no entity.key), so extractNormalizedEventFromPayload drops it.
const invalidJiraPayload = `{"issue":{"number":10042,"html_url":"https://example.atlassian.net/browse/PROJ-1"},"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10042,"url":"https://example.atlassian.net/browse/PROJ-1"},"transition":{"kind":"comment_added","comment":{"body":"/fs-triage","command":"/fs-triage"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment"}}}`

func writeDispatchPayload(t *testing.T, dir, payload string) {
	t.Helper()
	dispatchDir := filepath.Join(dir, "dispatch")
	require.NoError(t, os.MkdirAll(dispatchDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dispatchDir, "event-payload.json"), []byte(payload), 0o644))
}

func TestDispatchPayloadClaimsJira(t *testing.T) {
	t.Run("invalid jira event in dispatch file", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", "")
		dir := t.TempDir()
		writeDispatchPayload(t, dir, invalidJiraPayload)
		require.Nil(t, extractNormalizedEventFromDispatch(dir), "fixture must fail normalized-event validation")
		assert.True(t, dispatchPayloadClaimsJira(dir))
	})

	t.Run("jira event in GITHUB_EVENT_PATH", func(t *testing.T) {
		dir := t.TempDir()
		wrapper, err := json.Marshal(map[string]any{"inputs": map[string]any{"event_payload": invalidJiraPayload}})
		require.NoError(t, err)
		path := filepath.Join(dir, "gh-event.json")
		require.NoError(t, os.WriteFile(path, wrapper, 0o644))
		t.Setenv("GITHUB_EVENT_PATH", path)
		assert.True(t, dispatchPayloadClaimsJira(dir))
	})

	for name, payload := range map[string]string{
		"github source":              `{"_normalized_event":{"source":{"system":"github"}}}`,
		"no normalized event":        `{"issue":{"number":42}}`,
		"normalized event not a map": `{"_normalized_event":"jira"}`,
		"not json":                   `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GITHUB_EVENT_PATH", "")
			dir := t.TempDir()
			writeDispatchPayload(t, dir, payload)
			assert.False(t, dispatchPayloadClaimsJira(dir))
		})
	}

	t.Run("no payloads", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", filepath.Join(t.TempDir(), "missing.json"))
		assert.False(t, dispatchPayloadClaimsJira(t.TempDir()))
	})
}

func TestDispatchJiraClaimUnresolved(t *testing.T) {
	const validGitHubPayload = `{"issue":{"number":10},"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10,"url":"https://github.com/org/repo/issues/10"},"transition":{"kind":"label_changed","label":{"name":"ready-to-code","action":"added"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":["ready-to-code"]},"source":{"system":"github","raw_type":"issues"}}}`

	// Invalid Jira dispatch file plus a valid forge event in GITHUB_EVENT_PATH:
	// the forge event is accepted, but the Jira claim must still fail closed.
	t.Run("invalid jira dispatch file with valid forge event", func(t *testing.T) {
		dir := t.TempDir()
		writeDispatchPayload(t, dir, invalidJiraPayload)
		wrapper, err := json.Marshal(map[string]any{"inputs": map[string]any{"event_payload": validGitHubPayload}})
		require.NoError(t, err)
		path := filepath.Join(dir, "gh-event.json")
		require.NoError(t, os.WriteFile(path, wrapper, 0o644))
		t.Setenv("GITHUB_EVENT_PATH", path)

		eventMap := extractNormalizedEventFromDispatch(dir)
		require.NotNil(t, eventMap, "forge event from GITHUB_EVENT_PATH must be accepted")
		trackerSource := extractMapString(eventMap, "source", "system")
		require.Equal(t, "github", trackerSource)
		assert.True(t, dispatchJiraClaimUnresolved("", trackerSource, dir))
	})

	t.Run("no event file and no tracker source", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", "")
		dir := t.TempDir()
		writeDispatchPayload(t, dir, invalidJiraPayload)
		assert.True(t, dispatchJiraClaimUnresolved("", "", dir))
	})

	t.Run("tracker source already jira", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", "")
		dir := t.TempDir()
		writeDispatchPayload(t, dir, invalidJiraPayload)
		assert.False(t, dispatchJiraClaimUnresolved("", "jira", dir))
	})

	t.Run("event file given", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", "")
		dir := t.TempDir()
		writeDispatchPayload(t, dir, invalidJiraPayload)
		assert.False(t, dispatchJiraClaimUnresolved("event.json", "github", dir))
	})

	t.Run("no jira claim", func(t *testing.T) {
		t.Setenv("GITHUB_EVENT_PATH", "")
		dir := t.TempDir()
		writeDispatchPayload(t, dir, validGitHubPayload)
		assert.False(t, dispatchJiraClaimUnresolved("", "github", dir))
	})
}

func writeCodeHarnessFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	writePolicyFixture(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: coder\npolicy: policies/base.yaml\nrunner_env:\n  MY_SOURCE: ${FULLSEND_WORK_ITEM_SOURCE}\n  MY_KEY: ${FULLSEND_WORK_ITEM_KEY}\n  MY_URL: ${FULLSEND_WORK_ITEM_URL}\n  MY_ISSUE: ${ISSUE_NUMBER}\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	return dir
}

func runCodeAgentForWorkItemTest(t *testing.T, dir string) error {
	t.Helper()
	var buf bytes.Buffer
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	return runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags, statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
}

func unsetWorkItemEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{envWorkItemSource, envWorkItemKey, envWorkItemURL, "ISSUE_NUMBER"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
	t.Setenv("FULLSEND_MINT_URL", "")
}

// TestRunAgent_JiraWorkItemIdentityExported verifies that a Jira-sourced
// dispatch exports FULLSEND_WORK_ITEM_* and ISSUE_NUMBER before harness env
// validation, so a harness referencing them gets past validation even when
// the workflow did not set them (#2264).
func TestRunAgent_JiraWorkItemIdentityExported(t *testing.T) {
	useFakeOpenshell(t)
	unsetWorkItemEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", "")
	dir := writeCodeHarnessFixture(t)
	writeDispatchPayload(t, dir, `{"issue":{"number":10042,"html_url":"https://example.atlassian.net/browse/PROJ-1"},"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10042,"key":"PROJ-1","url":"https://example.atlassian.net/browse/PROJ-1"},"transition":{"kind":"comment_added","comment":{"body":"/fs-code","command":"/fs-code"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment"}}}`)

	err := runCodeAgentForWorkItemTest(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
	assert.NotContains(t, err.Error(), "validating env")
	// Values are restored once runAgent returns.
	_, set := os.LookupEnv(envWorkItemKey)
	assert.False(t, set)
}

func TestRunAgent_JiraInvalidNormalizedEventFailsClosed(t *testing.T) {
	useFakeOpenshell(t)
	unsetWorkItemEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", "")
	dir := writeCodeHarnessFixture(t)
	writeDispatchPayload(t, dir, invalidJiraPayload)

	err := runCodeAgentForWorkItemTest(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares a Jira source")
}

func TestRunAgent_JiraMalformedURLFailsClosed(t *testing.T) {
	useFakeOpenshell(t)
	unsetWorkItemEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", "")
	dir := writeCodeHarnessFixture(t)
	writeDispatchPayload(t, dir, `{"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10042,"key":"PROJ-1","url":"http://example.atlassian.net/browse/PROJ-1"},"transition":{"kind":"comment_added","comment":{"body":"/fs-code","command":"/fs-code"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment"}}}`)

	err := runCodeAgentForWorkItemTest(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed entity.url")
}
