package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const triggerJobsPrefix = webhookTestOwner + "/" + webhookTestRepo + "/"

// With the complete typed contract committed, an unguarded user job (or an
// include that cannot be shown to exclude trigger pipelines) would run with
// the protected credentials in a trigger pipeline. Fast-path provisioning
// defers, and an existing fast path is revoked.
func TestEnsureGitLabWebhookFastPath_UnguardedExtraWorkIsNotProvisioned(t *testing.T) {
	cases := map[string]string{
		"unguarded deployment job":         "\ndeploy:\n  stage: poll\n  script:\n    - ./deploy.sh\n",
		"job admitting the default branch": "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH'\n",
		"job with unevaluable rules":       "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - changes: [src/**]\n",
		"job with interpolated rule input": "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"$[[ inputs.stage ]]\"'\n",
		"job using extends":                "\n.base:\n  script: [./deploy.sh]\ndeploy:\n  extends: .base\n",
		"job using only":                   "\ndeploy:\n  script: [./deploy.sh]\n  only: [main]\n",
		"remote include":                   "\ninclude:\n  - remote: 'https://example.com/ci.yml'\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			root := string(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"])
			if strings.HasPrefix(extra, "\ninclude:") {
				// A second include entry joins the existing list.
				root = strings.Replace(root, "include:\n", "include:\n  - remote: 'https://example.com/ci.yml'\n", 1)
			} else {
				root += extra
			}
			c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = []byte(root)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			require.NotEmpty(t, res.Details)
			assert.Contains(t, res.Details[0], "trigger pipeline")
			assert.Contains(t, res.Details[0], "Fullsend does not edit user-owned jobs")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
		})
	}

	t.Run("include with interpolated rule input is not provisioned", func(t *testing.T) {
		c := newWebhookFake()
		root := string(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"])
		root = strings.Replace(root, "include:\n", "include:\n  - local: extra.yml\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"$[[ inputs.stage ]]\"'\n", 1)
		c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = []byte(root)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
	})

	t.Run("existing fast path is revoked when an unguarded job appears", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"], []byte("\ndeploy:\n  script: [./deploy.sh]\n")...)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
		assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
	})

	t.Run("extra work excluded from trigger pipelines is allowed", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"],
			[]byte("\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - when: on_success\nlint:\n  script: [./lint.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\n")...)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "update", res.Action)
		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
	})
}

// The committed dispatcher template must define a runnable dispatcher job in
// the dispatch stage that admits trigger pipelines; script paths alone are
// not enough, so an unmerged repair of a broken job still defers.
func TestEnsureGitLabWebhookFastPath_DispatcherJobMustBeRunnable(t *testing.T) {
	const jobName = "\"fullsend webhook dispatcher\""
	cases := map[string]string{
		"empty job":            jobName + ": {}\n",
		"missing job":          "other:\n  script: [true]\n",
		"unparseable":          "job: [unterminated\n",
		"empty script":         jobName + ":\n  stage: dispatch\n  script: []\n",
		"wrong stage":          jobName + ":\n  stage: poll\n  script: [true]\n",
		"schedule-only":        jobName + ":\n  stage: dispatch\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n",
		"trigger denied first": jobName + ":\n  stage: dispatch\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - when: always\n",
		"extends":              jobName + ":\n  extends: .base\n  stage: dispatch\n  script: [true]\n",
	}
	for name, template := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[triggerJobsPrefix+fullsendDispatcherTemplatePath] = []byte(template)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			require.Len(t, res.Details, 1)
			assert.Contains(t, res.Details[0], "dispatcher")
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
		})
	}

	t.Run("extra job in the dispatcher template is not allowed", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[triggerJobsPrefix+fullsendDispatcherTemplatePath] = append(c.FileContents[triggerJobsPrefix+fullsendDispatcherTemplatePath], []byte("\nextra:\n  script: [true]\n")...)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedProjectHooks)
	})
}

// A stored trigger token that is not masked, protected, and wildcard-scoped
// may have been exposed, so the managed triggers are revoked before any
// replacement step that could fail.
func TestEnsureGitLabWebhookFastPath_UnsafeTokenRevokedBeforeReplacement(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	for failing, label := range map[string]string{
		"UpdateProjectHook": "webhook update failure",
		"CreateRepoSecret":  "replacement storage failure",
	} {
		t.Run(label, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			oldID := c.triggers()[0].ID
			c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true} // unprotected
			c.Errors = map[string]error{failing: errors.New("boom")}

			_, err := ensureWebhookRaw(c)

			require.Error(t, err)
			for _, tr := range c.triggers() {
				assert.NotEqual(t, oldID, tr.ID, "the potentially exposed trigger must not outlive a failed replacement")
			}
		})
	}

	t.Run("revocation failure stops the replacement", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		before := len(c.CreatedTriggerTokens)
		c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true}
		c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("boom")}

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.Len(t, c.CreatedTriggerTokens, before, "no replacement is minted while the exposed token stays live")
	})

	t.Run("ordinary rotation still revokes after the webhook is updated", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"UpdateProjectHook": errors.New("boom")}

		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

		require.Error(t, err)
		assert.Len(t, c.triggers(), 2, "routine rotation keeps the old token until the new one is wired in")
	})
}
