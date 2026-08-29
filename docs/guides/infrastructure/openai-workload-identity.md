# OpenAI Workload Identity

Run GPT models on the [pi runtime](../../runtimes/pi.md) without storing an OpenAI API key
anywhere. Your GitHub Actions job proves who it is with its OIDC token, OpenAI hands back a
short-lived token (minutes — it never outlives the GitHub token it came from, and an hour at most),
fullsend renews it for as long as the run lasts, and the agent sandbox never sees any of it — it only
ever holds a placeholder that the OpenShell gateway swaps for the real token on the way out.

You set this up once per OpenAI organization and once per repository. It takes one visit to the
OpenAI console and three repository variables. No key is created, downloaded or rotated.

> **GitHub Actions only.** The exchange needs the job's OIDC endpoint. For GitLab CI and for runs
> on your own machine, use an API key in the runner environment — see [Run it locally](#run-it-locally).

## Before you start

- [ ] You can manage **Workload Identity Providers** in your OpenAI organization
      (Organization Settings → Security). This is an organization-level permission; being an owner
      of a project is not enough.
- [ ] You have an OpenAI **project** for the runs, ideally a dedicated one with a budget alert.
- [ ] The repository is enrolled per-repo on a fullsend release that includes this feature
      (the release notes for #6689 name it).

## 1. Add the identity provider (once per organization)

In the OpenAI console go to **Organization Settings → Security → Workload Identity Provider** and
add a provider:

| Field | Enter |
|---|---|
| OIDC issuer URL | `https://token.actions.githubusercontent.com` |
| Audience | `fullsend://<your-github-org>` (or any string you choose — you will use it verbatim below) |
| Use uploaded JWKS for token verification | **Off** |

Copy the **identity provider ID** — you need it in step 4.

The audience is not issued by anyone: whatever you type here is what your runs will request,
character for character. Pick it once and reuse it for every repository.

## 2. See what your repository actually claims (once)

OpenAI decides whether to trust a run by comparing claims in the GitHub token with the mapping you
write in step 3. A wrong audience and a wrong claim fail the same way, so look at the real values
before writing the mapping. Add this temporary workflow to the repository, run it, and copy the
printed claims:

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
          AUD: fullsend://<your-github-org>
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
  "aud": "fullsend://<your-github-org>",
  "repository": "<your-github-org>/<repo>",
  "ref": "refs/heads/main",
  "workflow_ref": "<your-github-org>/<repo>/.github/workflows/openai-wif-check.yml@refs/heads/main",
  "job_workflow_ref": "<your-github-org>/<repo>/.github/workflows/openai-wif-check.yml@refs/heads/main"
}
```

For the real agent runs, `repository` and `ref` are the same as above; `workflow_ref` names whichever
of the fullsend workflows installed in your repository started the run (`code.yml`, `triage.yml`,
`review.yml`, `fix.yml`, `prioritize.yml`, `retro.yml` or `dispatch.yml` under `.github/workflows/`),
and `job_workflow_ref` the pinned fullsend reusable workflow it calls. Delete the check workflow when
you are done.

## 3. Map the repository to a service account (once per repository)

Open the provider you created and add a **service account mapping**:

| Field | Enter |
|---|---|
| Claim assertions | `iss` = `https://token.actions.githubusercontent.com` · `aud` = your audience · `repository` = `<your-github-org>/<repo>` · `ref` = `refs/heads/main` |
| Project | the project the runs should be billed to |
| Service account | create a new one, for example `fullsend-<repo>-ci` |
| Permissions | `api.model.request` (fullsend also accepts `api.model.read`; anything broader is refused at run time) |

Every assertion must match what step 2 printed character for character; a one-letter difference
fails every exchange. Do not assert `workflow_ref`: fullsend runs agents from seven workflow files
(see step 2), so a single `workflow_ref` value would exclude all but one of them. If you want that
narrowing anyway, create one mapping per workflow file.

Copy the **service account ID** — you need it in step 4.

Two things not to do:

- **Do not create an API key** for the service account. The mapping is the credential; a key would
  put a long-lived secret back into the picture.
- **Do not assert `repository_owner` instead of `repository`.** That would trust every repository
  in your GitHub organization, including ones anybody with repository-create rights can add.

OpenAI allows 50 mappings per provider and 50 providers per organization, and mappings are created
in the console only. Treat enrolment as a deliberate step per repository.

**Which runs this mapping trusts.** `ref` = `refs/heads/main` covers every fullsend agent workflow
as installed on `main`, and GitHub mints the token for the repository the job runs in — so the
agent jobs fullsend starts for pull requests from forks are covered too (they run in your
repository, not the fork). What keeps an untrusted actor from starting such a run is fullsend's own
dispatch authorization (only events from people with write access trigger agents), not the mapping.
If you want the mapping itself to exclude some triggers, add a claim the untrusted runs cannot
carry — for example a GitHub `environment` that the agent jobs reference and that protects itself
with required reviewers — and assert `environment == "<name>"` here. A `workflow_dispatch` of a
fullsend workflow on a feature branch will not match `refs/heads/main`; that is expected. In the
org-wide installation mode the runs happen in the organization's central `.fullsend` repository,
so assert that repository instead.

## 4. Tell fullsend the three identifiers

Re-run the setup command you enrolled the repository with, adding the three values:

```bash
fullsend github setup <your-github-org>/<repo> \
  --openai-audience "fullsend://<your-github-org>" \
  --openai-identity-provider-id "<identity provider ID from step 1>" \
  --openai-service-account-id "<service account ID from step 3>"
```

It writes them into the repository's `.fullsend/config.yaml`, the same way it records the Vertex
project and provider:

```yaml
inference:
  openai:
    audience: fullsend://<your-github-org>
    identity_provider_id: <identity provider ID>
    service_account_id: <service account ID>
```

Commit that change (setup opens a pull request for it unless you pass `--direct`). A base
configuration (`config.base.yaml`, or a vendor preset) can carry the block for many repositories,
and a repository can restate any one of the three.

**Is it safe to commit these?** Yes. They are identifiers, not secrets: on their own they grant
nothing. OpenAI issues a token only to a caller presenting a GitHub OIDC token whose claims match
the mapping from step 3, and only your repository's `main` workflow can obtain one. fullsend reads
`.fullsend/config.yaml` from the base branch for pull-request events, so a pull request cannot
change them for the run that reviews it. The worst thing someone with write access could do is
point the block at their own OpenAI organization — and pay for your runs. fullsend prints the three
values in the run log so you can always see which mapping a run used.

If you would rather keep them out of the repository, set them as **repository variables** instead
(Settings → Secrets and variables → Actions → Variables): `FULLSEND_OPENAI_AUDIENCE`,
`FULLSEND_OPENAI_IDENTITY_PROVIDER_ID`, `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID`. The fullsend
workflows pass them to every agent run, and when any of them is set they replace the
`config.yaml` block entirely (the two are never mixed — set all three in one place).

## 5. Pick a GPT model for an agent

In `.fullsend/config.yaml`, put the agent on pi with an OpenAI model — or run
`fullsend agent set code --fullsend-dir .fullsend --runtime pi --model openai/gpt-5.6-luna`, which
writes the same entry after validating it:

```yaml
agents:
  - name: code
    runtime: pi
    model: openai/gpt-5.6-luna
```

The agent's harness must also declare the provider:

```yaml
providers:
  - openai
```

A custom agent (a `source:` entry) declares it on its own harness; the built-in fleet agents gain
it with the GPT pilot in the fullsend-ai/agents repository. `providers/openai.yaml` arrives with
the other upstream defaults when a run prepares its workspace, and both it and the matching profile
are built into fullsend — a local run needs nothing on disk, and you commit neither. The profile lets the sandbox reach `api.openai.com` for the Responses API and
nothing else. Use a model id from pi's OpenAI catalog
(`pi --list-models openai` in the sandbox image prints it); `gpt-5.6-luna` is the inexpensive
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

The key still goes through the gateway placeholder, so the sandbox does not see it, and the
provider it lands in expires an hour after the run ends at the latest. A committed `inference.openai`
block (step 4) is not used on your machine while `OPENAI_API_KEY` is set — there is no GitHub OIDC
endpoint to exchange with — so the same checkout works in CI and locally. See
[Running agents locally](../user/running-agents-locally.md#get-an-openai-key-gpt-on-pi-only).

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
| `OpenAI WIF exchange failed: … token endpoint returned 4xx` | The mapping does not match this run. Check the audience first (one character off is enough), then compare the claims from step 2 with the mapping's assertions. Works on `main` but not on a pull request → the `ref`/`workflow_ref` differ. Works in one repository but not another → that repository has no mapping yet. |
| Exchange succeeded, the model call was refused | The mapping's permissions do not cover the call, or the project cannot use that model. The `scope` in the exchange response shows what was granted. |
| `OpenAI WIF token refused: the service-account mapping grants …` | The mapping grants more than model access. Narrow its permissions to `api.model.request` (step 3); fullsend will not run an agent with a broader token. |
| `the service-account mapping does not narrow permissions` (warning) | The mapping has no permission restriction, so the token holds whatever the service account holds. Add `api.model.request` on the mapping. |
| `OPENAI_API_KEY in the sandbox is not a gateway placeholder` | A real key reached the sandbox environment by some other route (an env file copied into the sandbox, for example). Remove it; the provider is the only supported way in. |
| `pi config dir has models.json or an auth.json that is not the runner-seeded openai placeholder` | Something wrote into pi's config directory between iterations. Re-run with `--keep-sandbox` and inspect it. |
| `sandbox policy rule codex allows api.openai.com:443 without L7 inspection` | The harness has no `policy:`, so the sandbox image's default policy applies. Add `policy: policies/base.yaml` to the harness (see [Run it locally](#run-it-locally)). |
| `500 credential_unavailable` from `api.openai.com` | The placeholder pi sent no longer resolves: the credential expired, or the provider was replaced. With `--keep-sandbox` this is expected after the run ends. Otherwise check the refresh lines in the run log. |
| `401` or `500` from `api.openai.com` partway through a run | The refresh could not get a new token in time (the run log shows the attempts) and the credential expired as designed. Check the exchange errors above and re-run. |

## Related

- [pi runtime](../../runtimes/pi.md) — models, providers and behaviour differences
- [Running agents locally](../user/running-agents-locally.md)
- [ADR 0092](../../ADRs/0092-openai-wif-credential-delivery.md) — design and accepted risks
- [OpenAI: Workload identity federation for GitHub Actions](https://developers.openai.com/api/docs/guides/workload-identity-federation/github-actions)
