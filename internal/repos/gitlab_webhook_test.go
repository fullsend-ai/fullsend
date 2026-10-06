package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	webhookTestOwner = "group"
	webhookTestRepo  = "app"
	webhookTestBase  = "https://gitlab.example.com"
)

// webhookFake wraps FakeClient with a GitLab base URL and mirrors stored
// secrets into ListRepoVariables, as live GitLab returns masked variable
// values to a Maintainer-capable client.
type webhookFake struct {
	*forge.FakeClient
	base string
}

func (w webhookFake) BaseURL() string { return w.base }

func (w webhookFake) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := w.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	key := owner + "/" + repo + "/" + name
	w.VariableValues[key] = value
	// Live GitLab rewrites the variable as masked, protected, env_var.
	delete(w.SecretProtections, key)
	return nil
}

// DeleteRepoSecret also drops the mirrored variable value, as live GitLab
// stops returning a deleted variable.
func (w webhookFake) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	if err := w.FakeClient.DeleteRepoSecret(ctx, owner, repo, name); err != nil {
		return err
	}
	key := owner + "/" + repo + "/" + name
	delete(w.VariableValues, key)
	delete(w.SecretProtections, key)
	return nil
}

// echoFake returns API errors that echo the submitted credentials, as a
// misbehaving GitLab or proxy could.
type echoFake struct {
	webhookFake
	echoHook, echoSecret bool
}

func (e echoFake) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	if e.echoHook {
		return nil, errors.New("400 bad request: url=" + hook.URL + " token=" + hook.Token)
	}
	return e.webhookFake.CreateProjectHook(ctx, owner, repo, hook)
}

func (e echoFake) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if e.echoSecret && name == forge.SecretWebhookSecret {
		return errors.New("400 bad request: value " + value + " rejected")
	}
	return e.webhookFake.CreateRepoSecret(ctx, owner, repo, name, value)
}

// newWebhookFake returns a client whose project satisfies every
// readiness gate: dispatcher-including wrapper and template on the
// default branch, protected default branch, no_one_allowed.
func newWebhookFake() webhookFake {
	f := forge.NewFakeClient()
	f.Repos = []forge.Repository{{ID: 42, Name: webhookTestRepo, FullName: webhookTestOwner + "/" + webhookTestRepo, DefaultBranch: "main"}}
	return seedWebhookFake(f)
}

// webhookTestRootCI is a committed root .gitlab-ci.yml that includes the
// wrapper, keeps the dispatch stage, and admits trigger pipelines.
const webhookTestRootCI = "include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\nworkflow:\n  rules:\n    - if: '" + triggerDispatcherRuleIf + "'\n"

// seedWebhookFake satisfies the webhook readiness gates on f, whose
// Repos must already contain the test project.
func seedWebhookFake(f *forge.FakeClient) webhookFake {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	// The shipped typed wrapper, agent, and poll templates, and a root that
	// forwards the pipeline-input contract, are the committed state a
	// typed installation reaches before the fast-path may be enabled.
	for _, path := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath, fullsendDispatcherTemplatePath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		if err != nil {
			panic(err)
		}
		f.FileContents[prefix+path] = content
	}
	base, err := newGitLabCI()
	if err != nil {
		panic(err)
	}
	root, err := mergeGitLabCIWithWrapper(base, f.FileContents[prefix+fullsendPipelineInclude])
	if err != nil {
		panic(err)
	}
	f.FileContents[prefix+".gitlab-ci.yml"] = root
	f.FileContents[prefix+gitlabInstallCLIScriptPath] = []byte("#!/bin/sh\n")
	f.FileContents[prefix+gitlabDispatcherJobScriptPath] = []byte("#!/bin/sh\n")
	f.ProtectedBranches[prefix+"main"] = true
	f.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = forge.PipelineVarOverrideNoOneAllowed
	return webhookFake{FakeClient: f, base: webhookTestBase}
}

func (w webhookFake) hooks() []forge.ProjectHook {
	return w.ProjectHooks[webhookTestOwner+"/"+webhookTestRepo]
}

func (w webhookFake) triggers() []forge.PipelineTriggerToken {
	return w.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo]
}

func (w webhookFake) variable(name string) string {
	return w.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+name]
}

func ensureWebhook(t *testing.T, c webhookFake, rotate, dryRun bool) GitLabWebhookResult {
	t.Helper()
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, rotate, dryRun)
	require.NoError(t, err)
	return res
}

// assertNoCredentialLeak checks that result details never carry the
// trigger token, webhook secret, or webhook URL.
func assertNoCredentialLeak(t *testing.T, c webhookFake, res GitLabWebhookResult) {
	t.Helper()
	joined := strings.Join(res.Details, "\n")
	for _, secret := range []string{c.variable(forge.SecretTriggerToken), c.variable(forge.SecretWebhookSecret)} {
		if secret != "" {
			assert.NotContains(t, joined, secret)
		}
	}
	assert.NotContains(t, joined, "token=")
	assert.NotContains(t, joined, webhookTestBase)
	for _, tok := range c.CreatedTriggerTokens {
		assert.NotContains(t, joined, tok.Token)
	}
}

func TestGitLabWebhookTriggerURL(t *testing.T) {
	got := GitLabWebhookTriggerURL("https://gitlab.example.com/", 7, "release/1.x", "a b")
	assert.Equal(t, "https://gitlab.example.com/api/v4/projects/7/ref/release%2F1.x/trigger/pipeline?token=a+b", got)
}

func TestEnsureGitLabWebhookFastPath_FreshInstallProvisions(t *testing.T) {
	c := newWebhookFake()
	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 1)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, GitLabWebhookTriggerDescription, c.triggers()[0].Description)

	tok := c.CreatedTriggerTokens[0].Token
	assert.Equal(t, tok, c.variable(forge.SecretTriggerToken))
	secret := c.variable(forge.SecretWebhookSecret)
	assert.Len(t, secret, 64)

	require.Len(t, c.hooks(), 1)
	hook := c.hooks()[0]
	assert.Equal(t, webhookTestBase+"/api/v4/projects/42/ref/main/trigger/pipeline?token="+tok, hook.URL)
	assert.Equal(t, GitLabWebhookName, hook.Name)
	require.Len(t, c.CreatedProjectHooks, 1)
	assert.Equal(t, secret, c.CreatedProjectHooks[0].Token)
	assert.Empty(t, hook.Token, "GitLab never returns the webhook secret")
	assert.True(t, hook.IssuesEvents)
	assert.True(t, hook.MergeRequestsEvents)
	assert.True(t, hook.NoteEvents)
	assert.False(t, hook.PushEvents)
	assert.False(t, hook.ConfidentialIssuesEvents)
	assert.False(t, hook.ConfidentialNoteEvents)
	assert.True(t, hook.EnableSSLVerification)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assertNoCredentialLeak(t, c, res)

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestEnsureGitLabWebhookFastPath_ReinstallIsNoOp(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	secretsBefore := len(c.CreatedSecrets)

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "none", res.Action)
	assert.Empty(t, res.Details)
	assert.Len(t, c.CreatedTriggerTokens, 1)
	assert.Len(t, c.CreatedProjectHooks, 1)
	assert.Empty(t, c.UpdatedProjectHooks)
	assert.Empty(t, c.DeletedProjectHookIDs)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assert.Len(t, c.CreatedSecrets, secretsBefore)
	assert.Len(t, c.hooks(), 1)
	assert.Len(t, c.triggers(), 1)
}

func TestEnsureGitLabWebhookFastPath_RepairsMissingWebhook(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldID := c.triggers()[0].ID
	c.ProjectHooks[webhookTestOwner+"/"+webhookTestRepo] = nil

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs)

	res := ensureWebhook(t, c, false, false)

	// With the hook gone, nothing ties the stored token to the surviving
	// trigger, so a fresh token is minted and the old one revoked.
	assert.Equal(t, "update", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 2)
	tok := c.CreatedTriggerTokens[1].Token
	assert.Equal(t, tok, c.variable(forge.SecretTriggerToken))
	require.Len(t, c.hooks(), 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+tok)
	assert.Equal(t, []int64{oldID}, c.RevokedTriggerTokenIDs)
	require.Len(t, c.triggers(), 1)
	assertNoCredentialLeak(t, c, res)
}

func TestEnsureGitLabWebhookFastPath_RepairsMissingTriggerToken(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = nil

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 2)
	newTok := c.CreatedTriggerTokens[1].Token
	assert.Equal(t, newTok, c.variable(forge.SecretTriggerToken))
	require.Len(t, c.UpdatedProjectHooks, 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+newTok)
	assert.Len(t, c.CreatedProjectHooks, 1)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assertNoCredentialLeak(t, c, res)
}

func TestEnsureGitLabWebhookFastPath_RepairsMissingStoredToken(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldID := c.triggers()[0].ID
	delete(c.VariableValues, webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretTriggerToken)

	ensureWebhook(t, c, false, false)

	require.Len(t, c.CreatedTriggerTokens, 2)
	assert.Equal(t, c.CreatedTriggerTokens[1].Token, c.variable(forge.SecretTriggerToken))
	assert.Equal(t, []int64{oldID}, c.RevokedTriggerTokenIDs, "unreadable old token is superseded")
	require.Len(t, c.triggers(), 1)
}

func TestEnsureGitLabWebhookFastPath_RepairsMissingWebhookSecret(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldSecret := c.variable(forge.SecretWebhookSecret)
	delete(c.VariableValues, webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretWebhookSecret)

	ensureWebhook(t, c, false, false)

	newSecret := c.variable(forge.SecretWebhookSecret)
	assert.NotEmpty(t, newSecret)
	assert.NotEqual(t, oldSecret, newSecret)
	require.Len(t, c.UpdatedProjectHooks, 1)
	assert.Equal(t, newSecret, c.UpdatedProjectHooks[0].Token)
	assert.Len(t, c.CreatedTriggerTokens, 1)
}

func TestEnsureGitLabWebhookFastPath_RepairsDriftAndDuplicates(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	hooks := c.ProjectHooks[key]
	hooks[0].PushEvents = true
	hooks[0].URL = strings.Replace(hooks[0].URL, "/ref/main/", "/ref/feature/", 1)
	// A second, name-less hook (GitLab < 17.1) using the stored trigger token.
	storedTok := c.variable(forge.SecretTriggerToken)
	hooks = append(hooks, forge.ProjectHook{ID: 99, URL: webhookTestBase + "/api/v4/projects/42/ref/main/trigger/pipeline?token=" + storedTok})
	// An unrelated hook must be left alone.
	hooks = append(hooks, forge.ProjectHook{ID: 100, URL: "https://ci.example.com/hook", PushEvents: true})
	c.ProjectHooks[key] = hooks

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.UpdatedProjectHooks, 1)
	assert.False(t, c.UpdatedProjectHooks[0].PushEvents)
	assert.Contains(t, c.UpdatedProjectHooks[0].URL, "/ref/main/trigger/pipeline")
	assert.Equal(t, []int64{99}, c.DeletedProjectHookIDs)
	require.Len(t, c.hooks(), 2)
	assert.Equal(t, int64(100), c.hooks()[1].ID)
	assertNoCredentialLeak(t, c, res)
}

func TestEnsureGitLabWebhookFastPath_Rotate(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldID := c.triggers()[0].ID
	oldTok := c.variable(forge.SecretTriggerToken)
	secret := c.variable(forge.SecretWebhookSecret)

	res := ensureWebhook(t, c, true, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 2)
	newTok := c.CreatedTriggerTokens[1].Token
	assert.NotEqual(t, oldTok, newTok)
	assert.Equal(t, newTok, c.variable(forge.SecretTriggerToken))
	assert.Equal(t, secret, c.variable(forge.SecretWebhookSecret), "rotation keeps the webhook secret")
	require.Len(t, c.UpdatedProjectHooks, 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+newTok)
	assert.Equal(t, []int64{oldID}, c.RevokedTriggerTokenIDs)
	require.Len(t, c.triggers(), 1)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Rotated pipeline trigger token")
	assertNoCredentialLeak(t, c, res)
}

func TestEnsureGitLabWebhookFastPath_RevokeFailureReported(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("boom")}

	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoking superseded pipeline trigger token")
	// The webhook already points at the new token.
	assert.Contains(t, c.hooks()[0].URL, "token="+c.CreatedTriggerTokens[1].Token)
}

func TestEnsureGitLabWebhookFastPath_StoreFailureRevokesNewToken(t *testing.T) {
	c := newWebhookFake()
	c.Errors = map[string]error{"CreateRepoSecret": errors.New("boom")}

	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), forge.SecretTriggerToken)
	require.Len(t, c.CreatedTriggerTokens, 1)
	assert.Equal(t, []int64{c.CreatedTriggerTokens[0].ID}, c.RevokedTriggerTokenIDs)
	assert.Empty(t, c.CreatedProjectHooks)
	assert.NotContains(t, err.Error(), c.CreatedTriggerTokens[0].Token)
}

func TestEnsureGitLabWebhookFastPath_StepErrors(t *testing.T) {
	for _, method := range []string{"GetRepo", "ListPipelineTriggerTokens", "ListRepoVariables", "ListProjectHooks", "CreatePipelineTriggerToken", "CreateProjectHook", "IsProtectedBranch", "GetPipelineVariablesMinimumOverrideRole"} {
		t.Run(method, func(t *testing.T) {
			c := newWebhookFake()
			c.Errors = map[string]error{method: errors.New("boom")}
			_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			require.Error(t, err)
		})
	}
	t.Run("UpdateProjectHook", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"UpdateProjectHook": errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
		require.Error(t, err)
		assert.Empty(t, c.RevokedTriggerTokenIDs, "old token must stay valid while the webhook still uses it")
	})
	t.Run("GetFileContent", func(t *testing.T) {
		c := newWebhookFake()
		c.GetFileContentErrors = map[string]error{webhookTestOwner + "/" + webhookTestRepo + "/" + fullsendPipelineInclude: errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
	})
	t.Run("GetDispatcherTemplate", func(t *testing.T) {
		c := newWebhookFake()
		c.GetFileContentErrors = map[string]error{webhookTestOwner + "/" + webhookTestRepo + "/" + fullsendDispatcherTemplatePath: errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
	})
	t.Run("DeleteDuplicate", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		key := webhookTestOwner + "/" + webhookTestRepo
		c.ProjectHooks[key] = append(c.ProjectHooks[key], forge.ProjectHook{ID: 77, Name: GitLabWebhookName})
		c.Errors = map[string]error{"DeleteProjectHook": errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
	})
}

func TestEnsureGitLabWebhookFastPath_DeferredGates(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	cases := map[string]func(c webhookFake){
		"wrapper without dispatcher": func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("include: []\n")
		},
		"wrapper missing": func(c webhookFake) {
			delete(c.FileContents, prefix+fullsendPipelineInclude)
		},
		"root CI missing": func(c webhookFake) {
			delete(c.FileContents, prefix+".gitlab-ci.yml")
		},
		"root CI without wrapper include": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("stages: [dispatch]\n")
		},
		"root CI stages without dispatch": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [build, poll, agent]\n")
		},
		"root CI workflow rejects trigger pipelines": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nworkflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n")
		},
		"root CI earlier unconditional deny precedes the admit rule": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    - when: never\n    - if: '" + triggerDispatcherRuleIf + "'\n"))
		},
		"root CI earlier matching trigger deny precedes the admit rule": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - if: '" + triggerDispatcherRuleIf + "'\n"))
		},
		"root CI earlier rule depends on a project variable": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    - if: '$PROJECT_FREEZE == \"true\"'\n      when: never\n    - if: '" + triggerDispatcherRuleIf + "'\n"))
		},
		"root CI rule uses an unevaluable keyword": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    - changes: ['docs/**']\n    - if: '" + triggerDispatcherRuleIf + "'\n"))
		},
		"root CI wrapper include restricted to schedules": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\nstages: [dispatch, poll, agent]\n")
		},
		"root CI aliased workflow rules deny triggers": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\n.shared_rules: &shared_rules\n  - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n    when: never\n  - when: always\nworkflow:\n  rules: *shared_rules\n")
		},
		"root CI aliased workflow mapping denies triggers": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\n.shared_workflow: &shared_workflow\n  rules:\n    - when: never\nworkflow: *shared_workflow\n")
		},
		"root CI workflow rules are not a sequence": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\nworkflow:\n  rules: always\n")
		},
		"root CI deny depends on open merge requests": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    - if: '$CI_COMMIT_BRANCH && $CI_OPEN_MERGE_REQUESTS'\n      when: never\n    - if: '" + triggerDispatcherRuleIf + "'\n"))
		},
		"root CI aliased stages without dispatch": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\n.s: &s [build, poll, agent]\nstages: *s\n")
		},
		"root CI stages entry is an alias to a non-scalar": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\n.s: &s [dispatch]\nstages: [*s, poll]\n")
		},
		"root CI without stages inherits wrapper stages without dispatch": func(c webhookFake) {
			// The seeded root declares no stages: of its own.
			wrapper := string(c.FileContents[prefix+fullsendPipelineInclude])
			require.Contains(t, wrapper, "  - dispatch\n  - poll\n  - agent")
			c.FileContents[prefix+fullsendPipelineInclude] = []byte(strings.Replace(wrapper, "  - dispatch\n  - poll\n  - agent", "  - poll\n  - agent", 1))
		},
		"no configuration declares stages": func(c webhookFake) {
			wrapper := string(c.FileContents[prefix+fullsendPipelineInclude])
			start := strings.Index(wrapper, "\nstages:\n")
			require.GreaterOrEqual(t, start, 0)
			end := strings.Index(wrapper[start:], "  - agent\n")
			require.GreaterOrEqual(t, end, 0)
			c.FileContents[prefix+fullsendPipelineInclude] = []byte(wrapper[:start+1] + wrapper[start+end+len("  - agent\n"):])
		},
		"root CI workflow inherits deny rules through a merge key": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\n.base: &base\n  rules:\n    - when: never\nworkflow:\n  <<: *base\n")
		},
		"wrapper dispatcher include restricted to schedules": func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("include:\n  - local: '" + fullsendDispatcherTemplatePath + "'\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n")
		},
		"wrapper dispatcher include denies triggers": func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("include:\n  - local: '" + fullsendDispatcherTemplatePath + "'\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n        when: never\n      - when: always\n")
		},
		"wrapper unparseable": func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("# " + fullsendDispatcherTemplatePath + "\ninclude: [unterminated\n")
		},
		"wrapper mentions dispatcher outside an include": func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("# " + fullsendDispatcherTemplatePath + "\ninclude: []\n")
		},
		"other protected branch rule": func(c webhookFake) {
			c.ProtectedBranches[prefix+"release"] = true
		},
		"root CI empty rules": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules("    []\n"))
		},
		"root CI unparseable": func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte("include: [unterminated\n")
		},
		"dispatcher template missing": func(c webhookFake) {
			delete(c.FileContents, prefix+fullsendDispatcherTemplatePath)
		},
		"unprotected default branch": func(c webhookFake) {
			c.ProtectedBranches[prefix+"main"] = false
		},
		"override role not restricted": func(c webhookFake) {
			c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = "developer"
		},
		"no default branch": func(c webhookFake) {
			c.Repos[0].DefaultBranch = ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			mutate(c)
			res := ensureWebhook(t, c, false, false)
			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], "deferred")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
			assert.Empty(t, c.CreatedSecrets)
		})
	}
}

// webhookRootCIWithRules is a root .gitlab-ci.yml that includes the
// wrapper and keeps the dispatch stage, with the given workflow:rules
// body (already indented under "rules:").
func webhookRootCIWithRules(rules string) string {
	return "include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch, poll, agent]\nworkflow:\n  rules:\n" + rules
}

// Readiness evaluates workflow:rules first-match, so equivalent
// admissions are accepted without the exact Fullsend expression.
func TestEnsureGitLabWebhookFastPath_AdmissionEquivalents(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	cases := map[string]string{
		"unconditional allow":                 "    - when: always\n",
		"unconditional allow without when":    "    - {}\n",
		"broader trigger allow":               "    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n",
		"allow on protected refs":             "    - if: '$CI_COMMIT_REF_PROTECTED == \"true\"'\n",
		"allow on default branch name":        "    - if: '$CI_COMMIT_BRANCH == \"main\"'\n",
		"allow on default branch variable":    "    - if: '$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH'\n",
		"allow via alternation":               "    - if: '$CI_PIPELINE_SOURCE == \"schedule\" || $CI_PIPELINE_SOURCE == \"trigger\"'\n",
		"allow via regex":                     "    - if: '$CI_PIPELINE_SOURCE =~ /^(trigger|api)$/'\n",
		"allow via negation":                  "    - if: '$CI_PIPELINE_SOURCE != \"merge_request_event\"'\n",
		"allow behind non-matching deny":      "    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n      when: never\n    - when: always\n",
		"allow behind debug-trace deny":       "    - if: '" + debugTraceDenyRuleIf + "'\n      when: never\n    - if: '" + triggerDispatcherRuleIf + "'\n",
		"unevaluable rule after the decision": "    - when: always\n    - changes: ['docs/**']\n",
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookRootCIWithRules(rules))
			assertPassesRootAdmission(t, c)
		})
	}
}

// assertPassesRootAdmission asserts that the committed root's include,
// stage, and workflow admission checks accept it. These hand-written roots
// do not forward the typed pipeline-input contract, so readiness defers
// only at that later gate, which proves every earlier check passed.
func assertPassesRootAdmission(t *testing.T, c webhookFake) {
	t.Helper()
	reason, err := gitlabWebhookReadiness(context.Background(), c, webhookTestOwner, webhookTestRepo, "main")
	require.NoError(t, err)
	assert.Contains(t, reason, "root .gitlab-ci.yml declares and forwards the pipeline-input contract")
}

// The effective stages: list follows root-over-wrapper-over-template
// replacement precedence; with no declaration anywhere GitLab's default stages
// (which lack dispatch) apply.
func TestGitLabEffectiveStagesProblem(t *testing.T) {
	problem := func(root, wrapper, template string) string {
		return gitlabEffectiveStagesProblem([]byte(root), []byte(wrapper), []byte(template))
	}
	withDispatch := "stages: [dispatch, poll, agent]\n"
	withoutDispatch := "stages: [poll, agent]\n"

	assert.Empty(t, problem("include: []\n", withDispatch, ""))
	assert.Empty(t, problem(withDispatch, withoutDispatch, ""))
	assert.Empty(t, problem("include: []\n", "include: []\n", withDispatch))
	assert.Contains(t, problem("include: []\n", withoutDispatch, ""), "lists the dispatch stage")
	assert.Contains(t, problem(withoutDispatch, withDispatch, withDispatch), "lists the dispatch stage")
	assert.Contains(t, problem("include: []\n", "include: []\n", ""), "default stages apply")
	assert.Contains(t, problem("include: []\n", "include: []\n", withoutDispatch), "lists the dispatch stage")
	// A file that does not parse contributes nothing.
	assert.Empty(t, problem("include: [unterminated\n", withDispatch, ""))
	// A stages list that cannot be evaluated is a problem.
	assert.Contains(t, problem("include: []\n", "stages: always\n", withDispatch), "cannot be evaluated")
}

func TestGitLabRootCIDispatcherProblem_Diagnostics(t *testing.T) {
	problem := func(rules string) string {
		return gitlabRootCIDispatcherProblem([]byte(webhookRootCIWithRules(rules)), "main")
	}
	assert.Empty(t, problem("    - if: '"+triggerDispatcherRuleIf+"'\n"))
	assert.Contains(t, problem("    - when: never\n    - when: always\n"), "workflow rule 1 rejects them first")
	assert.Contains(t, problem("    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n"), "no workflow rule matches them")
	assert.Contains(t, problem("    - if: '$PROJECT_FREEZE == \"true\"'\n      when: never\n    - when: always\n"), "workflow rule 1 cannot be evaluated")
	assert.Contains(t, problem("    - if: '$CI_PIPELINE_SOURCE =='\n"), "cannot be evaluated")
	assert.Contains(t, problem("    - if: '$CI_COMMIT_BRANCH && $CI_OPEN_MERGE_REQUESTS'\n      when: never\n    - when: always\n"), "workflow rule 1 cannot be evaluated")
}

func TestGitLabRootCIDispatcherProblem_IncludeRules(t *testing.T) {
	root := func(includeRules string) string {
		return "include:\n  - local: '" + fullsendPipelineInclude + "'\n" + includeRules + "stages: [dispatch]\n"
	}
	problem := func(ci string) string { return gitlabRootCIDispatcherProblem([]byte(ci), "main") }

	assert.Empty(t, problem(root("")))
	assert.Empty(t, problem(root("    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n")))
	assert.Empty(t, problem(root("    rules:\n      - when: always\n")))
	assert.Contains(t, problem(root("    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n")), "no include rule matches them")
	assert.Contains(t, problem(root("    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n        when: never\n")), "include rule 1 rejects them first")
	assert.Contains(t, problem(root("    rules:\n      - exists: ['Makefile']\n")), "include rule 1 cannot be evaluated")
	assert.Contains(t, problem(root("    rules: always\n")), "include rules cannot be evaluated")
	assert.Contains(t, problem(root("    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"$[[ inputs.stage ]]\"'\n")), "include rule 1 cannot be evaluated")
	assert.Contains(t, problem(root("    rules:\n      - when: $[[ inputs.when ]]\n")), "include rule 1 cannot be evaluated")

	// A second wrapper include without rules still applies.
	assert.Empty(t, problem("include:\n  - local: '"+fullsendPipelineInclude+"'\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n  - local: '"+fullsendPipelineInclude+"'\nstages: [dispatch]\n"))

	// An aliased include sequence is resolved.
	assert.Empty(t, problem(".inc: &inc\n  - local: '"+fullsendPipelineInclude+"'\ninclude: *inc\nstages: [dispatch]\n"))

	// Rules inherited through a merge key apply to the wrapper's dispatcher
	// include; explicit rules override them. (A root include item carrying a
	// merge key is not recognized as the wrapper include and already defers.)
	wrapper := func(item string) string {
		return ".sched: &sched\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\ninclude:\n  - local: '" + fullsendDispatcherTemplatePath + "'\n" + item
	}
	assert.Contains(t, gitlabWrapperDispatcherProblem([]byte(wrapper("    <<: *sched\n")), "main"), "no include rule matches them")
	assert.Empty(t, gitlabWrapperDispatcherProblem([]byte(wrapper("    rules:\n      - when: always\n    <<: *sched\n")), "main"))
	// A merge source that cannot be evaluated defers instead of admitting.
	assert.Contains(t, gitlabWrapperDispatcherProblem([]byte(wrapper("    <<: not-a-mapping\n")), "main"), "include rules cannot be evaluated")
}

func TestGitLabRootCIDispatcherProblem_AliasedWorkflow(t *testing.T) {
	ci := func(workflow string) string {
		return "include:\n  - local: '" + fullsendPipelineInclude + "'\nstages: [dispatch]\n" + workflow
	}
	problem := func(s string) string { return gitlabRootCIDispatcherProblem([]byte(s), "main") }

	denied := ".r: &r\n  - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n    when: never\n  - when: always\nworkflow:\n  rules: *r\n"
	assert.Contains(t, problem(ci(denied)), "workflow rule 1 rejects them first")
	allowed := ".r: &r\n  - when: always\nworkflow:\n  rules: *r\n"
	assert.Empty(t, problem(ci(allowed)))
	assert.Contains(t, problem(ci("workflow:\n  rules: always\n")), "cannot be evaluated")
	assert.Contains(t, problem(ci("workflow: always\n")), "cannot be evaluated")
	// workflow without rules is fine.
	assert.Empty(t, problem(ci("workflow:\n  name: x\n")))
}

func TestEvalWorkflowIf(t *testing.T) {
	env := triggerPipelineEnv("main")
	cases := []struct {
		expr string
		want triadState
	}{
		{`$CI_PIPELINE_SOURCE == "trigger"`, triadTrue},
		{`$CI_PIPELINE_SOURCE == 'schedule'`, triadFalse},
		{`$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH`, triadTrue},
		{`$CI_COMMIT_TAG`, triadFalse},
		{`$CI_COMMIT_TAG == null`, triadTrue},
		{`$CI_COMMIT_REF_PROTECTED`, triadTrue},
		{`$CI_DEBUG_TRACE =~ /^(1|t|true)$/i`, triadFalse},
		{`$CI_PIPELINE_SOURCE =~ /TRIG/i`, triadTrue},
		{`$CI_PIPELINE_SOURCE !~ /^trigger$/`, triadFalse},
		{`$PROJECT_VAR == "x"`, triadUnknown},
		{`$PROJECT_VAR == "x" && $CI_PIPELINE_SOURCE == "schedule"`, triadFalse},
		{`$PROJECT_VAR == "x" || $CI_PIPELINE_SOURCE == "trigger"`, triadTrue},
		{`$PROJECT_VAR == "x" && $CI_PIPELINE_SOURCE == "trigger"`, triadUnknown},
		{`($CI_PIPELINE_SOURCE == "schedule" || $CI_PIPELINE_SOURCE == "trigger") && $CI_COMMIT_REF_PROTECTED == "true"`, triadTrue},
		{`$CI_PIPELINE_SOURCE == "schedule" && $A || $CI_PIPELINE_SOURCE == "trigger"`, triadTrue},
		{`$CI_PIPELINE_SOURCE ==`, triadUnknown},
		{`$CI_PIPELINE_SOURCE == "trigger`, triadUnknown},
		{`$CI_PIPELINE_SOURCE =~ /(/`, triadUnknown},
		{`($CI_PIPELINE_SOURCE == "trigger"`, triadUnknown},
		{`$CI_PIPELINE_SOURCE "trigger"`, triadUnknown},
		{`$ == "x"`, triadUnknown},
		{`"trigger" =~ "trigger"`, triadUnknown},
		{`/x/ == /x/`, triadUnknown},
		{``, triadUnknown},
		{`$CI_PIPELINE_SOURCE == "$[[ inputs.stage ]]"`, triadUnknown},
		{`$CI_PIPELINE_SOURCE == "$[[ inputs.stage ]]" || $CI_PIPELINE_SOURCE == "schedule"`, triadUnknown},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, evalWorkflowIf(tc.expr, env), tc.expr)
	}
	assert.Equal(t, triadTrue, triadFalse.not())
	assert.Equal(t, triadFalse, triadTrue.not())
	assert.Equal(t, triadUnknown, triadUnknown.not())
}

func TestEnsureGitLabWebhookFastPath_DryRun(t *testing.T) {
	c := newWebhookFake()
	res := ensureWebhook(t, c, false, true)

	assert.Equal(t, "would-update", res.Action)
	assert.NotEmpty(t, res.Details)
	for _, d := range res.Details {
		assert.True(t, strings.HasPrefix(d, "Would "), d)
	}
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.CreatedProjectHooks)
	assert.Empty(t, c.CreatedSecrets)

	// Dry-run rotation of a provisioned repo plans update and revoke.
	ensureWebhook(t, c, false, false)
	res = ensureWebhook(t, c, true, true)
	assert.Equal(t, "would-update", res.Action)
	assert.Len(t, c.CreatedTriggerTokens, 1)
	assert.Empty(t, c.UpdatedProjectHooks)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	joined := strings.Join(res.Details, "\n")
	assert.Contains(t, joined, "Would rotate")
	assert.Contains(t, joined, "Would update project webhook")
	assert.Contains(t, joined, "Would revoke superseded")

	// Dry-run duplicate cleanup plans deletion only.
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key] = append(c.ProjectHooks[key], forge.ProjectHook{ID: 55, Name: GitLabWebhookName})
	res = ensureWebhook(t, c, false, true)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Would delete duplicate project webhook (ID 55)")
	assert.Empty(t, c.DeletedProjectHookIDs)
}

func TestEnsureGitLabWebhookFastPath_RequiresBaseURL(t *testing.T) {
	c := newWebhookFake()
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, "", webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Empty(t, c.CreatedTriggerTokens)

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, "", webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	assert.True(t, needs)
}

func TestGitLabWebhookNeedsProvisioning_ProbeError(t *testing.T) {
	c := newWebhookFake()
	c.Errors = map[string]error{"GetRepo": errors.New("boom")}
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	assert.True(t, needs)
}

func TestGitLabBaseURLAndNeedsWork(t *testing.T) {
	c := newWebhookFake()
	c.base = " " + webhookTestBase + " "
	assert.Equal(t, webhookTestBase, GitLabBaseURL(c))
	assert.Equal(t, "", GitLabBaseURL(c.FakeClient))

	ctx := context.Background()
	assert.False(t, gitlabWebhookNeedsWork(ctx, nil, webhookTestOwner, webhookTestRepo))
	assert.True(t, gitlabWebhookNeedsWork(ctx, c.FakeClient, webhookTestOwner, webhookTestRepo), "no base URL → let the idempotent step decide")
	assert.True(t, gitlabWebhookNeedsWork(ctx, c, webhookTestOwner, webhookTestRepo))
	ensureWebhook(t, c, false, false)
	assert.False(t, gitlabWebhookNeedsWork(ctx, c, webhookTestOwner, webhookTestRepo))
}

func TestConverge_GitLab_NeedsGitLabWebhook(t *testing.T) {
	full := webhookTestOwner + "/" + webhookTestRepo
	run := func(t *testing.T, c webhookFake) ConvergeResult {
		t.Helper()
		cfg := gitlabConvergeCfg(full)
		cfg.Direct = false
		sc := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(c), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		require.NoError(t, result.Results[0].Error)
		return result.Results[0]
	}

	fc := newFakeClientForBatch(full)
	fc.Repos[0].ID = 42
	c := seedWebhookFake(fc)
	assert.True(t, run(t, c).NeedsGitLabWebhook, "fresh project needs the fast path provisioned")

	ensureWebhook(t, c, false, false)
	assert.False(t, run(t, c).NeedsGitLabWebhook, "provisioned project is converged")

	c.ProjectHooks[full] = nil
	assert.True(t, run(t, c).NeedsGitLabWebhook, "missing webhook must be repaired")
}

func TestEnsureGitLabWebhookFastPath_LeavesUnrelatedTriggerHooksAlone(t *testing.T) {
	c := newWebhookFake()
	key := webhookTestOwner + "/" + webhookTestRepo
	triggerURL := webhookTestBase + "/api/v4/projects/42/ref/main/trigger/pipeline?token="
	c.ProjectHooks[key] = []forge.ProjectHook{
		{ID: 200, Name: "user integration", URL: triggerURL + "user-token"},
		{ID: 201, URL: triggerURL + "another-user-token"},
	}

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	assert.Empty(t, c.UpdatedProjectHooks, "user-owned hooks must not be overwritten")
	assert.Empty(t, c.DeletedProjectHookIDs, "user-owned hooks must not be deleted")
	require.Len(t, c.CreatedProjectHooks, 1)
	require.Len(t, c.hooks(), 3)

	// A second run keeps converging without touching them.
	res = ensureWebhook(t, c, false, false)
	assert.Equal(t, "none", res.Action)
	assert.Empty(t, c.DeletedProjectHookIDs)
	assert.Len(t, c.hooks(), 3)
}

func TestEnsureGitLabWebhookFastPath_UnnamedLegacyHookWithStoredTokenIsOwned(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key][0].Name = "" // GitLab < 17.1 does not persist names
	c.ProjectHooks[key][0].PushEvents = true

	ensureWebhook(t, c, false, false)

	require.Len(t, c.UpdatedProjectHooks, 1, "legacy hook is repaired in place")
	assert.Empty(t, c.CreatedProjectHooks[1:])
	assert.Len(t, c.hooks(), 1)
}

func TestEnsureGitLabWebhookFastPath_UnsafeCredentialMetadataIsReplaced(t *testing.T) {
	unsafe := map[string]forge.SecretProtection{
		"unmasked":     {Exists: true, Protected: true},
		"unprotected":  {Exists: true, Masked: true},
		"file type":    {Exists: true, Masked: true, Protected: true, FileType: true},
		"env scoped":   {Exists: true, Masked: true, Protected: true, EnvironmentScoped: true},
		"not wildcard": {},
	}
	for _, name := range []string{forge.SecretTriggerToken, forge.SecretWebhookSecret} {
		for label, prot := range unsafe {
			t.Run(name+"/"+label, func(t *testing.T) {
				c := newWebhookFake()
				ensureWebhook(t, c, false, false)
				oldValue := c.variable(name)
				oldTrigger := c.variable(forge.SecretTriggerToken)
				c.SecretProtections[webhookTestOwner+"/"+webhookTestRepo+"/"+name] = prot

				needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
				require.NoError(t, err)
				assert.True(t, needs, "metadata drift must be part of convergence")

				res := ensureWebhook(t, c, false, false)

				assert.Equal(t, "update", res.Action)
				assert.NotEqual(t, oldValue, c.variable(name), "an exposed credential must be regenerated, not reused")
				assert.Contains(t, strings.Join(res.Details, "\n"), "Replaced")
				require.NotEmpty(t, c.UpdatedProjectHooks)
				if name == forge.SecretWebhookSecret {
					assert.Equal(t, oldTrigger, c.variable(forge.SecretTriggerToken), "the other credential is untouched")
					assert.Len(t, c.CreatedTriggerTokens, 1)
				} else {
					assert.Len(t, c.CreatedTriggerTokens, 2)
					assert.Len(t, c.triggers(), 1)
				}
				assertNoCredentialLeak(t, c, res)

				needs, err = GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
				require.NoError(t, err)
				assert.False(t, needs)
			})
		}
	}
}

func TestEnsureGitLabWebhookFastPath_UnsafeMetadataDryRunMakesNoChanges(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.SecretProtections[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true}
	secrets := len(c.CreatedSecrets)

	res := ensureWebhook(t, c, false, true)

	assert.Equal(t, "would-update", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Would replace pipeline trigger token")
	assert.Len(t, c.CreatedSecrets, secrets)
	assert.Len(t, c.CreatedTriggerTokens, 1)
}

func TestEnsureGitLabWebhookFastPath_ProtectionLookupError(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.Errors = map[string]error{"GetRepoSecretProtection": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
}

func TestEnsureGitLabWebhookFastPath_RevocationFailureRetriedByOrdinaryConvergence(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldID := c.triggers()[0].ID
	c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
	require.Error(t, err)
	require.Len(t, c.triggers(), 2, "superseded token is still live")

	c.Errors = nil
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "pending revocation is convergence work")

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	assert.Len(t, c.CreatedTriggerTokens, 2, "retry must not mint another token")
	assert.Equal(t, []int64{oldID}, c.RevokedTriggerTokenIDs)
	require.Len(t, c.triggers(), 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+c.variable(forge.SecretTriggerToken))

	needs, err = GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestEnsureGitLabWebhookFastPath_WebhookUpdateFailureRecoversWithoutForcedRotation(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.Errors = map[string]error{"UpdateProjectHook": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
	require.Error(t, err)
	require.Len(t, c.triggers(), 2)

	c.Errors = nil
	ensureWebhook(t, c, false, false)

	require.Len(t, c.triggers(), 1, "every superseded token is revoked")
	assert.Len(t, c.hooks(), 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+c.variable(forge.SecretTriggerToken))
	assert.Equal(t, c.CreatedTriggerTokens[len(c.CreatedTriggerTokens)-1].ID, c.triggers()[0].ID)

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestEnsureGitLabWebhookFastPath_DeferredWhenDispatcherScriptMissing(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	extra := ".gitlab/ci/scripts/extra-helper.sh"
	cases := map[string]func(c webhookFake){
		"install script missing":    func(c webhookFake) { delete(c.FileContents, prefix+gitlabInstallCLIScriptPath) },
		"dispatcher script missing": func(c webhookFake) { delete(c.FileContents, prefix+gitlabDispatcherJobScriptPath) },
		"template-sourced script missing": func(c webhookFake) {
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = append(c.FileContents[prefix+fullsendDispatcherTemplatePath], []byte("    - . \"${CI_PROJECT_DIR:-.}/"+extra+"\"\n")...)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			mutate(c)
			res := ensureWebhook(t, c, false, false)
			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], ".gitlab/ci/scripts/")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
			assert.Empty(t, c.CreatedSecrets)
		})
	}

	t.Run("script read error", func(t *testing.T) {
		c := newWebhookFake()
		c.GetFileContentErrors = map[string]error{prefix + gitlabInstallCLIScriptPath: errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
	})
}

func TestEnsureGitLabWebhookFastPath_RealDispatcherTemplateScriptsRequired(t *testing.T) {
	tpl, err := scaffold.GitLabPerRepoFile(fullsendDispatcherTemplatePath)
	require.NoError(t, err)
	required := requiredDispatcherScripts(tpl)
	assert.Contains(t, required, gitlabInstallCLIScriptPath)
	assert.Contains(t, required, gitlabDispatcherJobScriptPath)

	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for _, script := range required {
		t.Run(script, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = tpl
			for _, s := range required {
				c.FileContents[prefix+s] = []byte("#!/bin/sh\n")
			}
			delete(c.FileContents, prefix+script)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, res.Details[0], script)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
		})
	}
}

func TestEnsureGitLabWebhookFastPath_DeferredWhenSourcedDependencyMissing(t *testing.T) {
	tpl, err := scaffold.GitLabPerRepoFile(fullsendDispatcherTemplatePath)
	require.NoError(t, err)
	dispatcherJob, err := scaffold.GitLabPerRepoFile(gitlabDispatcherJobScriptPath)
	require.NoError(t, err)
	deps := sourcedScripts(dispatcherJob)
	require.Contains(t, deps, gitlabPinCIJobIdentityScriptPath)
	require.Contains(t, deps, gitlabRoleTokenScriptPath)

	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for _, dep := range deps {
		t.Run(dep, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = tpl
			c.FileContents[prefix+gitlabDispatcherJobScriptPath] = dispatcherJob
			for _, s := range requiredDispatcherScripts(tpl) {
				if _, ok := c.FileContents[prefix+s]; !ok {
					c.FileContents[prefix+s] = []byte("#!/bin/sh\n")
				}
			}
			for _, d := range deps {
				c.FileContents[prefix+d] = []byte("#!/bin/sh\n")
			}
			delete(c.FileContents, prefix+dep)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], dep)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
			assert.Empty(t, c.CreatedSecrets)
		})
	}

	t.Run("dependencies present provisions", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[prefix+gitlabDispatcherJobScriptPath] = dispatcherJob
		for _, d := range deps {
			c.FileContents[prefix+d] = []byte("#!/bin/sh\n")
		}
		assert.Equal(t, "update", ensureWebhook(t, c, false, false).Action)
	})

	t.Run("transitive dependency of a dependency", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[prefix+gitlabDispatcherJobScriptPath] = []byte(". \"${CI_PROJECT_DIR:-.}/" + deps[0] + "\"\n")
		c.FileContents[prefix+deps[0]] = []byte("source .gitlab/ci/scripts/nested.sh\n")
		res := ensureWebhook(t, c, false, false)
		assert.Equal(t, "deferred", res.Action)
		assert.Contains(t, res.Details[0], ".gitlab/ci/scripts/nested.sh")
	})

	t.Run("dependency read error", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[prefix+gitlabDispatcherJobScriptPath] = dispatcherJob
		c.GetFileContentErrors = map[string]error{prefix + deps[0]: errors.New("boom")}
		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
	})
}

func TestSourcedScripts(t *testing.T) {
	content := []byte(strings.Join([]string{
		"#!/bin/sh",
		"# . .gitlab/ci/scripts/commented.sh",
		"echo uses .gitlab/ci/scripts/not-sourced.sh",
		". \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/a.sh\"",
		"  source .gitlab/ci/scripts/b.sh",
		". \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/a.sh\"",
	}, "\n"))
	assert.Equal(t, []string{".gitlab/ci/scripts/a.sh", ".gitlab/ci/scripts/b.sh"}, sourcedScripts(content))
	assert.Empty(t, sourcedScripts([]byte("echo hi\n")))
}

func TestEnsureGitLabWebhookFastPath_SecretRepairRetriedAfterFailedHookUpdate(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	oldSecret := c.variable(forge.SecretWebhookSecret)
	// An exposed (unmasked) stored secret must be replaced.
	c.SecretProtections[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretWebhookSecret] = forge.SecretProtection{Exists: true, Protected: true}

	c.Errors = map[string]error{"UpdateProjectHook": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.Equal(t, oldSecret, c.variable(forge.SecretWebhookSecret), "the replacement is not stored before the hook carries it")

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "the unsafe secret is still pending repair")

	c.Errors = nil
	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	assert.NotEqual(t, oldSecret, c.variable(forge.SecretWebhookSecret))
	require.NotEmpty(t, c.UpdatedProjectHooks)
	last := c.UpdatedProjectHooks[len(c.UpdatedProjectHooks)-1]
	assert.Equal(t, c.variable(forge.SecretWebhookSecret), last.Token, "the hook carries the stored secret")
	needs, err = GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestEnsureGitLabWebhookFastPath_SecretStoreFailureAfterHookRetried(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	delete(c.VariableValues, webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretWebhookSecret)

	c.Errors = map[string]error{"CreateRepoSecret": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)

	c.Errors = nil
	ensureWebhook(t, c, false, false)
	last := c.UpdatedProjectHooks[len(c.UpdatedProjectHooks)-1]
	assert.Equal(t, c.variable(forge.SecretWebhookSecret), last.Token)
}

func TestEnsureGitLabWebhookFastPath_ErrorsRedactEchoedCredentials(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, e echoFake) error {
		t.Helper()
		_, err := EnsureGitLabWebhookFastPath(ctx, e, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
		return err
	}

	t.Run("webhook creation echoes URL and secret", func(t *testing.T) {
		e := echoFake{webhookFake: newWebhookFake(), echoHook: true}
		err := run(t, e)
		msg := err.Error()
		assert.Contains(t, msg, credentialRedacted)
		assert.NotContains(t, msg, e.variable(forge.SecretTriggerToken))
		// The webhook secret is stored only after the hook exists, so it
		// is not yet readable here; the echoed hook.Token must be redacted.
		assert.Contains(t, msg, "url="+credentialRedacted+" token="+credentialRedacted)
		assert.NotContains(t, msg, webhookTestBase)
		assert.NotContains(t, msg, "glptt-fake")
	})

	t.Run("secret write echoes value", func(t *testing.T) {
		e := echoFake{webhookFake: newWebhookFake(), echoSecret: true}
		err := run(t, e)
		assert.Contains(t, err.Error(), credentialRedacted)
		for _, rec := range e.CreatedSecrets {
			assert.NotContains(t, err.Error(), rec.Value)
		}
	})

	t.Run("existing credentials are redacted too", func(t *testing.T) {
		e := echoFake{webhookFake: newWebhookFake()}
		ensureWebhook(t, e.webhookFake, false, false)
		oldSecret := e.variable(forge.SecretWebhookSecret)
		e.SecretProtections[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.SecretWebhookSecret] = forge.SecretProtection{Exists: true, Masked: true}
		e.echoSecret = true
		err := run(t, e)
		assert.NotContains(t, err.Error(), oldSecret)
	})
}

func TestEnsureGitLabWebhookFastPath_UnsafeTokenRepairKeepsUnnamedHook(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key][0].Name = ""                                                                           // GitLab < 17.1 does not persist names
	c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true} // unprotected

	ensureWebhook(t, c, false, false)

	require.Len(t, c.hooks(), 1, "the unnamed managed hook is repaired, not stranded beside a replacement")
	assert.Len(t, c.CreatedProjectHooks, 1)
	assert.Empty(t, c.DeletedProjectHookIDs)
	assert.Contains(t, c.hooks()[0].URL, "token="+c.variable(forge.SecretTriggerToken))
	require.Len(t, c.triggers(), 1)
}

func TestEnsureGitLabWebhookFastPath_FailedRotationKeepsUnnamedHook(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key][0].Name = ""
	c.Errors = map[string]error{"UpdateProjectHook": errors.New("boom")}
	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
	require.Error(t, err)

	// The stored token now differs from the one in the hook.
	c.Errors = nil
	ensureWebhook(t, c, false, false)

	require.Len(t, c.hooks(), 1)
	assert.Len(t, c.CreatedProjectHooks, 1, "no replacement hook is created")
	assert.Empty(t, c.DeletedProjectHookIDs)
	require.Len(t, c.triggers(), 1)
	assert.Contains(t, c.hooks()[0].URL, "token="+c.variable(forge.SecretTriggerToken))
}

func TestEnsureGitLabWebhookFastPath_StoredTokenChangeRotatesInsteadOfTrustingIt(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.VariableValues[key+"/"+forge.SecretTriggerToken] = "unrelated-bearer-value"

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "a stored token the managed hook does not use is drift")

	ensureWebhook(t, c, false, false)

	require.Len(t, c.CreatedTriggerTokens, 2, "a new trigger is minted")
	require.Len(t, c.triggers(), 1)
	require.Len(t, c.hooks(), 1)
	assert.NotContains(t, c.hooks()[0].URL, "unrelated-bearer-value")
	assert.Equal(t, c.CreatedTriggerTokens[1].Token, c.variable(forge.SecretTriggerToken))
	assert.Contains(t, c.hooks()[0].URL, "token="+c.CreatedTriggerTokens[1].Token)
}

func TestTeardownGitLabWebhookFastPath_EnvironmentScopedTokenDoesNotEstablishOwnership(t *testing.T) {
	for name, prot := range map[string]forge.SecretProtection{
		"no wildcard variable":    {},
		"environment-scoped flag": {Exists: true, Masked: true, Protected: true, EnvironmentScoped: true},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			key := webhookTestOwner + "/" + webhookTestRepo
			c.VariableValues[key+"/"+forge.SecretTriggerToken] = "env-scoped-token"
			c.SecretProtections[key+"/"+forge.SecretTriggerToken] = prot
			c.ProjectHooks[key] = []forge.ProjectHook{{ID: 700, URL: GitLabWebhookTriggerURL(webhookTestBase, 42, "main", "env-scoped-token")}}

			res, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)

			require.NoError(t, err)
			assert.Equal(t, 0, res.HooksDeleted)
			assert.Empty(t, c.DeletedProjectHookIDs)
			require.Len(t, c.hooks(), 1, "the unnamed user hook survives")
		})
	}
}

func TestTeardownGitLabWebhookFastPath_UnnamedHookWithFullsendDescriptionIsOwned(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key][0].Name = ""
	c.VariableValues[key+"/"+forge.SecretTriggerToken] = "stale-stored-value" // drifted from the hook

	res, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)

	require.NoError(t, err)
	assert.Equal(t, 1, res.HooksDeleted)
	assert.Empty(t, c.hooks())
}

func TestWebhookErrorsBeforeCredentialsAreKnownWithholdServerText(t *testing.T) {
	const leaked = "leaked-credential-value"
	ctx := context.Background()
	for _, step := range []string{"ListRepoVariables", "ListProjectHooks", "ListPipelineTriggerTokens", "CreatePipelineTriggerToken"} {
		t.Run("ensure/"+step, func(t *testing.T) {
			c := newWebhookFake()
			cause := errors.New("server said " + leaked)
			c.Errors = map[string]error{step: cause}
			_, err := EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), leaked)
			assert.ErrorIs(t, err, cause)
		})
	}
	for _, step := range []string{"ListRepoVariables", "ListProjectHooks", "ListPipelineTriggerTokens"} {
		t.Run("teardown/"+step, func(t *testing.T) {
			c := newWebhookFake()
			cause := errors.New("server said " + leaked)
			c.Errors = map[string]error{step: cause}
			_, err := TeardownGitLabWebhookFastPath(ctx, c, webhookTestOwner, webhookTestRepo)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), leaked)
			assert.ErrorIs(t, err, cause)
		})
	}
}

// A hook left by an interrupted rotation can carry a still-valid token that
// differs from the stored one; list errors echoing it must not be printed.
func TestWebhookListErrorsDoNotLeakDriftedHookToken(t *testing.T) {
	const oldToken = "drifted-hook-token-abc"
	ctx := context.Background()
	key := webhookTestOwner + "/" + webhookTestRepo
	hookURL := GitLabWebhookTriggerURL(webhookTestBase, 42, "main", oldToken)
	for _, step := range []string{"ListProjectHooks", "ListPipelineTriggerTokens"} {
		cause := errors.New("proxy error echoing " + hookURL + " token " + oldToken)
		for _, op := range []string{"probe", "ensure", "teardown"} {
			t.Run(op+"/"+step, func(t *testing.T) {
				c := newWebhookFake()
				c.ProjectHooks[key] = []forge.ProjectHook{{ID: 800, Name: GitLabWebhookName, URL: hookURL}}
				c.Errors = map[string]error{step: cause}
				var err error
				switch op {
				case "probe":
					_, err = GitLabWebhookNeedsProvisioning(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo)
				case "ensure":
					_, err = EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
				default:
					_, err = TeardownGitLabWebhookFastPath(ctx, c, webhookTestOwner, webhookTestRepo)
				}
				require.Error(t, err)
				assert.NotContains(t, err.Error(), oldToken)
				assert.NotContains(t, err.Error(), hookURL)
				assert.ErrorIs(t, err, cause)
			})
		}
	}
}

// If the active trigger recorded in the webhook description is revoked and
// only a superseded one remains, the stored token is dead; convergence must
// replace it instead of reporting the fast path provisioned.
func TestEnsureGitLabWebhookFastPath_RevokedActiveTriggerIsRepaired(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	oldToken := c.variable(forge.SecretTriggerToken)
	activeID := c.triggers()[0].ID

	// Rotation left a superseded trigger; the active one was then revoked.
	key := webhookTestOwner + "/" + webhookTestRepo
	c.PipelineTriggerTokens[key] = []forge.PipelineTriggerToken{{ID: activeID + 100, Description: GitLabWebhookTriggerDescription, OwnerID: 1001}}

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "a webhook whose recorded active trigger is gone is not provisioned")

	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "update", res.Action)
	assert.NotEqual(t, oldToken, c.variable(forge.SecretTriggerToken))
	assert.Contains(t, c.RevokedTriggerTokenIDs, activeID+100)

	needs, err = GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestWebhookErrorsRedactBareTokenFromExistingHookURL(t *testing.T) {
	const oldToken = "old-bearer-token-xyz"
	ctx := context.Background()
	key := webhookTestOwner + "/" + webhookTestRepo
	seedHook := func(c webhookFake) {
		c.ProjectHooks[key] = []forge.ProjectHook{{ID: 800, Name: GitLabWebhookName, URL: GitLabWebhookTriggerURL(webhookTestBase, 42, "main", oldToken)}}
	}

	t.Run("ensure", func(t *testing.T) {
		c := newWebhookFake()
		seedHook(c)
		c.Errors = map[string]error{"UpdateProjectHook": errors.New("rejected bearer " + oldToken)}
		_, err := EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), oldToken)
		assert.Contains(t, err.Error(), credentialRedacted)
	})

	t.Run("teardown", func(t *testing.T) {
		c := newWebhookFake()
		seedHook(c)
		c.Errors = map[string]error{"DeleteProjectHook": errors.New("rejected bearer " + oldToken)}
		_, err := TeardownGitLabWebhookFastPath(ctx, c, webhookTestOwner, webhookTestRepo)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), oldToken)
		assert.Contains(t, err.Error(), credentialRedacted)
	})
}

func TestCredentialRedactor(t *testing.T) {
	r := &credentialRedactor{}
	r.add("", "a b/c")
	assert.NoError(t, r.redact(nil))

	plain := errors.New("nothing sensitive")
	assert.Same(t, plain, r.redact(plain))

	base := forge.ErrNotFound
	got := r.redact(fmt.Errorf("echo a b/c, a+b%%2Fc and a%%20b%%2Fc: %w", base))
	assert.NotContains(t, got.Error(), "a b/c")
	assert.NotContains(t, got.Error(), "a+b%2Fc")
	assert.NotContains(t, got.Error(), "a%20b%2Fc")
	assert.ErrorIs(t, got, forge.ErrNotFound, "redaction keeps the error chain")
}

func TestHookTriggerToken(t *testing.T) {
	prefix := webhookTestBase + "/api/v4/projects/42/ref/main/trigger/pipeline"
	tok, ok := hookTriggerToken(prefix+"?token=abc", webhookTestBase, 42)
	assert.True(t, ok)
	assert.Equal(t, "abc", tok)
	for _, u := range []string{
		prefix + "?token=",
		prefix,
		prefix + "?token=%zz",
		webhookTestBase + "/api/v4/projects/43/ref/main/trigger/pipeline?token=abc",
		"https://other.example.com/hook?token=abc",
	} {
		_, ok := hookTriggerToken(u, webhookTestBase, 42)
		assert.False(t, ok, u)
	}
}

func TestTeardownGitLabWebhookFastPath_RemovesOwnedHooksAndManagedTriggers(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	stored := c.variable(forge.SecretTriggerToken)

	// An unnamed legacy hook using the stored token is owned; hooks that
	// belong to someone else (different name, or unnamed with another
	// token) and an unrelated trigger token must survive.
	c.ProjectHooks[key] = append(c.ProjectHooks[key],
		forge.ProjectHook{ID: 501, URL: GitLabWebhookTriggerURL(webhookTestBase, 42, "main", stored)},
		forge.ProjectHook{ID: 502, Name: "someone else", URL: GitLabWebhookTriggerURL(webhookTestBase, 42, "main", stored)},
		forge.ProjectHook{ID: 503, URL: GitLabWebhookTriggerURL(webhookTestBase, 42, "main", "other-token")},
	)
	c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{ID: 900, Description: "user token"})
	managedID := c.triggers()[0].ID

	res, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)

	require.NoError(t, err)
	assert.Equal(t, 2, res.HooksDeleted)
	assert.Equal(t, 1, res.TriggersRevoked)
	assert.Len(t, c.DeletedProjectHookIDs, 2)
	assert.Contains(t, c.DeletedProjectHookIDs, int64(501))
	assert.NotContains(t, c.DeletedProjectHookIDs, int64(502))
	assert.NotContains(t, c.DeletedProjectHookIDs, int64(503))
	assert.Equal(t, []int64{managedID}, c.RevokedTriggerTokenIDs)
	remaining := []int64{}
	for _, h := range c.hooks() {
		remaining = append(remaining, h.ID)
	}
	assert.ElementsMatch(t, []int64{502, 503}, remaining)
}

func TestTeardownGitLabWebhookFastPath_NothingProvisionedIsNoOp(t *testing.T) {
	c := newWebhookFake()
	res, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.Equal(t, GitLabWebhookTeardownResult{}, res)
}

func TestTeardownGitLabWebhookFastPath_ErrorsAreReportedRedactedAndRetryConverges(t *testing.T) {
	for _, step := range []string{"ListRepoVariables", "ListProjectHooks", "ListPipelineTriggerTokens", "DeleteProjectHook", "RevokePipelineTriggerToken"} {
		t.Run(step, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			stored := c.variable(forge.SecretTriggerToken)

			c.Errors = map[string]error{step: errors.New("boom " + stored)}
			_, err := TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)
			require.Error(t, err)
			if strings.HasPrefix(step, "List") {
				// Server text is withheld: credentials in the listed state
				// are unknown, so the error cannot be redacted reliably.
				assert.NotContains(t, err.Error(), "boom")
			} else {
				assert.Contains(t, err.Error(), "boom")
			}
			assert.NotContains(t, err.Error(), stored, "credential values never reach errors")

			// A retry after the failure clears removes whatever remains.
			c.Errors = nil
			_, err = TeardownGitLabWebhookFastPath(context.Background(), c, webhookTestOwner, webhookTestRepo)
			require.NoError(t, err)
			assert.Empty(t, c.hooks())
			assert.Empty(t, c.triggers())
		})
	}
}

func TestTriggerURLToken(t *testing.T) {
	assert.Equal(t, "abc", triggerURLToken(GitLabWebhookTriggerURL(webhookTestBase, 7, "main", "abc")))
	assert.Empty(t, triggerURLToken("https://example.com/hook"))
	assert.Empty(t, triggerURLToken("https://x/trigger/pipeline?token=%zz"))
}

func TestUninstall_GitLabRemovesWebhookFastPathAndCredentials(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")
	client.ProjectHooks = map[string][]forge.ProjectHook{"acme/api": {{ID: 7, Name: GitLabWebhookName}, {ID: 8, Name: "mine"}}}
	client.PipelineTriggerTokens = map[string][]forge.PipelineTriggerToken{"acme/api": {{ID: 31, Description: GitLabWebhookTriggerDescription}}}

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)

	require.NoError(t, err)
	r := results[0]
	require.NoError(t, r.Error)
	assert.True(t, r.Success)
	assert.Equal(t, []int64{7}, client.DeletedProjectHookIDs)
	assert.Equal(t, []int64{31}, client.RevokedTriggerTokenIDs)
	assert.GreaterOrEqual(t, r.TokensRevoked, 1)
	deleted := map[string]bool{}
	for _, rec := range client.DeletedSecrets {
		deleted[rec.Name] = true
	}
	assert.True(t, deleted[forge.SecretTriggerToken])
	assert.True(t, deleted[forge.SecretWebhookSecret])
}

func TestUninstall_GitLabWebhookTeardownFailureKeepsScaffoldAndReportsFailure(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")
	client.ProjectHooks = map[string][]forge.ProjectHook{"acme/api": {{ID: 7, Name: GitLabWebhookName}}}
	client.Errors["DeleteProjectHook"] = errors.New("forbidden")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)

	require.NoError(t, err)
	r := results[0]
	require.Error(t, r.Error)
	assert.False(t, r.Success, "the manifest entry must be retained so uninstall can be retried")
	assert.False(t, r.WorkflowDeleted, "the scaffold is not removed while the webhook is still configured")
	assert.Empty(t, client.CommittedFiles)

	// A retry after the failure clears converges.
	delete(client.Errors, "DeleteProjectHook")
	results, err = Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)
	require.NoError(t, err)
	assert.True(t, results[0].Success, "%v", results[0].Error)
}

// echoDeleteSecretClient fails DeleteRepoSecret for one secret with an error
// that echoes the supplied text, as a misbehaving API or proxy could.
type echoDeleteSecretClient struct {
	*forge.FakeClient
	failName string
	echo     string
}

func (c echoDeleteSecretClient) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	if name == c.failName {
		return errors.New("500 from proxy: " + c.echo)
	}
	return c.FakeClient.DeleteRepoSecret(ctx, owner, repo, name)
}

func TestUninstall_GitLabWebhookCredentialDeleteErrorsWithholdServerText(t *testing.T) {
	for _, name := range []string{forge.SecretTriggerToken, forge.SecretWebhookSecret} {
		t.Run(name, func(t *testing.T) {
			const credential = "echoed-credential-value-123"
			fake := newInstalledFakeGitLabClient("acme/api")
			fake.VariableValues["acme/api/"+name] = credential
			client := echoDeleteSecretClient{FakeClient: fake, failName: name, echo: credential}

			var progressLines []string
			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testGitLabManifest("acme/api"),
				Repos:          []string{"acme/api"},
				Direct:         true,
				MaxConcurrency: 4,
			}, newTestClientFactory(client), uninstallCommitFn(fake), func(repo, phase, message string) {
				progressLines = append(progressLines, message)
			})

			require.NoError(t, err)
			require.Error(t, results[0].Error)
			assert.NotContains(t, results[0].Error.Error(), credential)
			assert.Contains(t, results[0].Error.Error(), name)
			for _, line := range progressLines {
				assert.NotContains(t, line, credential)
			}
		})
	}
}

// Readiness accepts equivalent YAML structure: aliased stage lists that name
// dispatch, workflow rules inherited through a merge key (with explicit keys
// overriding), and a wrapper whose dispatcher include admits triggers.
func TestEnsureGitLabWebhookFastPath_YAMLStructureEquivalentsAdmitted(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	inc := "include:\n  - local: '" + fullsendPipelineInclude + "'\n"
	cases := map[string]string{
		"aliased stages with dispatch":          inc + ".s: &s [dispatch, poll, agent]\nstages: *s\n",
		"stages entry alias to scalar":          inc + ".d: &d dispatch\nstages: [*d, poll, agent]\n",
		"merge-inherited admitting rules":       inc + "stages: [dispatch]\n.base: &base\n  rules:\n    - when: always\nworkflow:\n  <<: *base\n",
		"explicit rules override a merged deny": inc + "stages: [dispatch]\n.base: &base\n  rules:\n    - when: never\nworkflow:\n  <<: *base\n  rules:\n    - when: always\n",
	}
	for name, ci := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(ci)
			assertPassesRootAdmission(t, c)
		})
	}

	t.Run("wrapper dispatcher include admits triggers", func(t *testing.T) {
		wrapper := "spec:\n  inputs:\n    stage:\n      default: ''\n---\ninclude:\n  - local: '" + fullsendDispatcherTemplatePath + "'\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n"
		assert.Empty(t, gitlabWrapperDispatcherProblem([]byte(wrapper), "main"))
	})
}

// A provisioned fast path whose trigger-safety invariant later drifts must
// not leave the bearer credential live, whether or not the repository is
// otherwise ready.
func TestEnsureGitLabWebhookFastPath_SafetyDriftRevokesManagedCredentials(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	cases := map[string]func(c webhookFake){
		"override role weakened": func(c webhookFake) {
			c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = "developer"
		},
		"override role weakened while readiness also fails": func(c webhookFake) {
			c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = "developer"
			delete(c.FileContents, prefix+fullsendPipelineInclude)
		},
		"override role unverifiable": func(c webhookFake) {
			c.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
		},
		"alternate protected ref added": func(c webhookFake) {
			c.ProtectedBranches[prefix+"release/1.0"] = true
		},
		"alternate protected wildcard rule added": func(c webhookFake) {
			c.ProtectedBranches[prefix+"release/*"] = true
		},
		"protected branches unlistable": func(c webhookFake) {
			c.Errors = map[string]error{"ListProtectedBranches": errors.New("boom")}
		},
		"protected tag added": func(c webhookFake) {
			c.ProtectedTags[prefix+"v1.0.0"] = true
		},
		"protected tag wildcard added": func(c webhookFake) {
			c.ProtectedTags[prefix+"v*"] = true
		},
		"protected tags unlistable": func(c webhookFake) {
			c.Errors = map[string]error{"ListProtectedTags": errors.New("boom")}
		},
	}
	for name, drift := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			require.Len(t, c.hooks(), 1)
			drift(c)

			needs, _ := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
			assert.True(t, needs, "drift on a live credential is convergence work")

			dry, _ := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)
			assert.Len(t, c.triggers(), 1, "dry run must not revoke")
			assert.Len(t, c.hooks(), 1, "dry run must not delete")
			assert.NotEqual(t, "none", dry.Action)

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			if name == "override role unverifiable" || name == "protected branches unlistable" || name == "protected tags unlistable" {
				require.Error(t, err, "an unverifiable invariant is reported")
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, c.triggers(), "managed trigger token is revoked")
			assert.Empty(t, c.hooks(), "owned webhook is deleted")
			assert.Len(t, c.CreatedTriggerTokens, 1, "nothing new is minted")
			assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
			assertNoCredentialLeak(t, c, res)
		})
	}
}

// Safety reconciliation only removes what Fullsend owns.
func TestEnsureGitLabWebhookFastPath_SafetyRevocationKeepsUserHooks(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectHooks[key] = append(c.ProjectHooks[key], forge.ProjectHook{ID: 900, Name: "user hook", URL: "https://ci.example.com/hook"})
	c.PipelineVarOverrideRoles[key] = "maintainer"

	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.NoError(t, err)

	require.Len(t, c.hooks(), 1)
	assert.Equal(t, "user hook", c.hooks()[0].Name)
}

// With no managed credentials there is nothing to revoke and a violated
// invariant is just a deferral.
func TestEnsureGitLabWebhookFastPath_SafetyViolationWithoutCredentialsIsDeferral(t *testing.T) {
	c := newWebhookFake()
	c.ProtectedBranches[webhookTestOwner+"/"+webhookTestRepo+"/release"] = true
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs)

	res := ensureWebhook(t, c, false, false)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, res.Details, 1)
	assert.Contains(t, res.Details[0], "release")
	assert.Empty(t, c.CreatedTriggerTokens)
}

// The exported probe redacts credential echoes from protection lookups,
// including a credential that has not been looked up yet.
func TestGitLabWebhookNeedsProvisioning_ProtectionErrorRedactsBothCredentials(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	tok, sec := c.variable(forge.SecretTriggerToken), c.variable(forge.SecretWebhookSecret)
	require.NotEmpty(t, tok)
	require.NotEmpty(t, sec)
	c.Errors = map[string]error{"GetRepoSecretProtection": errors.New("500: upstream said token=" + tok + " secret=" + sec)}

	_, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)
	assert.NotContains(t, err.Error(), sec)
	assert.Contains(t, err.Error(), credentialRedacted)
}

// Protected tags are refs a project-wide trigger token can target, so any
// protected-tag rule, exact or wildcard, defers provisioning.
func TestEnsureGitLabWebhookFastPath_ProtectedTagsDeferProvisioning(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for _, pattern := range []string{"v1.0.0", "v*"} {
		t.Run(pattern, func(t *testing.T) {
			c := newWebhookFake()
			c.ProtectedTags[prefix+pattern] = true

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], "protected-tag")
			assert.Contains(t, res.Details[0], pattern)
			assert.Empty(t, c.CreatedTriggerTokens)
		})
	}

	t.Run("listing failure is reported and nothing is provisioned", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors = map[string]error{"ListProtectedTags": errors.New("boom")}

		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing protected tags")
		assert.Empty(t, c.CreatedTriggerTokens)
	})
}

// ReconcileGitLabWebhookSafety revokes managed credentials when an invariant
// is weakened and never provisions.
func TestReconcileGitLabWebhookSafety(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo

	t.Run("revokes after weakened restriction", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		require.Len(t, c.hooks(), 1)
		c.PipelineVarOverrideRoles[key] = "developer"

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
		assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
	})

	t.Run("revokes after emptied restriction", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.PipelineVarOverrideRoles[key] = ""

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("dry run changes nothing", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.PipelineVarOverrideRoles[key] = "developer"

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, true)

		require.NoError(t, err)
		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
	})

	t.Run("safe repo is untouched and nothing is provisioned", func(t *testing.T) {
		c := newWebhookFake()

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Equal(t, "none", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Empty(t, c.CreatedProjectHooks)
	})

	t.Run("unverifiable restriction revokes and reports", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.Empty(t, c.triggers())
	})
}

// webhookRootWithoutWorkflowRules is the shipped typed root with its
// workflow:rules removed, so it inherits whatever workflow an included file
// defines. The shipped root ends with its workflow block.
func webhookRootWithoutWorkflowRules(t *testing.T, c webhookFake) []byte {
	t.Helper()
	root := string(c.FileContents[webhookTestOwner+"/"+webhookTestRepo+"/.gitlab-ci.yml"])
	idx := strings.Index(root, "\nworkflow:")
	require.GreaterOrEqual(t, idx, 0, "seeded root must define workflow")
	return []byte(root[:idx] + "\nworkflow:\n  auto_cancel:\n    on_new_commit: none\n")
}

// Admission is evaluated on the effective workflow: a typed root without
// its own workflow:rules inherits them from the wrapper or the dispatcher
// template, and GitLab rejects trigger pipelines those rules refuse.
func TestEnsureGitLabWebhookFastPath_InheritedWorkflowRules(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	const rejecting = "\nworkflow:\n  rules:\n    - when: never\n"
	const admitting = "\nworkflow:\n  rules:\n    - when: always\n"
	const unevaluable = "\nworkflow:\n  rules: always\n"

	cases := map[string]struct {
		rootRules     bool
		wrapperExtra  string
		templateExtra string
		deferred      string
	}{
		"wrapper rejects and root has no rules":         {wrapperExtra: rejecting, deferred: "workflow rule 1 rejects them first"},
		"template rejects and root has no rules":        {templateExtra: rejecting, deferred: "workflow rule 1 rejects them first"},
		"wrapper rules cannot be evaluated":             {wrapperExtra: unevaluable, deferred: "cannot be evaluated"},
		"wrapper admits and root has no rules":          {wrapperExtra: admitting},
		"wrapper overrides a rejecting template":        {wrapperExtra: admitting, templateExtra: rejecting},
		"root rules override a rejecting wrapper":       {rootRules: true, wrapperExtra: rejecting},
		"root rules override a rejecting template":      {rootRules: true, templateExtra: rejecting},
		"no workflow rules anywhere is not a rejection": {},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			if !tc.rootRules {
				c.FileContents[prefix+".gitlab-ci.yml"] = webhookRootWithoutWorkflowRules(t, c)
			}
			c.FileContents[prefix+fullsendPipelineInclude] = append(c.FileContents[prefix+fullsendPipelineInclude], tc.wrapperExtra...)
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = append(c.FileContents[prefix+fullsendDispatcherTemplatePath], tc.templateExtra...)

			res := ensureWebhook(t, c, false, false)

			if tc.deferred == "" {
				assert.Equal(t, "update", res.Action)
				assert.NotEmpty(t, c.CreatedTriggerTokens)
				return
			}
			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], tc.deferred)
			assert.Contains(t, res.Details[0], "effective workflow rules from")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
		})
	}
}

// The probe's initial project lookup runs before any credential is
// registered with a redactor, so a server error echoing a stored credential
// must not reach the error returned by GitLabWebhookNeedsProvisioning.
func TestGitLabWebhookNeedsProvisioning_ProjectLookupErrorWithholdsServerText(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	token := c.variable(forge.SecretTriggerToken)
	secret := c.variable(forge.SecretWebhookSecret)
	require.NotEmpty(t, token)
	require.NotEmpty(t, secret)
	lookupErr := errors.New("502 bad gateway: token=" + token + " secret=" + secret)
	c.Errors = map[string]error{"GetRepo": lookupErr}

	_, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

	require.Error(t, err)
	assert.ErrorIs(t, err, lookupErr)
	assert.ErrorContains(t, err, "reading project")
	assert.ErrorContains(t, err, "server error text withheld")
	assert.NotContains(t, err.Error(), token)
	assert.NotContains(t, err.Error(), secret)
}

// Readiness failures that occur after safety inspection succeeded are
// reported before the outer redactor knows the stored credentials, so a
// server error echoing one must not reach the returned error.
func TestEnsureGitLabWebhookFastPath_ReadinessErrorsWithholdServerText(t *testing.T) {
	for _, op := range []string{"IsProtectedBranch"} {
		t.Run(op, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			token := c.variable(forge.SecretTriggerToken)
			secret := c.variable(forge.SecretWebhookSecret)
			require.NotEmpty(t, token)
			require.NotEmpty(t, secret)
			// Safety inspection passes, then a later readiness request echoes
			// the credentials.
			c.Errors = map[string]error{op: errors.New("502 bad gateway: token=" + token + " secret=" + secret)}

			_, err := ensureWebhookRaw(c)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), token)
			assert.NotContains(t, err.Error(), secret)
			assert.ErrorContains(t, err, "server error text withheld")

			_, needsErr := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
			require.Error(t, needsErr)
			assert.NotContains(t, needsErr.Error(), token)
			assert.NotContains(t, needsErr.Error(), secret)
		})
	}
}
