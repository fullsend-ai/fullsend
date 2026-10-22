package steps

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const celVar = "FULLSEND_CEL_ROUTING_REVIEW"

func TestGivenRepoVariable_SetsAndRestoresAbsentVariable(t *testing.T) {
	fc := forge.NewFakeClient()
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}

	require.NoError(t, givenRepoVariable(w, celVar, "shadow"))
	require.NoError(t, givenRepoVariable(w, celVar, "on"))
	assert.Equal(t, "on", fc.VariableValues["org/repo/"+celVar])

	require.NoError(t, RestoreRepoVariables(w))
	_, exists := fc.VariableValues["org/repo/"+celVar]
	assert.False(t, exists, "a variable that did not exist is deleted")
	assert.Empty(t, w.RepoVariablesOriginal)
}

func TestGivenRepoVariable_RestoresPreviousValue(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues = map[string]string{"org/repo/" + celVar: "off"}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}

	require.NoError(t, givenRepoVariable(w, celVar, "on"))
	require.NoError(t, RestoreRepoVariables(w))
	assert.Equal(t, "off", fc.VariableValues["org/repo/"+celVar])
}

func TestGivenRepoVariable_RequiresRepo(t *testing.T) {
	err := givenRepoVariable(&world.World{}, celVar, "on")
	require.ErrorContains(t, err, "enrolled test repository")
}

func TestGivenRepoVariable_ReadErrorDoesNotSet(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"GetRepoVariable": errors.New("boom")}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}

	require.ErrorContains(t, givenRepoVariable(w, celVar, "on"), "boom")
	assert.Empty(t, fc.VariableValues)
	assert.Empty(t, w.RepoVariablesOriginal)
}

func TestRestoreRepoVariables_NotFoundIsFine(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoVariable": fmt.Errorf("%w: variable", forge.ErrNotFound)}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc),
		RepoVariablesOriginal: map[string]*string{celVar: nil}}
	require.NoError(t, RestoreRepoVariables(w))
	assert.Empty(t, w.RepoVariablesOriginal)
}

func TestRestoreRepoVariables_ErrorKeepsEntryForRetry(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoVariable": errors.New("boom")}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc),
		RepoVariablesOriginal: map[string]*string{celVar: nil}}
	require.ErrorContains(t, RestoreRepoVariables(w), "boom")
	assert.Contains(t, w.RepoVariablesOriginal, celVar, "a failed restore is retried by cleanupRetry")
}
