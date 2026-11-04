package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	messages "github.com/cucumber/messages/go/v34"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestParseDummyAgentTable_EmptyIdentity(t *testing.T) {
	t.Parallel()
	w := &world.World{}
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}}},
			{Cells: []*messages.PickleTableCell{{Value: "x"}, {Value: "read_file"}, {Value: "foo"}}},
		},
	}
	err := parseDummyAgentTable(w, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestParseDummyAgentTable_RequiresFixturesRoot(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo"}
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}}},
			{Cells: []*messages.PickleTableCell{{Value: "x"}, {Value: "read_file"}, {Value: "foo"}}},
		},
	}
	err := parseDummyAgentTable(w, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FixturesRoot")
}

func TestParseDummyAgentTable_RejectsUnknownExpect(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo", FixturesRoot: "e2e/behaviour/fixtures"}
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}, {Value: "expect"}}},
			{Cells: []*messages.PickleTableCell{{Value: "x"}, {Value: "url_get"}, {Value: "https://example.com/"}, {Value: "pass"}}},
		},
	}
	err := parseDummyAgentTable(w, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expect must be empty or "fail"`)
}

func TestFindModuleSubdir(t *testing.T) {
	dir, err := findModuleSubdir("pkg/behaviourtest/steps")
	require.NoError(t, err)
	assert.DirExists(t, dir)
}

// fixtureTable builds a dummy-agent table whose rows are op/args pairs.
func fixtureTable(rows ...[2]string) *godog.Table {
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}}},
		},
	}
	for _, r := range rows {
		table.Rows = append(table.Rows, &messages.PickleTableRow{
			Cells: []*messages.PickleTableCell{{Value: r[0]}, {Value: r[0]}, {Value: r[1]}},
		})
	}
	return table
}

func fixtureWorld(issue int) (*world.World, *fakeURLSCM) {
	scm := &fakeURLSCM{files: map[string][]byte{}}
	return &world.World{
		SCM:          scm,
		Org:          "test-org",
		RepoOwner:    "test-org",
		RepoName:     "test-repo-03",
		IssueNumber:  issue,
		FixturesRoot: "pkg/behaviourtest/steps/testdata",
	}, scm
}

func TestParseDummyAgentTable_WriteFixtureExpandsPlaceholders(t *testing.T) {
	w, scm := fixtureWorld(42)
	err := parseDummyAgentTable(w, fixtureTable(
		[2]string{"write_fixture", "output/agent-result.json, fixtures/placeholders.json"},
	))
	require.NoError(t, err)
	require.Len(t, w.DummyOps, 1)
	assert.Equal(t, "{\n"+
		"  \"target_repo\": \"test-org/test-repo-03\",\n"+
		"  \"title\": \"Proposal for #42\",\n"+
		"  \"body\": \"Filed from test-org/test-repo-03#42\"\n"+
		"}\n", w.DummyOps[0].Content)
	assert.Contains(t, string(scm.files["test-org/test-repo-03/"+w.BehaviourScriptPath()]), "test-org/test-repo-03#42")
}

func TestParseDummyAgentTable_WriteFixtureIssueBeforeIssueExists(t *testing.T) {
	w, scm := fixtureWorld(0)
	err := parseDummyAgentTable(w, fixtureTable(
		[2]string{"write_fixture", "output/agent-result.json, fixtures/placeholders.json"},
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixtures/placeholders.json")
	assert.Contains(t, err.Error(), "no issue exists yet")
	assert.NotContains(t, err.Error(), "target_repo", "the error names the token, not the fixture content")
	assert.Empty(t, scm.files, "no script is committed when the Given step fails")
}

func TestParseDummyAgentTable_WriteFixtureWithoutPlaceholdersUnchanged(t *testing.T) {
	// No issue exists yet: content without placeholders needs none.
	w, _ := fixtureWorld(0)
	err := parseDummyAgentTable(w, fixtureTable(
		[2]string{"write_fixture", "output/agent-result.json, fixtures/plain.json"},
	))
	require.NoError(t, err)
	require.Len(t, w.DummyOps, 1)
	assert.Equal(t, "{\n  \"target_repo\": \"fullsend-ai/agents\"\n}\n", w.DummyOps[0].Content)
}

func TestParseDummyAgentTable_PlaceholdersNotExpandedInOtherOps(t *testing.T) {
	w, _ := fixtureWorld(42)
	err := parseDummyAgentTable(w, fixtureTable(
		[2]string{"read_file", "docs/<repo>/<issue>.md"},
		[2]string{"write_fixture", "output/<repo>.json, fixtures/plain.json"},
	))
	require.NoError(t, err)
	require.Len(t, w.DummyOps, 2)
	assert.Equal(t, "docs/<repo>/<issue>.md", w.DummyOps[0].Args)
	assert.Empty(t, w.DummyOps[0].Content)
	assert.Equal(t, "output/<repo>.json, fixtures/plain.json", w.DummyOps[1].Args, "the write_fixture destination is not expanded")
}

func TestExpandFixturePlaceholders_RepoRequiresScenarioRepo(t *testing.T) {
	t.Parallel()
	_, err := expandFixturePlaceholders(&world.World{}, `{"target_repo": "<repo>"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repository is enrolled")
}

type fakeHeadSHASCM struct {
	*fakeDispatchSCM
	sha string
	err error
}

func (f *fakeHeadSHASCM) GetPullRequestHeadSHA(context.Context, string, string, int) (string, error) {
	return f.sha, f.err
}

func TestExpandPRHeadSHAPlaceholder(t *testing.T) {
	const fixture = `{"head_sha": "<pr_head_sha>"}`
	tests := []struct {
		name    string
		world   *world.World
		content string
		want    string
		wantErr string
	}{
		{name: "no placeholder", world: &world.World{}, content: `{"a": 1}`, want: `{"a": 1}`},
		{name: "no pull request", world: &world.World{SCM: &fakeHeadSHASCM{fakeDispatchSCM: &fakeDispatchSCM{}}}, content: fixture, wantErr: "no pull request exists yet"},
		{name: "driver cannot read head", world: &world.World{SCM: &fakeDispatchSCM{}, PRNumber: 7}, content: fixture, wantErr: "cannot read a pull request head SHA"},
		{name: "fetch error", world: &world.World{SCM: &fakeHeadSHASCM{fakeDispatchSCM: &fakeDispatchSCM{}, err: errors.New("boom")}, PRNumber: 7}, content: fixture, wantErr: "boom"},
		{name: "empty sha", world: &world.World{SCM: &fakeHeadSHASCM{fakeDispatchSCM: &fakeDispatchSCM{}}, PRNumber: 7}, content: fixture, wantErr: "empty head SHA"},
		{name: "expanded", world: &world.World{SCM: &fakeHeadSHASCM{fakeDispatchSCM: &fakeDispatchSCM{}, sha: "abc123"}, PRNumber: 7}, content: fixture, want: `{"head_sha": "abc123"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandPRHeadSHAPlaceholder(tc.world, tc.content)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
