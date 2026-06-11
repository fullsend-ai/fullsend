# Runbook: Jira Automation Webhook Setup

This runbook walks a Jira admin through creating the two Jira Automation rules
that connect a Jira project to the fullsend Jira triage agent.

## Background

The Jira triage agent is triggered by GitHub `repository_dispatch` events, not
by polling. Jira Automation rules fire HTTP webhooks when issue events occur;
those webhooks POST to the GitHub API, which queues a dispatch event that
launches the agent.

Two rules are required because Jira Automation has different trigger types for
issue creation and comment events:

- **Rule 1** fires on issue creation and sends a `jira-issue-created` dispatch.
- **Rule 2** fires on any comment and sends a `jira-command` dispatch when the
  comment body contains `/fs-triage`.

Both rules target the same GitHub endpoint: the `.fullsend` config repository
for your organization.

`FULLSEND_DISPATCH_TOKEN` is a GitHub Personal Access Token with `repo` scope
on the `.fullsend` config repository. It is stored as a Jira Automation secret
(not in GitHub Secrets) because the webhook fires from Jira's automation
runtime, not from a GitHub Actions workflow.

## Prerequisites

- **`fullsend jira enroll` completed.** The project key must be registered in
  your `.fullsend` config repo before dispatched events will be processed.
  Run `fullsend jira enroll <PROJECT-KEY> --host <jira-host>` if not done.
- **`FULLSEND_DISPATCH_TOKEN`.** A GitHub Personal Access Token with `repo`
  scope on the `.fullsend` config repository. To create one: GitHub →
  Settings → Developer settings → Personal access tokens → Fine-grained tokens
  → New token → select the `.fullsend` repo → grant `Contents: Read and write`.
  Store it as a Jira Automation secret: Jira → Settings → System → Automation
  secrets → Add secret, name it `FULLSEND_DISPATCH_TOKEN`.
- **Jira Admin access** on the project (or global admin) to create Automation
  rules.

---

## Rule 1 — Auto-triage on issue creation

### Navigate to Automation

1. Open your Jira project.
2. Go to **Project Settings** → **Automation**.
3. Click **Create rule** (top right).

### Set the trigger

1. Under **When**, select **Issue created**.
2. Click **Save**.

### Skip conditions

No conditions are needed. All new issues in the project should be triaged.

### Add the action

1. Click **+ Add component** → **Action**.
2. Select **Send web request**.
3. Fill in the fields:

**URL:**
```
https://api.github.com/repos/<ORG>/.fullsend/dispatches
```
Replace `<ORG>` with your GitHub organization name (e.g., `acme-corp`).

**HTTP method:** `POST`

**Headers** (add each as a separate header entry):

| Name | Value |
|------|-------|
| `Authorization` | `Bearer {{FULLSEND_DISPATCH_TOKEN}}` |
| `Accept` | `application/vnd.github.v3+json` |
| `Content-Type` | `application/json` |

For the `Authorization` header value, use Jira's secret reference syntax:
click the `{{…}}` button in the value field and select
`FULLSEND_DISPATCH_TOKEN`. This prevents the token from appearing in plain text
in the rule definition.

**HTTP body** (select `Custom data` / `JSON`):
```json
{
  "event_type": "jira-issue-created",
  "client_payload": {
    "issue_key": "{{issue.key}}",
    "project_key": "{{issue.fields.project.key}}"
  }
}
```

4. Click **Save**.

### Name and enable the rule

1. Click **Turn it on** (or set the rule name first: `fullsend: auto-triage on issue creation`).
2. Confirm the rule is **Enabled**.

---

## Rule 2 — `/fs-triage` command

### Navigate to Automation

1. Open your Jira project.
2. Go to **Project Settings** → **Automation**.
3. Click **Create rule**.

### Set the trigger

1. Under **When**, select **Comment added**.
2. Click **Save**.

### Add a condition

1. Click **+ Add component** → **Condition**.
2. Select **Comment body contains text** (or **Advanced compare condition**
   depending on your Jira version).
3. Set the value to `/fs-triage` (exact string, case-sensitive).

This prevents the rule from firing on every comment — only comments that
contain the literal string `/fs-triage` will proceed.

### Add the action

1. Click **+ Add component** → **Action**.
2. Select **Send web request**.
3. Fill in the fields:

**URL:**
```
https://api.github.com/repos/<ORG>/.fullsend/dispatches
```
Use the same `<ORG>` value as Rule 1.

**HTTP method:** `POST`

**Headers** (same as Rule 1):

| Name | Value |
|------|-------|
| `Authorization` | `Bearer {{FULLSEND_DISPATCH_TOKEN}}` |
| `Accept` | `application/vnd.github.v3+json` |
| `Content-Type` | `application/json` |

**HTTP body** (select `Custom data` / `JSON`):
```json
{
  "event_type": "jira-command",
  "client_payload": {
    "issue_key": "{{issue.key}}",
    "project_key": "{{issue.fields.project.key}}",
    "command": "/fs-triage"
  }
}
```

4. Click **Save**.

### Name and enable the rule

1. Name the rule `fullsend: /fs-triage command`.
2. Click **Turn it on**.
3. Confirm the rule is **Enabled**.

---

## Verification

### Test Rule 1 (auto-triage)

1. Create a test Jira issue in the enrolled project. Use a clearly incomplete
   description so the agent has something to act on.
2. Wait approximately 30–60 seconds for the automation rule to fire.
3. In GitHub, go to your organization → `.fullsend` repository → **Actions**
   tab.
4. Look for a workflow run triggered by a `repository_dispatch` event with
   `event_type: jira-issue-created`. The run should appear within a few minutes.
5. After the run completes, return to the Jira issue. The agent should have
   posted a triage comment and applied a `fullsend:` label.

### Test Rule 2 (/fs-triage command)

1. On any existing issue in the enrolled project, add a comment containing
   `/fs-triage`.
2. Wait approximately 30–60 seconds.
3. In GitHub → `.fullsend` → **Actions** tab, look for a run triggered by
   `repository_dispatch` with `event_type: jira-command`.
4. After the run completes, the agent should have updated the triage comment
   on the issue.

---

## Troubleshooting

### The automation rule fired but no GitHub Actions run appeared

- **Wrong org or repo in the URL.** Double-check that the URL is
  `https://api.github.com/repos/<ORG>/.fullsend/dispatches` and that `<ORG>`
  matches your GitHub organization exactly (case-sensitive).
- **Invalid or expired token.** Verify the `FULLSEND_DISPATCH_TOKEN` Jira secret
  contains a valid GitHub PAT. Test it manually:
  ```
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST \
    -H "Authorization: Bearer <YOUR-TOKEN>" \
    -H "Accept: application/vnd.github.v3+json" \
    -H "Content-Type: application/json" \
    -d '{"event_type":"jira-triage-test","client_payload":{"issue_key":"TEST-1","project_key":"TEST"}}' \
    https://api.github.com/repos/<ORG>/.fullsend/dispatches
  ```
  A `204` response means the token and URL are correct. A `401` means the token
  is invalid. A `404` means the repo doesn't exist or the token lacks access.
- **Token scope.** The PAT must have `repo` scope (or `Contents: Read and write`
  for fine-grained tokens) on the `.fullsend` repository specifically.

### The GitHub Actions run appeared but the agent failed

- **Project not enrolled.** Run `fullsend jira enroll <PROJECT-KEY> --host <jira-host>`.
  Check the `.fullsend` repo for a config entry for the project key.
- **Jira credentials missing.** The agent needs a Jira API token to read issue
  content and post comments. Check that `JIRA_API_TOKEN` and `JIRA_USER_EMAIL`
  are configured in the `.fullsend` repo's GitHub Actions secrets.
- **Read the run logs.** In GitHub → `.fullsend` → **Actions** → click the
  failed run → expand the failing step. The agent logs the exact error before
  exiting.

### The comment condition in Rule 2 is not matching

- The condition must match the literal string `/fs-triage`. Confirm there are no
  leading/trailing spaces in the condition value in Jira Automation.
- Some Jira versions call this condition **Comment body** → **contains**; others
  use **Advanced compare** with field `comment.body`. Either works as long as
  the match string is `/fs-triage`.

### Where to check logs

All agent activity is visible in GitHub Actions:

1. Go to your GitHub organization.
2. Open the `.fullsend` repository.
3. Click the **Actions** tab.
4. Filter by workflow name or look for runs with the `repository_dispatch`
   trigger.
5. Click any run to see step-by-step logs, including the agent transcript.

Jira Automation also keeps an audit log of rule executions: **Project Settings**
→ **Automation** → click the rule name → **Audit log**. This shows whether the
webhook fired and what HTTP response code GitHub returned.
