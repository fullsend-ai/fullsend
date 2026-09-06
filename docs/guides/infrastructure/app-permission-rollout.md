# Rolling out a GitHub App permission

Adding a permission to an agent role is not a code change you can just deploy. The mint asks GitHub
for the role's permissions every time it mints a token, and GitHub only hands out permissions the
**installation** has already accepted. Existing installations accept nothing automatically: an owner
of each installing organization has to click Accept. This page is the procedure that gets a new
permission from a pull request to every installation without breaking the installations that have
not caught up yet.

The running example is `packages:read` for the `coder` role, shipped in fullsend v0.40.0 so agents
can pull from GitHub Packages. Substitute your own permission and role throughout.

## When you need this

Follow this page whenever a role in the mint's permission map needs a permission it did not request
before — a new GitHub API surface for an agent, a read scope for a package registry, a write scope
for a new automation. You do not need it to change *which repositories* a token covers, to add a new
role, or to change anything outside the GitHub App permission set; see
[Mint administration](mint-administration.md) for those.

Five roles are involved, usually five different people:

| Role | What they do |
|------|--------------|
| **Contributor** | Lands one PR that adds the permission to the mint's role map, the embedded copy of the mint, and the App manifest, and marks it optional for the duration of the rollout. |
| **App owner** | Adds the permission on the GitHub App registration, which puts a pending update in front of every installation. |
| **Mint admin** | Deploys the mint that understands the new map, confirms the deploy landed, and watches the logs for installations that have not accepted. |
| **Installation owner** | An **owner** of an organization the App is installed on. Accepts the pending update for that organization. |
| **CLI user / repo admin** | Upgrades the fullsend CLI and re-runs `fullsend github setup <owner/repo>` for their repository. |

Each step-by-step section below is written for one of these people; find your row and skip to that
section.

### Before you start

- **Contributor**: a fullsend checkout with Go and `make lint` working; write access to open a PR.
- **App owner**: an **owner** of the GitHub organization that owns the App registration (for the
  hosted set, `fullsend-ai`; for a self-managed set, your own org).
- **Mint admin**: the fullsend CLI at the release that carries the change, deploy rights on the
  mint's GCP project or Cloudflare account (see [Mint administration](mint-administration.md)), and
  read access to its logs.
- **Installation owner**: an **owner** of each organization the App is installed on. Members
  cannot see or accept permission requests.
- **CLI user / repo admin**: admin on the repository, `gh` authenticated, and the fullsend CLI at a
  release cut after the mint deploy.

The first three steps happen in a fixed order (code, then App registration, then mint deploy). After
that, nobody waits for anybody: installation owners accept whenever they get to it, CLI users upgrade
whenever they like, and every agent keeps working in the meantime. That is the whole point of the
mechanism — the permission rolls out over days or weeks without a flag day.

## The rules

These five facts explain every step below. If a step ever looks optional, come back here.

1. **There is no partial downscope.** When the mint POSTs for an installation token, GitHub rejects
   the *entire* request with `422` if any requested permission is not granted on that installation.
   It does not quietly return a smaller token. A role map that asks for one ungranted permission
   therefore breaks every token for that role, not just the new capability.
2. **The installation object lists accepted permissions only.** The mint looks the installation up
   (`GET /orgs/<org>/installation`, `GET /repos/<owner>/<repo>/installation`) and reads the
   `permissions` map. A pending update never appears there — only what the organization has already
   accepted. This is why the App registration can be updated before the mint is redeployed without
   breaking anything, and why the mint can decide what to request *before* it POSTs.
3. **Saving the registration is what starts the rollout.** When the App owner saves a new permission
   on the App's Permissions & events page, GitHub marks every installation of that App as having a
   pending update and emails the owners of each installing organization. New installations of an
   already-updated App get the permission at install time. An existing installation's `permissions`
   map flips only when an **owner** of that organization accepts.
4. **Optional permissions are dropped; everything else fails fast.** Before the token POST, the mint
   compares the role's requested permissions with the installation's granted map. A permission
   listed in `optionalRolePermissions` (today: `packages` for the `coder` role and for the `fix`
   role) is dropped when ungranted, and the drop is logged. Any *other* ungranted permission fails
   immediately with `422` and no token. The CLI mirrors this: a missing optional permission is a
   warning and setup continues; a missing required permission is a setup error, exactly as it was
   before this mechanism existed. If the installation lookup carries no `permissions` field at all
   (or an empty one), the mint sends the full requested set and lets GitHub validate it.
5. **The order is fixed:** code merged → App registration updated → mint redeployed → CLI released →
   ongoing outreach to installation owners. The registration comes before the mint so that
   organizations which accept quickly already have the permission on the first mint that asks for
   it. The CLI comes after the mint because `fullsend github setup` hard-fails on required gaps and
   only downgrades a gap to a warning for the optional set — a CLI that treats a permission as
   optional should not be in users' hands before the mint that agrees with it. **Never gate a deploy
   on every installation having accepted.** One inactive or unreachable installation would stall the
   platform forever; the mint's `dropped=` log lines are the outreach signal, not a release gate.

## Step by step

The subsections below are in rollout order. Each one is a different person's job.

### Contributor: land the change

One pull request changes the mint's role map, the embedded copy of the mint used by the Cloud
Function, and the GitHub App manifest the CLI creates apps from — and adds the permission to
`optionalRolePermissions` so installations that have not accepted keep minting tokens. The exact
files, the parity tests, and the commit conventions are in
[Adding a permission to a role](../../contributing/mintcore.md#adding-a-permission-to-a-role). This
is a breaking change for users, because App owners must update their registration, so the commit
subject and PR title carry `!`.

Nothing happens to any installation when this merges. The mint does not use the new map until a mint
admin deploys it.

### App owner: update the App registration

Do this once per App set you own. For the hosted apps the coder App slug is `fullsend-ai-coder`; for
your own app set it is `<app-set>-coder`.

1. Open `https://github.com/organizations/<owner-org>/settings/apps/<app-slug>/permissions`.
2. Find the permission in the section GitHub files it under — **Repository permissions** for most
   (the worked example: **Packages** → **Read-only**), **Organization permissions** for org-level
   ones such as organization projects — and set the level.
3. Fill in the optional note to users. GitHub shows it to every organization owner alongside the
   request; one sentence saying what the agents need the permission for makes acceptance much
   faster.
4. **Save changes**.

What you should see: the page confirms the save, and every installation of the App now carries a
pending update. GitHub emails the owners of each organization the App is installed on.

Repeat for any **test** App set you run. A test app set left un-updated sits on permission-drop
warnings forever and makes the rollout logs harder to read.

The `fix` and `code` dispatch stages both mint the `coder` role and share the coder App. There is no
separate `fix` App to update.

### Mint admin: deploy the mint and watch the logs

Deploy the mint that carries the new role map:

```bash
fullsend mint deploy --project=<project> --region=<region>
```

Add `--public` if the mint is a public mint. Deploy rejects mode conversion in either direction, so
redeploy with the same mode the mint already has — omitting `--public` on a public mint fails with
`existing mint is in public mode (PER_REPO_WIF_REPOS=*); redeploy with --public`.

Then confirm the deploy actually took traffic. A green deploy is not proof:

```bash
curl -s <mint-url>/health
```

The response carries a `commit` field, and it must be the commit you just deployed. If it still
shows the old one, see [Troubleshooting](#troubleshooting) — a deploy can create a new revision
while traffic stays pinned to an older one.

From then on, three log lines tell you the state of every mint. The first is the rollout working as
intended — the organization has not accepted yet, so the mint dropped the optional permission and
minted a token without it:

```text
installation permissions not granted: org="<org>" installation_id=<id> role="coder" dropped=packages:read; if the App already requests these permissions, Accept the pending update at https://github.com/organizations/<org>/settings/installations/<id>; otherwise the App owner must add them first
```

The second is a successful mint. The `permissions` map is exactly what the token carries — check
whether the new permission is in it:

```text
granted scope: repos=[…] permissions=map[…] repo_selection=…
```

The third is a hard failure for a permission that is *not* in the optional list. Expect some of these
right after the deploy for roles you did not touch; see [Troubleshooting](#troubleshooting). In the
mint log it looks like this:

```text
failed to mint token: org=<org> target_org=<org> role=coder err=required permissions missing for role "coder": <perm:level>; if the App already requests these permissions, Accept the pending update at https://github.com/organizations/<org>/settings/installations/<id>; otherwise the App owner must add them first
```

The caller receives HTTP `422` with only the message part as the `error` field — no
`failed to mint token:` prefix and no `org=` fields:

```json
{"error": "required permissions missing for role \"coder\": <perm:level>; if the App already requests these permissions, Accept the pending update at https://github.com/organizations/<org>/settings/installations/<id>; otherwise the App owner must add them first"}
```

To find lagging organizations, read the logs for the drop line. On GCP the filter shape is:

```bash
gcloud logging read \
  'resource.type=cloud_run_revision AND textPayload:"permissions not granted"' \
  --project=<project> --limit=50 --freshness=7d
```

Swap `"permissions not granted"` for `"required permissions missing"` to find hard failures. Each
line names the organization, so the two filters give you the outreach list and the breakage list.
For a Cloudflare Worker mint, the same lines appear in the Worker logs (`npx wrangler tail
<worker-name>` or the dashboard's Logs view); grep for the same two phrases.

### Installation owner: accept the update

You get an email from GitHub saying the App is requesting updated permissions. To act on it:

1. Go to your organization → **Settings** → **Third-party Access** → **GitHub Apps**, or straight to
   `https://github.com/organizations/<org>/settings/installations`.
2. Click **Configure** next to the App (for the worked example, the coder App).
3. A banner at the top of the page says the App is requesting an update to its permissions. Click
   **Review request**.
4. Review the listed permission change and accept it.

**Only organization owners see this.** Members see the App page with no banner and no request — if
someone reports that the Review request is missing, check whether they are an owner. If the App is
not listed at all, it is not installed on that organization and there is nothing to accept. For an
App installed on a personal account rather than an organization, the equivalent page is
`https://github.com/settings/installations`.

To verify from the command line, before and after:

```bash
gh api orgs/<org>/installations \
  --jq '.installations[] | select(.app_slug=="<app-slug>") | .permissions'
```

Before accepting, the pending permission is absent from the map — the request does not show up
anywhere in the API. After accepting, it is present (for the worked example, `"packages": "read"`).
The endpoint needs organization-admin access.

### CLI user: upgrade the CLI and re-run setup

Nothing in this step is needed for tokens to gain the permission — that happens the moment your
organization's owner accepts. This step refreshes the repository's shim workflows to the release that
knows about the new permission and shows you where your installation stands. Upgrade the fullsend CLI
to a release cut *after* the mint was deployed, then re-run setup for your repository:

```bash
fullsend github setup <owner/repo>
```

Re-running setup with no flags is the supported update path: it refreshes the shim workflows and
reuses your existing secrets, and it leaves `.fullsend/config.yaml` alone unless you pass
`--runtime`, `--agents`, `--mint-url`, an `--inference-*` or an `--openai-*` flag.

What you see depends on the state of your organization's installation:

- Update pending and the permission is optional — a warning, and setup finishes normally:

  ```text
  app <app-slug> pending rollout permissions (setup continues): packages:read — if the App already requests these permissions, Accept the pending update at https://github.com/organizations/<org>/settings/installations/<id>; otherwise the App owner must add them first
  ```

- Already accepted — nothing about permissions is printed.
- A **required** permission is missing — a warning, then a failed setup:

  ```text
  app <app-slug> missing permissions: <perm:level> — if the App already requests these permissions, Accept the pending update at https://github.com/organizations/<org>/settings/installations/<id>; otherwise the App owner must add them first
  ```

  and the run ends with `apps have stale permissions:` listing each App. Fix it by accepting the
  pending update (or having the App owner add the permission), then re-run setup.
- GitHub returned no permission data for the installation — `app <app-slug>: permissions not
  available, skipping check`. Setup continues, and GitHub validates at mint time.

## What you will see at run time

| State | Mint log | Token contents | `fullsend github setup` |
|-------|----------|----------------|-------------------------|
| Update pending, permission is **optional** | `installation permissions not granted: … dropped=packages:read; …`, then `granted scope: …` | Role's permissions **minus** the pending one | `pending rollout permissions (setup continues): packages:read — …`, setup succeeds |
| Update accepted | `granted scope: … permissions=map[… packages:read …]`, no drop line | Full role permission set | Nothing printed about permissions |
| A **required** permission is ungranted | `failed to mint token: … err=required permissions missing for role "<role>": <perm:level>; …` | No token — the mint returns `422` whose `error` field is the message after `err=` | `missing permissions: <perm:level> — …`, then setup fails with `apps have stale permissions:` |
| Installation lookup returned no `permissions` map | No preflight line; GitHub validates the POST | Full requested set, or GitHub's `422` | `permissions not available, skipping check` |

## Finishing the rollout

The `dropped=` lines are the to-do list. Each one names an organization that has not accepted;
contact its owners with the Accept URL from the log line. When the drop lines stop appearing over a
full activity cycle — long enough that quiet installations have minted at least once — the rollout
is done.

Then land a follow-up PR that removes the permission from `optionalRolePermissions` (see
[Adding a permission to a role](../../contributing/mintcore.md#adding-a-permission-to-a-role)). It
stays in the role map; only the optional marking goes away. From that point the permission behaves
like every other one: an installation that has not granted it gets a fast, loud `422` instead of a
silently smaller token. Leaving the optional entry in place indefinitely is the failure mode to
avoid — it turns a permanent misconfiguration into a warning nobody reads.

## Troubleshooting

**The coder mints fine, but the agent gets a `403` from GitHub Packages later in the run.** Expected
while the rollout is in progress: the token was minted without `packages:read` because the
organization has not accepted yet. Confirm with the mint log — there will be a `dropped=packages:read`
line for that organization — and get an owner to accept. This is the one trade-off of the optional
mechanism: the failure moves from mint time to use time.

**Right after a mint deploy, `required permissions missing` appears for roles you did not touch.**
That is old debt becoming visible, not a regression from your change. The preflight fails fast on
*any* ungranted required permission, so an App update an organization never accepted — possibly
months ago — now surfaces as a clear error instead of an opaque `422` from GitHub. Do not roll back
the deploy. Send the organization's owner to the installation page; accepting fixes it.

**An owner says there is no Review request on the App page.** Three causes, in order of likelihood:
they are an organization member rather than an owner (only owners see the banner); the App is not
installed on that organization at all (it will not be listed under Third-party Access → GitHub
Apps); or the update was already accepted. Check with
`gh api orgs/<org>/installations --jq '.installations[] | select(.app_slug=="<app-slug>") | .permissions'`
— if the permission is already in the map, there is nothing left to accept.

**`fullsend mint deploy` reported success but `/health` still shows the old commit.** On GCP a deploy
can build the image and create a new Cloud Run revision while traffic stays pinned to an older
revision, and still report success. Always `curl <mint-url>/health` after a deploy and compare the
`commit` field; if it is stale, inspect the service's revisions and traffic split, move traffic to
the new revision, and re-check. Tracked in
[fullsend-ai/fullsend#6945](https://github.com/fullsend-ai/fullsend/issues/6945).

**Setup fails on a permission you expected to be optional.** Either the permission is not in
`optionalRolePermissions` for *that* role — the list is per role, and today only `coder` and `fix`
have an entry — or the CLI predates the mint that introduced the optional set. Upgrade the CLI, and
check the role named in the error against the map. If the permission genuinely needs to be optional
during a rollout, that is a code change: see
[Adding a permission to a role](../../contributing/mintcore.md#adding-a-permission-to-a-role).

## Related

- [Adding a permission to a role](../../contributing/mintcore.md#adding-a-permission-to-a-role) — the contributor side of this procedure.
- [Mint administration](mint-administration.md) — deploying the mint, managing roles and app sets.
- [Infrastructure reference](infrastructure-reference.md#role-permissions-matrix) — the permissions each role requests.
- [Operations](../getting-started/operations.md) — day-to-day repository and organization administration.
