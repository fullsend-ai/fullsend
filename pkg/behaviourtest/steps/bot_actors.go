package steps

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Bot test actors, as named in step text. The role bots act through the
// GitHub App of their Fullsend role: a workflow pushed to a temporary
// branch of the leased repository exchanges its GitHub Actions OIDC token
// at the mint (POST /v1/token, role at the write level, the leased
// repository only) and calls the GitHub API with the minted installation
// token. The leased repository is on the mint's workflow-host list and the
// suite's mints accept any workflow basename, so no reusable host
// repository is needed. The e2e bot is the suite's own installation
// identity behind World.SCM, which no role of the app set uses.
const (
	actorReviewBot = "review bot"
	actorCoderBot  = "coder bot"
	actorTriageBot = "triage bot"
	actorE2EBot    = "e2e bot"
)

// botActorRoles maps each role bot to the role whose App it acts as.
var botActorRoles = map[string]string{
	actorReviewBot: "review",
	actorCoderBot:  "coder",
	actorTriageBot: "triage",
}

// botActorAlternation matches any bot test actor name in a step pattern.
const botActorAlternation = actorReviewBot + "|" + actorCoderBot + "|" + actorTriageBot + "|" + actorE2EBot

// Bot actions a pushed workflow performs.
const (
	botActionComment = "comment"
	botActionLabel   = "label"
)

// botActorValueRe bounds a comment body or label interpolated into the
// pushed workflow: no quotes, backslashes, expression braces or newlines.
var botActorValueRe = regexp.MustCompile(`^[A-Za-z0-9/._ -]+$`)

func registerBotActorSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (`+botActorAlternation+`) posts "([^"]+)" on the issue$`, func(ctx context.Context, actor, body string) (context.Context, error) {
		return ctx, whenBotActs(ctx, world.FromContext(ctx), actor, botActionComment, body)
	})
	sc.Step(`^the (`+botActorAlternation+`) labels the issue "([^"]+)"$`, func(ctx context.Context, actor, label string) (context.Context, error) {
		return ctx, whenBotActs(ctx, world.FromContext(ctx), actor, botActionLabel, label)
	})
}

// whenBotActs makes the named bot comment on or label the scenario's
// issue. ScenarioStart is set before the bot acts, so the harness runs
// the action starts are counted; a label leaves an earlier start alone,
// so a comment and a label by the same bot count as one window.
func whenBotActs(ctx context.Context, w *world.World, actor, action, value string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	if action == botActionComment || w.ScenarioStart.IsZero() {
		// Run created_at has second precision (#7957).
		w.ScenarioStart = time.Now().Truncate(time.Second)
	}
	if actor == actorE2EBot {
		return e2eBotActs(ctx, w, action, value)
	}
	role, ok := botActorRoles[actor]
	if !ok {
		return fmt.Errorf("unknown bot actor %q", actor)
	}
	return roleBotActs(ctx, w, role, action, value)
}

// e2eBotActs acts as the suite's own installation identity.
func e2eBotActs(ctx context.Context, w *world.World, action, value string) error {
	switch action {
	case botActionComment:
		_, err := w.SCM.AddComment(ctx, w.RepoOwner, w.RepoName, w.IssueNumber, value)
		return err
	case botActionLabel:
		return w.SCM.AddIssueLabels(ctx, w.RepoOwner, w.RepoName, w.IssueNumber, value)
	default:
		return fmt.Errorf("unknown bot action %q", action)
	}
}

// roleBotActs pushes a workflow that acts as the role's bot and waits for
// its run to succeed.
func roleBotActs(ctx context.Context, w *world.World, role, action, value string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	lister, ok := w.CI.(ci.RunLister)
	if !ok {
		return fmt.Errorf("bot actor: CI driver %T cannot list workflow runs (ci.RunLister)", w.CI)
	}
	nonce, err := mintProbeNonce()
	if err != nil {
		return err
	}
	name := "bot-actor-" + nonce
	branch := "behaviour/" + name
	workflow, err := renderBotActorWorkflow(botActorWorkflowSpec{
		Name:     name,
		Branch:   branch,
		Role:     role,
		Repo:     w.RepoOwner + "/" + w.RepoName,
		RepoName: w.RepoName,
		Issue:    w.IssueNumber,
		Action:   action,
		Value:    value,
	})
	if err != nil {
		return err
	}

	if err := w.SCM.CreateBranch(ctx, w.RepoOwner, w.RepoName, branch); err != nil {
		return fmt.Errorf("bot actor: creating branch %s: %w", branch, err)
	}
	w.CreatedBranches = append(w.CreatedBranches, branch)
	path := ".github/workflows/" + name + ".yml"
	if err := w.SCM.CommitFileToBranch(ctx, w.RepoOwner, w.RepoName, branch, path, "behaviour: add bot actor workflow", workflow); err != nil {
		return fmt.Errorf("bot actor: committing the workflow to %s: %w", branch, err)
	}
	run, err := waitForMintProbeRun(ctx, lister, w.RepoOwner, w.RepoName, name)
	if err != nil {
		return fmt.Errorf("bot actor workflow failure: %w", err)
	}
	if run.Conclusion != "success" {
		return fmt.Errorf("bot actor workflow failure: run %d concluded %q (%s)", run.ID, run.Conclusion, run.HTMLURL)
	}
	return nil
}

// botActorWorkflowSpec is the input of renderBotActorWorkflow.
type botActorWorkflowSpec struct {
	Name, Branch string
	Role         string
	Repo         string // owner/repo of the leased repository
	RepoName     string
	Issue        int
	Action       string
	Value        string
}

// renderBotActorWorkflow renders the workflow pushed to the leased
// repository. The workflow reads FULLSEND_MINT_URL from that repository,
// so the bot's token comes from the mint the repository is installed
// against. The minted token is masked and revoked before the job ends.
func renderBotActorWorkflow(s botActorWorkflowSpec) ([]byte, error) {
	if !mintProbeRoleRe.MatchString(s.Role) {
		return nil, fmt.Errorf("bot actor: invalid role %q", s.Role)
	}
	if s.Action != botActionComment && s.Action != botActionLabel {
		return nil, fmt.Errorf("bot actor: unknown action %q", s.Action)
	}
	if s.Issue <= 0 {
		return nil, fmt.Errorf("bot actor: invalid issue number %d", s.Issue)
	}
	if !botActorValueRe.MatchString(s.Value) {
		return nil, fmt.Errorf("bot actor: invalid %s value %q", s.Action, s.Value)
	}
	if !mintProbeRepoRe.MatchString(s.Name) || !mintProbeRepoRe.MatchString(s.RepoName) {
		return nil, fmt.Errorf("bot actor: invalid name %q or repository %q", s.Name, s.RepoName)
	}
	if s.Branch == "" || strings.ContainsAny(s.Branch, "'\"\n\r\t {}$") {
		return nil, fmt.Errorf("bot actor: invalid branch %q", s.Branch)
	}
	if !strings.HasSuffix(s.Repo, "/"+s.RepoName) {
		return nil, fmt.Errorf("bot actor: repository %q does not end in %q", s.Repo, s.RepoName)
	}
	return []byte(fmt.Sprintf(botActorWorkflowTemplate,
		s.Name, s.Branch, s.Role, s.Repo, s.RepoName, s.Issue, s.Action, s.Value)), nil
}

// botActorWorkflowTemplate takes: workflow name, branch, role, owner/repo,
// repo name, issue number, action, value.
const botActorWorkflowTemplate = `# Behaviour-test bot actor (pkg/behaviourtest/steps/bot_actors.go).
# Pushed to a temporary branch of the leased test repository; scenario
# cleanup deletes the branch.
name: %s

on:
  push:
    branches: ['%s']

permissions: {}

jobs:
  act:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      id-token: write
    timeout-minutes: 10
    steps:
      - name: Act as the role bot
        env:
          MINT_URL: ${{ vars.FULLSEND_MINT_URL }}
          ROLE: '%s'
          REPO: '%s'
          REPO_NAME: '%s'
          ISSUE: '%d'
          ACTION: '%s'
          VALUE: '%s'
        run: |
          set -euo pipefail
          token=''
          revoke_token() {
            local attempt code
            [[ -n "${token}" ]] || return 0
            for attempt in 1 2 3; do
              code="$(curl -sS --connect-timeout 10 --max-time 20 -o /dev/null -w '%%{http_code}' -X DELETE \
                -H "Authorization: Bearer ${token}" \
                -H "Accept: application/vnd.github+json" \
                https://api.github.com/installation/token)" || code=000
              if [[ "${code}" == 204 ]]; then
                token=''
                return 0
              fi
              sleep 2
            done
            return 1
          }
          trap 'revoke_token || true' EXIT

          if [[ -z "${MINT_URL}" ]]; then
            echo "FULLSEND_MINT_URL is not set on the repository"
            exit 1
          fi
          oidc_token="$(curl -sS --fail --retry 3 --retry-delay 2 --retry-all-errors \
            -H "Authorization: bearer ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}" \
            "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=fullsend-mint" | jq -er '.value')"
          echo "::add-mask::${oidc_token}"

          request="$(jq -nc --arg role "${ROLE}" --arg repo "${REPO_NAME}" \
            '{role: $role, level: "write", repos: [$repo]}')"
          minted="$(curl -sS --fail --retry 2 --retry-delay 2 \
            -H "Authorization: Bearer ${oidc_token}" \
            -H "Content-Type: application/json" \
            -d "${request}" \
            "${MINT_URL%%/}/v1/token" | jq -er '.token')"
          echo "::add-mask::${minted}"
          token="${minted}"

          case "${ACTION}" in
            comment)
              url="https://api.github.com/repos/${REPO}/issues/${ISSUE}/comments"
              payload="$(jq -nc --arg body "${VALUE}" '{body: $body}')"
              ;;
            label)
              url="https://api.github.com/repos/${REPO}/issues/${ISSUE}/labels"
              payload="$(jq -nc --arg label "${VALUE}" '{labels: [$label]}')"
              ;;
            *)
              echo "unknown action ${ACTION}"
              exit 1
              ;;
          esac
          curl -sS --fail -o /dev/null -X POST \
            -H "Authorization: Bearer ${token}" \
            -H "Accept: application/vnd.github+json" \
            -d "${payload}" \
            "${url}"
          revoke_token
`
