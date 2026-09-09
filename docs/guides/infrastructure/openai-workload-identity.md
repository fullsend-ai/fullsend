# OpenAI Workload Identity

Run GPT models on the [pi](../../runtimes/pi.md) or [codex](../../runtimes/codex.md) runtime
without storing an OpenAI API key
anywhere. Your GitHub Actions job proves who it is with its OIDC token, OpenAI hands back a
short-lived token (minutes — it never outlives the GitHub token it came from, and an hour at most),
fullsend renews it for as long as the run lasts, and the agent sandbox never sees any of it — it only
ever holds a placeholder that the OpenShell gateway swaps for the real token on the way out.

Setting it up is one visit to the OpenAI console — yours, or your IT administrator's — and one
command per repository. No key is created, downloaded or rotated.

> **GitHub Actions only.** The exchange needs the job's OIDC endpoint. For GitLab CI and for runs
> on your own machine, use an API key in the runner environment — see [Run it locally](#run-it-locally).

## What you end up with

Three identifiers, which you hand to fullsend in [step 4](#4-tell-fullsend-the-three-identifiers).
They are not secrets: on their own they grant nothing.

| Identifier | What it is | Where it comes from |
|---|---|---|
| **Audience** | The string your runs ask GitHub to put in the token's `aud` claim. It must equal what the identity provider was created with, character for character. | Whoever created the provider — you (route A) or your IT admin (route B). It is not issued by anyone and has no required format. |
| **Identity provider ID** | The OpenAI Workload Identity Provider for GitHub Actions in your organization. | The provider's page in the OpenAI console (route A), or your IT admin (route B). |
| **Service account ID** | The service account the mapping grants to your repository, in the project the runs are billed to. | The mapping (route A), or your IT admin (route B). |

You also need an OpenAI **project** to bill the runs to (ideally a dedicated one with a budget
alert), and the repository must be enrolled per-repo on a fullsend release that includes this
feature — PR #6695 for pi, or the release that carries the codex runtime and `CODEX_VERSION`
(#6920) if you are putting an agent on codex; check the release notes.

## Which route are you on?

Workload Identity Providers and their service-account mappings are an **organization-level**
security setting in OpenAI (Organization Settings → Security). Who can edit them decides your route —
open that page to find out: if you can see **Workload Identity Provider** and add one, you are on
route A; if the page or the setting is not there, you are on route B and someone else owns it.

- **Route A — you can manage providers in your OpenAI organization.** Being an owner of a project
  is not enough; you need the organization-level permission. You create (or reuse) the provider and
  add one mapping per repository yourself. Do [step 1](#1-see-what-your-repository-actually-claims-both-routes),
  then [A2](#a2-add-or-reuse-the-identity-provider-route-a) and [A3](#a3-map-the-repository-to-a-service-account-route-a).
- **Route B — the provider is managed centrally.** This is the common shape in a company: an IT
  administrator owns Organization Settings and the GitHub Actions provider; you own (or request) a
  project. A service account is a non-human API principal inside a project; the administrator can
  create it while adding the mapping, so all you need to know is the project's name or ID
  (Organization → Projects). You send one request per repository and receive the three
  identifiers back. Do [step 1](#1-see-what-your-repository-actually-claims-both-routes), then
  [B2](#b2-send-the-request-route-b) and [B3](#b3-record-what-you-get-back-route-b).

Either way, steps 4 and onwards are the same, and
[`fullsend inference openai`](../../cli/inference.md#inference-openai) does the paperwork on both:
`request` computes the provider and mapping values from the repository name (route A: what to type
into the console; route B: the ticket to send), and `import` records the identifiers you end up with.

> **The rule that holds in both routes: trust is per repository.** A mapping asserts
> `repository == <your-github-org>/<repo>` for each company-owned repository you enrol — one
> mapping per repository (a mapping has no list form: its assertions are exact scalar values, all of
> which must match within a mapping, OR-ed across mappings). One trailing wildcard with a non-empty
> prefix is permitted per assertion value (e.g. `refs/pull/*`). Never a pattern over the organization
> (`repository_owner`, a name prefix, a derived attribute): a GitHub organization often contains
> repositories the company does not own, and a pattern would let every one of them obtain your
> token. Adding a repository later means adding its assertion; that is the gate, by design.

## 1. See what your repository actually claims (both routes)

OpenAI decides whether to trust a run by comparing claims in the GitHub token with the mapping's
assertions. A wrong audience and a wrong claim fail the same way, so look at the real values before
writing the mapping (route A) or before asking for it (route B). Add this temporary workflow to
the repository, run it, and copy the printed claims. Put the provider's audience in `AUD` if you
already know it; if you do not yet, any string works for this check — `repository` and `ref` are
what you are after.

```yaml
name: openai-wif-check
on: workflow_dispatch
permissions:
  id-token: write
  contents: read
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - env:
          AUD: <the provider's audience>
        run: |
          TOK=$(curl -sSf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=$(jq -rn --arg a "$AUD" '$a|@uri')" | jq -r .value)
          echo "::add-mask::$TOK"
          TOK="$TOK" python3 -c "import base64,json,os; p=os.environ['TOK'].split('.')[1]; \
            p+='='*(-len(p)%4); c=json.loads(base64.urlsafe_b64decode(p)); \
            print(json.dumps({k:c.get(k) for k in ('iss','aud','repository','ref','workflow_ref','job_workflow_ref')}, indent=2))"
```

You will see something like:

```json
{
  "iss": "https://token.actions.githubusercontent.com",
  "aud": "<the provider's audience>",
  "repository": "<your-github-org>/<repo>",
  "ref": "refs/heads/main",
  "workflow_ref": "<your-github-org>/<repo>/.github/workflows/openai-wif-check.yml@refs/heads/main",
  "job_workflow_ref": "<your-github-org>/<repo>/.github/workflows/openai-wif-check.yml@refs/heads/main"
}
```

For the real agent runs, `repository` is the same as above; `ref` depends on the triggering event
(`refs/heads/main` for `issues` and `issue_comment`; `refs/pull/<N>/merge` for `pull_request_review`;
the dispatched ref for `workflow_dispatch`). `workflow_ref` names whichever fullsend workflow started
the run — a per-repo installation deploys `.github/workflows/fullsend.yaml` plus a thin `prioritize`
caller, which invoke the vendored reusable dispatcher via `workflow_call` — and `job_workflow_ref` the
pinned reusable workflow it calls. A mapping asserts `repository`, not `workflow_ref`, because the
workflows vary. Delete the check workflow when you are done.

## A2. Add or reuse the identity provider (route A)

If your organization already has a provider for GitHub Actions, reuse it: open it, note its
**audience** and copy its **identity provider ID**, and go to A3. Otherwise, in **Organization
Settings → Security → Workload Identity Provider**, add one:

| Field | Enter |
|---|---|
| OIDC issuer URL | `https://token.actions.githubusercontent.com` |
| Audience | Any string you choose, for example `fullsend://<your-github-org>` — your runs will request it verbatim |
| Use uploaded JWKS for token verification | **Off** |

Copy the **identity provider ID**. One provider serves every repository; OpenAI allows 50 providers
per organization and 50 mappings per provider.

## A3. Map the repository to a service account (route A)

> **Shortcut.** `fullsend inference openai request <owner>/<repo> --project <project>` prints the
> exact values for the fields below (one block per repository), so you copy rather than retype them.

Open the provider and add a **service account mapping**:

| Field | Enter |
|---|---|
| Claim assertions | `iss` = `https://token.actions.githubusercontent.com` · `aud` = the provider's audience · `repository` = `<your-github-org>/<repo>` |
| Project | the project the runs should be billed to |
| Service account | create a new one, for example `fullsend-<repo>-ci` |
| Permissions | `api.model.request` (fullsend also accepts `api.model.read`; anything broader is refused at run time) |

Every assertion must match what step 1 printed character for character; a one-letter difference
fails every exchange. Copy the **service account ID**.

Two things not to do:

- **Do not create an API key** for the service account. The mapping is the credential; a key would
  put a long-lived secret back into the picture.
- **Do not assert `repository_owner`, a prefix or any pattern instead of `repository`** — see the
  per-repository rule above.

**Which runs this mapping trusts.** Be precise about this, because it is the boundary you are
setting: the mapping trusts **any job in that repository that can request an OIDC token** — not one
named workflow, and not one ref. The default mapping has no `ref` assertion, matching the Vertex
path's `attribute.repository` scoping: `issues`, `issue_comment`, and `pull_request_target` events
carry the default branch ref, while `pull_request_review` events carry `refs/pull/<N>/merge`. A
repository-only mapping covers all of them.

Anyone with write access can obtain a model token: pushing a branch with a workflow that has
`permissions: id-token: write` is enough, since a same-repository `pull_request` run executes the
workflow from that branch — no merge to the default branch required. The mapping's blast radius is
"write access to this repository", and the service account's spend limit is what bounds it. That is
the trade for not asserting `workflow_ref`, which cannot cover fullsend's agent workflow files with
one value. fullsend's own dispatch authorization governs which agent runs *fullsend* starts; it does
not stand between such a workflow and OpenAI's token endpoint.

GitHub mints the token for the repository the job runs in, so the agent jobs fullsend starts for
pull requests from forks are covered too (they run in your repository, not the fork). Fork handling
differs per stage: the review→fix path rejects cross-repo PRs, while other stages have their own
authorization gates. What keeps an untrusted *contributor* from starting such a run is fullsend's
own dispatch authorization (only events from people with write access trigger agents), not the
mapping.

**Tightening with `--ref`.** If you want the mapping to restrict which refs can exchange, pass
`--ref refs/heads/main` to `fullsend inference openai request`. This emits **two mappings** per
repository: one asserting `ref` = `refs/heads/main` and one asserting `ref` = `refs/pull/*` (OpenAI
allows one trailing wildcard with a non-empty prefix), so both default-branch and
PR-review-triggered runs work. The cost is two mappings per repository instead of one, halving the
50-mapping-per-provider budget to 25 repositories. Teams that want branch-level narrowing choose
that deliberately.

If you want the mapping itself to exclude some triggers, add a claim the untrusted runs cannot
carry — for example a GitHub `environment` that the agent jobs reference and that protects itself
with required reviewers — and assert `environment == "<name>"` here.

## B2. Send the request (route B)

You cannot see the provider, so ask the administrator who owns it. Generate the request instead of
writing it — every value follows from the repository name:

```bash
fullsend inference openai request <owner>/<repo> \
  --project "<project name or id>" \
  --audience "<the provider's audience, if you know it>" \
  --format md --out openai-wif-request.md
```

Send one request per repository (or one listing several — the administrator still creates one
mapping per repository). `--format json` emits the same content as a machine-readable document, which
an administrator can fill in and hand back for [B3](#b3-record-what-you-get-back-route-b). What the
generated request says, and what to write by hand if you would rather not run the command:

```text
Subject: OpenAI Workload Identity mapping for GitHub repository <org>/<repo>

Please add a service-account mapping on the organization's GitHub Actions identity provider
(issuer https://token.actions.githubusercontent.com) with these claim assertions, exactly:

  iss        = https://token.actions.githubusercontent.com
  aud        = <the provider's audience>
  repository = <org>/<repo>

Target project: <project name / id>  (the project these runs are billed to)
Service account: create a new one named fullsend-<repo>-ci in that project
                 (or map the existing service account <id>)
Permissions on the mapping: api.model.request only — nothing broader, and please do not
create an API key for the service account; the mapping is the credential.

Please do not add a workflow_ref or sub assertion: these runs start from several
workflow files in the repository, so a single value would exclude the others.

One mapping per repository, please (assertions within a mapping are AND-ed exact values;
one trailing wildcard with a non-empty prefix is permitted per value) — not a pattern over
the organization, since not every repository in it is ours.

Please send back: the identity provider ID, the provider's audience string, and the
service account ID.
```

If the administrator's standard mapping grants more than model access, ask for it to be narrowed:
fullsend refuses a token whose permissions exceed `api.model.request`/`api.model.read`, and only
warns when the mapping does not narrow at all. If you want the mapping to exclude some triggers
(a GitHub `environment` with required reviewers, for example), the reasoning is under
[A3](#a3-map-the-repository-to-a-service-account-route-a) — ask for the extra assertion in the
same request.

## B3. Record what you get back (route B)

The reply gives you the three identifiers from [What you end up with](#what-you-end-up-with).
Record them with
[`fullsend inference openai import`](../../cli/inference.md#inference-openai-import) — it takes the
filled-in reply JSON (`fullsend inference openai import reply.json`) or the three values as flags,
and writes the same `inference.openai` block [step 4](#4-tell-fullsend-the-three-identifiers)
describes. Unlike `fullsend github setup`, which opens a pull request, `import` only writes the file
on your machine: commit it, or a CI run will not see the identifiers.

To check the enrolment before a real agent run, `fullsend inference openai status <owner>/<repo>`
prints the resolved identifiers and where they came from; inside a GitHub Actions job with
`id-token: write` it performs one exchange and reports the granted scope and expiry. Otherwise,
re-run the step-1 workflow with the real audience in `AUD` and confirm `aud` and `repository` print
as expected — that is the whole verification you can do from your side before the first run. If the
first run's exchange still returns 4xx, the assertions and the claims differ somewhere (compare them
character for character with the administrator); a `repository` that is not in any mapping is the
usual cause when a second repository is enrolled.

## 4. Tell fullsend the three identifiers

> **Shortcut.** `fullsend inference openai import` writes the same block from a reply JSON file or
> from `--audience`/`--identity-provider-id`/`--service-account-id` flags, and `--variables` sets the
> repository variables instead. See
> [`fullsend inference openai import`](../../cli/inference.md#inference-openai-import).

Re-run the setup command you enrolled the repository with, adding the three values:

```bash
fullsend github setup <your-github-org>/<repo> \
  --openai-audience "<the provider's audience>" \
  --openai-identity-provider-id "<identity provider ID>" \
  --openai-service-account-id "<service account ID>"
```

It writes them into the repository's `.fullsend/config.yaml`, the same way it records the Vertex
project and provider:

```yaml
inference:
  openai:
    audience: <the provider's audience>
    identity_provider_id: <identity provider ID>
    service_account_id: <service account ID>
```

Commit that change (setup opens a pull request for it unless you pass `--direct`). A base
configuration (`config.base.yaml`, or a vendor preset) can carry the block for many repositories,
and a repository can restate any one of the three — with a centrally managed provider, the audience
and the provider ID are typically the same for every repository and only the service account differs.

**Is it safe to commit these?** Yes. They are identifiers, not secrets: on their own they grant
nothing. OpenAI issues a token only to a caller presenting a GitHub OIDC token whose claims match
a mapping, and only a workflow in the repository with `id-token: write` can obtain one. fullsend reads
`.fullsend/config.yaml` from the base branch for pull-request events, so a pull request cannot
change them for the run that reviews it. Someone with write access could point the block at their
own OpenAI organization — and pay for their own runs — but note that write access already carries
the greater power described under [A3](#a3-map-the-repository-to-a-service-account-route-a): a
workflow they merge to `main` can obtain a model token from your mapping. Guard write access and the
project's spend limit accordingly. fullsend prints the three values in the run log so you can always
see which mapping a run used.

If you would rather keep them out of the repository, set them as **repository variables** instead
(Settings → Secrets and variables → Actions → Variables): `FULLSEND_OPENAI_AUDIENCE`,
`FULLSEND_OPENAI_IDENTITY_PROVIDER_ID`, `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID`. The fullsend
workflows pass them to every agent run, and when any of them is set they replace the
`config.yaml` block entirely (the two are never mixed — set all three in one place).

## 5. Pick a GPT model for an agent

In `.fullsend/config.yaml`, put the agent on a runtime that serves OpenAI models — `pi` or
`codex` — with an OpenAI model, or run
`fullsend agent set code --fullsend-dir .fullsend --runtime pi --model openai/gpt-5.6-luna`
(swap in `--runtime codex` for codex), which writes the same entry after validating it:

```yaml
agents:
  - name: code
    runtime: pi # or codex
    model: openai/gpt-5.6-luna
```

The credential path below is identical either way: the same run-scoped provider carries the token,
and only how the agent process reads it differs. On codex the model must be an OpenAI id — the
Claude aliases are refused — and a repo-wide default can be set once with `FULLSEND_CODEX_MODEL`
([Codex › Models](../../runtimes/codex.md#models)).

The agent's harness must also declare the provider:

```yaml
providers:
  - openai
```

Declaring it costs nothing on runs that do not use it: the run-scoped provider is created only
when the selected runtime will actually call OpenAI (codex, or pi on an `openai/` model), so the
same harness can carry the provider for every runtime — a Vertex run notes that the declared
provider was skipped and needs no OpenAI credential.

A custom agent (a `source:` entry) declares it on its own harness; the built-in fleet agents gain
it with the GPT pilot in the fullsend-ai/agents repository. `providers/openai.yaml` arrives with
the other upstream defaults when a run prepares its workspace, and both it and the matching profile
are built into fullsend — a local run needs nothing on disk, and you commit neither. The profile lets the sandbox reach `api.openai.com` for the Responses API and
nothing else. Use a model id from OpenAI's catalog — on pi, `pi --list-models openai` in the sandbox image
prints the ones it knows; `gpt-5.6-luna` is the inexpensive
reasoning model and `gpt-5.6-sol` the capable one, and a model the mapping's project cannot use is
refused at the first call, not at setup. A sensible starting point is to put GPT on the agents that
execute work (for example `code`, `fix` or `triage`) and keep the stages that decide what happens
on your fleet default — adjust to your own fleet's roles and budget.

Trigger a run. In the run log you will see the credential being resolved (`WIF: identity provider
…, service account …, expires in …, scope …`), the run-scoped provider being created, refreshed
while the run lasts and, at the end, deleted.

## Run it locally

Your laptop has no OIDC endpoint, so use an API key from the same project — in the environment of
the fullsend command, never in the harness or the sandbox. An env file keeps it out of your shell
history:

```bash
# fullsend-openai.env
OPENAI_API_KEY=sk-...
```

```bash
fullsend run triage --runtime pi --model openai/gpt-5.6-luna \
  --env-file fullsend-openai.env --env-file fullsend-triage.env ...
```

`--runtime codex` takes the same key and the same harness requirements.

The key still goes through the gateway placeholder, so the sandbox does not see it, and the
provider it lands in expires an hour after the run ends at the latest. A committed `inference.openai`
block (step 4) is not used on your machine while `OPENAI_API_KEY` is set — there is no GitHub OIDC
endpoint to exchange with — so the same checkout works in CI and locally. See
[Running agents locally](../user/running-agents-locally.md#get-an-openai-key-gpt-on-pi-or-codex).

The fleet's agents already declare a sandbox policy. If you run a **custom harness**, give it one
too — `policy: policies/base.yaml`, the fleet's base policy from the agents repository (it sets
only filesystem and process rules; network access comes from the providers). Without it the
sandbox image's default policy also allows `api.openai.com` as an uninspected tunnel, and the
gateway then refuses to hand the credential over that route; fullsend stops the run before the
agent starts and names the rule.

## How it stays safe

- **No stored secret.** The chain starts with a token GitHub mints for one job and ends with an
  OpenAI token that lives minutes (an hour at most) and cannot call the Admin API.
- **Nothing in the sandbox.** The agent process only holds a placeholder. The real token sits in a
  provider that belongs to this run alone, gets a recorded expiry, is refreshed before it runs out,
  and is removed when the run ends (a sandbox kept with `--keep-sandbox` has its credential expired
  instead).
- **Redacted everywhere.** The token is masked in the run output and in the Actions log; the three
  identifiers never reach the sandbox or your scripts.
- **Tamper-proof start.** pi refuses to start if its config directory could redirect or replace the
  credential (`models.json`, or an `auth.json` that is anything but pi's own empty file or the
  placeholder entry fullsend seeds). fullsend writes that entry itself before every iteration and
  again after every credential refresh — pi re-reads it per request, which is how a running
  iteration follows a refresh.
- **Bound to one endpoint.** The gateway substitutes the placeholder only on requests to
  `api.openai.com` `/v1/responses` — the endpoint the profile names — so even a compromised agent
  cannot get the token resolved against another host its policy allows. The design and what was
  verified against OpenShell 0.0.115 are in
  [ADR 0092](../../ADRs/0092-openai-wif-credential-delivery.md).

## Troubleshooting

| What you see | What to do |
|---|---|
| `no OpenAI credential: set FULLSEND_OPENAI_AUDIENCE, …` | Run step 4 (or add the three variables), or bump the workflow pin to a release that includes this feature. |
| `OpenAI WIF is partially configured: missing …` / `inference.openai in config.yaml is partially configured` | One value is empty in the place you chose (variables, or `config.yaml`). Fill it in — fullsend will not silently fall back to an API key, and it does not mix the two sources. |
| `… the job has no GitHub OIDC endpoint` | This is not a GitHub Actions job, or `permissions: id-token: write` is missing from the workflow. On GitLab CI or locally, use an API key. |
| `OpenAI WIF exchange failed: … token endpoint returned 4xx` | The mapping does not match this run. Check the audience first (one character off is enough), then compare the claims from step 1 with the mapping's assertions (route B: with the administrator). Works in one repository but not another → that repository has no mapping yet. |
| Exchange fails with 4xx on a PR-review-triggered run | The mapping has a `ref` assertion (e.g. `refs/heads/main`) but `pull_request_review` events carry `refs/pull/<N>/merge`. Either remove the `ref` assertion (the default since this release) or add a second mapping asserting `ref` = `refs/pull/*` — regenerate with `fullsend inference openai request --ref refs/heads/main` to get both. |
| Exchange succeeded, the model call was refused | The mapping's permissions do not cover the call, or the project cannot use that model. The `scope` in the exchange response shows what was granted. |
| `OpenAI WIF token refused: the service-account mapping grants …` | The mapping grants more than model access. Narrow its permissions to `api.model.request` (route A: edit the mapping in A3; route B: ask your administrator); fullsend will not run an agent with a broader token. |
| `the service-account mapping does not narrow permissions` (warning) | The mapping has no permission restriction, so the token holds whatever the service account holds. Add `api.model.request` on the mapping. |
| `OPENAI_API_KEY in the sandbox is not a gateway placeholder` | A real key reached the sandbox environment by some other route (an env file copied into the sandbox, for example). Remove it; the provider is the only supported way in. |
| `pi config dir has models.json or an auth.json that is not the runner-seeded openai placeholder` | Something wrote into pi's config directory between iterations. Re-run with `--keep-sandbox` and inspect it. |
| `sandbox policy rule codex allows api.openai.com:443 without L7 inspection` | The harness has no `policy:`, so the sandbox image's default policy applies. Add `policy: policies/base.yaml` to the harness (see [Run it locally](#run-it-locally)). |
| `500 credential_unavailable` from `api.openai.com` | The placeholder pi sent no longer resolves: the credential expired, or the provider was replaced. With `--keep-sandbox` this is expected after the run ends. Otherwise check the refresh lines in the run log. |
| `401` or `500` from `api.openai.com` partway through a run | The refresh could not get a new token in time (the run log shows the attempts) and the credential expired as designed. Check the exchange errors above and re-run. |

## Related

- [pi runtime](../../runtimes/pi.md) — models, providers and behaviour differences
- [codex runtime](../../runtimes/codex.md) — the other runtime that serves OpenAI models
- [Running agents locally](../user/running-agents-locally.md)
- [ADR 0092](../../ADRs/0092-openai-wif-credential-delivery.md) — design and accepted risks
- [OpenAI: Workload identity federation for GitHub Actions](https://developers.openai.com/api/docs/guides/workload-identity-federation/github-actions)
