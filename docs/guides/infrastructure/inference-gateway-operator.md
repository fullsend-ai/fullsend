# Run an inference gateway that trusts GitHub Actions OIDC

<!--
Maintainers: the pre-commit hook check-gateway-doc-configs (hack/check-gateway-doc-configs)
validates the agentgateway config blocks on this page, with examples/inference-gateway/echo-upstream.yaml,
using agentgateway's own config check at the version this page names. It also checks that the
workflow excerpt matches examples/inference-gateway/gateway-check.yml, which actionlint lints.
Every yaml block on this page needs a "gateway-doc-check:" marker comment just above it.
-->

This guide is for the **platform operator** who runs the inference gateway that fullsend's agents
call (pi, codex and Claude Code). When you finish, you have an [agentgateway](https://github.com/agentgateway/agentgateway)
service on Cloud Run that:

- accepts a GitHub Actions job's OIDC token as its only credential, and only for the repositories
  you list;
- holds the upstream model credential itself, so no forge secret stores a reusable key;
- refuses an expired token with `401` (agentgateway's `jwtAuth` does, after a 60 s leeway). Claude
  Code recovers from a stale credential only on a `401` or `403`, never on a `5xx`;
- serves the three APIs the runtimes use: `/v1/messages` (pi, Claude Code), `/v1/chat/completions`
  (pi) and `/v1/responses` (pi, codex).

> **Security boundary: securing the credential becomes your job.** The gateway
> keeps model keys out of every repository, but the credential still exists: the
> upstream key in Secret Manager, and the runtime service account's access to
> Vertex AI. Both live in your GCP project, and protecting them is your job as
> the operator:
>
> - Anyone who can deploy a revision that runs as the runtime service account
>   (`roles/run.developer` on the service plus `roles/iam.serviceAccountUser` on
>   the account) can read the upstream key and call Vertex AI as the gateway.
>   Treat those roles as holding the key.
> - Anyone who can read the key secret (`roles/secretmanager.secretAccessor` or
>   an admin role on it or the project), or act as the runtime service account
>   by any other route (another Cloud Run service or job, token creation,
>   service account keys), also holds the key.
> - Anyone who can add a version to the `gateway-config` secret also holds the
>   key. The config decides which repositories the gateway serves and which
>   signing keys (`jwks`) it trusts, and also where each model sends the
>   upstream key: a model whose `baseUrl` points at the writer's own server,
>   with `apiKey.file` naming the mounted key, hands the key over on the first
>   call. A revision that mounts `latest` loads a new version on its next start,
>   with no deploy needed. Let the people who approve enrolments edit only the
>   allowlist, and have a trusted step generate, check and roll out the full
>   config.
> - Project owners and editors hold all of these. Keep that list short, and
>   review the project's IAM and audit logs as you would review access to the
>   key itself.

The runner side (how a fullsend run fetches the token and points the agent's runtime at the gateway) is in the
[user guide for the hosted gateway route](../user/inference-gateway.md).
This page covers only the gateway. It uses agentgateway v1.7.0, and every config block on it is
validated against that release.

> **See it in practice.** The fullsend-ai organization runs its own gateway this
> way. Its config and operations live in the
> [fullsend-ai/org inference gateway README](https://github.com/fullsend-ai/org/blob/main/inference-gateway/README.md).

## What you end up with

You hand these three values to the people who enrol repositories. None of them is a secret.

| Value | Example in this guide | What it is |
|---|---|---|
| **Gateway URL** | `https://gateway.example.com` | The Cloud Run service URL, or your own domain in front of it. Behind a shared ingress it can carry a path prefix ([Serve the gateway under a path prefix](#serve-the-gateway-under-a-path-prefix)). |
| **Audience** | `fullsend-inference` | The string every job asks GitHub to put in its token's `aud` claim. The gateway refuses any other value. You choose it; it has no required format. |
| **Allowed repositories** | `example-org/example-repo` | The `owner/name` of each repository the gateway serves, matched exactly against the token's `repository` claim. |

## Prerequisites

- A GCP project where you can create Artifact Registry repositories, service accounts, Secret
  Manager secrets and Cloud Run services, and grant project IAM roles.
- `gcloud`, signed in to that project.
- [`skopeo`](https://github.com/containers/skopeo), to copy the release image.
- `podman` or `docker`, to validate the config before deploying (optional but recommended).
- For a Vertex AI upstream: the models you plan to serve enabled in the project. Model
  availability varies by project.
- A repository where you can add a workflow, to run the checks in [step 8](#8-check-the-gateway-from-github-actions).

Set the names once. Every command below uses them:

```bash
export PROJECT=example-project
export REGION=us-east5
export AR_REPO=example-images
export SERVICE=inference-gateway
export SA_NAME=gateway-runtime
export SA="$SA_NAME@$PROJECT.iam.gserviceaccount.com"
export AUDIENCE=fullsend-inference
```

## 1. Copy the release image into Artifact Registry

Cloud Run can't pull from `ghcr.io`, so copy the agentgateway release image into your project
unchanged.

```bash
gcloud artifacts repositories create "$AR_REPO" --project "$PROJECT" --location "$REGION" \
  --repository-format docker
skopeo copy --all --quiet \
  --dest-creds "oauth2accesstoken:$(gcloud auth print-access-token)" \
  docker://ghcr.io/agentgateway/agentgateway:v1.7.0 \
  "docker://$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.7.0"
```

```text
Create request issued for: [example-images]
Waiting for operation [projects/example-project/locations/us-east5/operations/...] to complete...
.....done.
Created repository [example-images].
```

`--all` copies every architecture in the release. Check that the copy is byte-for-byte the release:

```bash
skopeo inspect --raw docker://ghcr.io/agentgateway/agentgateway:v1.7.0 | shasum -a 256
skopeo inspect --raw --creds "oauth2accesstoken:$(gcloud auth print-access-token)" \
  "docker://$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.7.0" | shasum -a 256
```

The release's digest:

```text
60216aafb03d37f94e952ff3d51d2f630a65b10ecaebdc0a864882f92610c45b  -
```

Both commands must print this digest.

## 2. Create the runtime service account

The gateway calls Vertex AI as its own service account. No key file is created.

```bash
gcloud iam service-accounts create "$SA_NAME" --project "$PROJECT" \
  --display-name "inference gateway runtime"
gcloud projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$SA" \
  --role roles/aiplatform.user --condition=None --format='value(etag)'
```

```text
Created service account [gateway-runtime].
Service account email: gateway-runtime@example-project.iam.gserviceaccount.com
Updated IAM policy for project [example-project].
BwZd...
```

Skip the `roles/aiplatform.user` binding if you serve no Vertex models.

## 3. Write the gateway config

Save this as `config.tmpl.yaml`. Each rule in the comments is required, and the reason is on the line
it applies to.

<!-- gateway-doc-check: base -->
```yaml
gateways:
  default: { port: 8080, bindAddress: 0.0.0.0 }
llm:
  gateways: [default]
  discovery: disabled
  policies:
    jwtAuth:
      mode: strict                                   # a request without a valid token is refused
      providers:
      - issuer: https://token.actions.githubusercontent.com
        audiences: [AUDIENCE]                        # a fixed audience: a token minted for another service is refused
        jwks: { url: https://token.actions.githubusercontent.com/.well-known/jwks }   # remote JWKS: GitHub rotates its keys
  models:
  - name: claude-sonnet-4-6
    provider: vertex
    params: { vertexProject: VERTEX_PROJECT, vertexRegion: global }
    requestHeaders: { remove: [x-api-key] }          # the gateway forwards other caller headers unchanged
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'    # exact match; never a prefix or owner-only match
  - name: gemini-2.5-flash
    provider: vertex
    params: { vertexProject: VERTEX_PROJECT, vertexRegion: global }
    requestHeaders: { remove: [x-api-key] }
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'
```

What the config does, and what it must not do:

- **Validate the token, then drop it.** `jwtAuth` checks the signature against GitHub's published
  keys, the issuer, the audience and the expiry. After that, the caller's `Authorization` header is
  not forwarded upstream; the gateway sends its own credential instead. This is the default. Never
  set `preserveToken: true` or a `passthrough` backend auth: either one hands the job's token to the
  model provider.
- **Authorise per repository.** Each model's `authorization` rule decides who may call it, and
  `/v1/models` lists for each caller only the models it may call. Allow more repositories with `||`:
  `jwt.repository == "example-org/a" || jwt.repository == "example-org/b"`.
- **Give every model an `allow` rule that tests the repository.** Any GitHub repository can mint a
  token for your audience and pass `jwtAuth`, so the `authorization` rules are the only thing
  that refuses one. agentgateway lets every such caller through when a model has:
  - no `authorization` block, or an empty `rules` list;
  - only `deny` rules: everything not denied is allowed;
  - an `allow` rule that does not test the repository, such as `'true'`. A bare string in
    `rules` (`- 'true'`) is an `allow` rule;
  - only `require` rules, none of which tests the repository.
- **Pin the repository ids too, if a name may be reused.** GitHub's token also carries the
  numeric `repository_id` and `repository_owner_id` claims, sent as strings
  (`jwt.repository_id == "123"`, not `== 123`). The ids stay with a repository through a rename;
  a new repository that reuses an old name gets a new `repository_id`. Matching the ids as well
  stops a reused name from inheriting access, and a renamed repository is refused until you
  update its entry. This walkthrough matches the name only.
- **Strip `x-api-key`.** agentgateway removes the credential it validated, but passes every other
  caller header through to the upstream, `x-api-key` included ([step 9](#9-check-that-no-caller-credential-reaches-the-upstream)
  shows both cases). Add `requestHeaders.remove` to every model.
- **Pick the Vertex location your project serves the model in.** This walkthrough used `global`
  for both models. Model availability varies by project and location.

Fill in the placeholders:

```bash
sed -e "s/VERTEX_PROJECT/$PROJECT/g" -e "s/AUDIENCE/$AUDIENCE/" \
    -e "s#ALLOWED_REPO#example-org/example-repo#g" config.tmpl.yaml > config.yaml
```

This is enough for a first deploy. Once you serve more than a few repositories, keep the allowed
repositories in a file of their own and generate `config.yaml` from it with a script. The script
should refuse a config that breaks the rules above, such as a model whose `authorization` rules do
not test the repository. Then every change to the allowlist is a one-line, reviewable diff.

### An upstream that needs an API key

For a provider that takes a key rather than the service account (OpenAI, for example), the gateway
reads the key from a file. You mount that file from Secret Manager in [step 6](#6-deploy-to-cloud-run).

<!-- gateway-doc-check: openai-model -->
```yaml
  - name: gpt-6-luna
    provider: openAI
    params: { apiKey: { file: /etc/agw-upstream/key } }
    requestHeaders: { remove: [x-api-key] }
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'
```

> **Not executed in this walkthrough:** no OpenAI key was available for this run, so the OpenAI
> model and the native `/v1/responses` path are not shown here. The file-mounted key pattern itself
> was exercised with the custody-check upstream in [step 9](#9-check-that-no-caller-credential-reaches-the-upstream).
> If a file referenced by `apiKey.file` is missing, the gateway exits at startup.

### Accept a static key as well (`auth: api-key`)

A repository that sets `auth: api-key` sends a key you issue
instead of an OIDC token, in the same `Authorization: Bearer` header. (A repository can instead
send its key as `x-api-key` on `/v1/messages` with `messages_auth_header: x-api-key`; that needs a
gateway that reads the header, see
[Header custody for an x-api-key gateway](#header-custody-for-an-x-api-key-gateway).) Serve these
repositories only if you must: a key is a long-lived secret, and a leaked key works until you
revoke it. Prefer OIDC wherever it is possible.

> **Not executed here:** this section has no live run, so no output is shown. Validate the config
> as in [step 4](#4-validate-the-config-locally) before you deploy it. To repeat the
> [step 9](#9-check-that-no-caller-credential-reaches-the-upstream) custody check with a key in
> place of the token, grant a test key the echo models for the check only: add `echo` and
> `echo-noremove` to its `allowedModels`, add its `apiKey.purpose` rule to both echo models, and
> remove both grants once the check passes.

Add an agentgateway `apiKey` policy under `llm.policies`. Store each key's SHA-256 hash, never the
key, and list the models each key may call:

<!-- gateway-doc-check: api-key-policies -->
```yaml
  policies:
    jwtAuth:
      mode: permissive                               # only on a gateway that serves both modes; see below
      providers:
      - issuer: https://token.actions.githubusercontent.com
        audiences: [AUDIENCE]
        jwks: { url: https://token.actions.githubusercontent.com/.well-known/jwks }
    apiKey:
      mode: optional
      keys:
      - keyHash: "sha256:KEY_HASH"                   # the key's hex SHA-256 digest, never the key itself
        metadata: { name: example-repo, purpose: example-repo-key }
        allowedModels: [claude-sonnet-4-6]           # the models this key may call
```

Admit the key on each model it may call, with a second rule next to the repository rule:

<!-- gateway-doc-check: api-key-authorization -->
```yaml
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'
      - allow: 'apiKey.purpose == "example-repo-key"'
```

- **Strip the caller's key before the upstream, as you strip the bearer.** The key must never
  reach the model provider. Keep `requestHeaders.remove: [x-api-key]` on every model, never
  forward the caller's `Authorization` header, and check with the step 9 custody models (with the
  temporary test-key grants above) that the upstream sees only the gateway's own key.
- **A gateway that serves only OIDC keeps strict `jwtAuth`.** Leave out the `apiKey` policy, keep
  `mode: strict` as in [step 3](#3-write-the-gateway-config), and the gateway stays as tested above.
- **Serving both modes on one gateway weakens authentication.** `jwtAuth` must be `permissive`
  so that a key, which is not a JWT, gets past it, and `apiKey` must be `optional` so that a
  request authenticated by its token, which carries no key, gets past the key check. A request
  with no credential at all then passes both and reaches the per-model `authorization` rules.
  Those rules are the only thing that refuses it, so every model must have them, and none may
  admit an anonymous caller. Where you can, run a separate gateway for `api-key` repositories, or
  keep the gateway OIDC-only with strict `jwtAuth`.

## 4. Validate the config locally

```bash
mkdir -p v/config v/upstream && cp config.yaml v/config/ && chmod -R a+rX v
podman run --rm -v "$PWD/v/config:/etc/agw-config:ro" -v "$PWD/v/upstream:/etc/agw-upstream:ro" \
  ghcr.io/agentgateway/agentgateway:v1.7.0 -f /etc/agw-config/config.yaml --validate-only
```

```text
Configuration is valid!
```

Both the config above and the version with the custody-check models from
[step 9](#9-check-that-no-caller-credential-reaches-the-upstream) returned this. The container runs as a non-root user, so the mounted files must be world-readable. On an Apple
silicon Mac, add `--platform linux/arm64`; amd64 emulation hangs. If you configured an
`apiKey.file`, put a placeholder file at that path in `v/upstream/` first.

## 5. Store the config and the upstream key in Secret Manager

```bash
gcloud secrets create gateway-config --project "$PROJECT" --replication-policy automatic \
  --data-file=config.yaml
gcloud secrets create gateway-upstream-key --project "$PROJECT" --replication-policy automatic \
  --data-file=upstream-key
for s in gateway-config gateway-upstream-key; do
  gcloud secrets add-iam-policy-binding "$s" --project "$PROJECT" --member "serviceAccount:$SA" \
    --role roles/secretmanager.secretAccessor --format='value(bindings)'
done
```

```text
Created version [1] of the secret [gateway-config].
Created version [1] of the secret [gateway-upstream-key].
Updated IAM policy for secret [gateway-config].
{'members': ['serviceAccount:gateway-runtime@example-project.iam.gserviceaccount.com'], 'role': 'roles/secretmanager.secretAccessor'}
Updated IAM policy for secret [gateway-upstream-key].
{'members': ['serviceAccount:gateway-runtime@example-project.iam.gserviceaccount.com'], 'role': 'roles/secretmanager.secretAccessor'}
```

`upstream-key` is the file holding your provider key, or the throwaway key from
[step 9](#9-check-that-no-caller-credential-reaches-the-upstream). Create it with the provider's
console or CLI, and delete the local copy once the secret exists.

**If every model uses the service account** (Vertex only, no custody check), skip
`gateway-upstream-key`: create and bind only `gateway-config`, and in step 6 drop
`,/etc/agw-upstream/key=gateway-upstream-key:latest` from `--set-secrets`.

## 6. Deploy to Cloud Run

```bash
gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --image "$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.7.0" \
  --service-account "$SA" --port 8080 \
  --allow-unauthenticated --no-invoker-iam-check \
  --cpu 1 --memory 512Mi --max-instances 1 \
  --args=-f,/etc/agw-config/config.yaml \
  --set-secrets "/etc/agw-config/config.yaml=gateway-config:latest,/etc/agw-upstream/key=gateway-upstream-key:latest"
```

```text
Deploying container to Cloud Run service [inference-gateway] in project [example-project] region [us-east5]
Deploying new service...
Setting IAM Policy...........done
Creating Revision...................................................done
Routing traffic.....done
Done.
Service [inference-gateway] revision [inference-gateway-00001-abc] has been deployed and is serving 100 percent of traffic.
Service URL: https://gateway.example.com
```

- **`--no-invoker-iam-check` is required.** The gateway does its own authentication. Without this
  flag, Cloud Run's front end tries to verify every `Authorization: Bearer` as a Google token and
  rejects a GitHub token with an HTML `401` before it reaches the gateway, even with
  `--allow-unauthenticated`. This is masked for the first minutes after a deploy while IAM settles.
- **Each secret gets its own directory.** Cloud Run mounts one secret per directory.
- **`latest` is for the first deploy only.** This revision reads whichever config version is
  newest, so it is not a rollback target. Every later change mounts a version number; see
  [Change the config](#change-the-config).
- Size the service for your load. The values above are what this walkthrough ran with. See
  [Watch health and capacity](#watch-health-and-capacity) for what to measure.

Check that the gateway, not Cloud Run, answers a non-Google bearer:

```bash
curl -s -i https://gateway.example.com/v1/models -H 'authorization: Bearer not-a-google-token'  # gitleaks:allow
```

```text
HTTP/2 401
content-type: text/plain
server: Google Frontend
content-length: 21

authentication failed
```

A `401` with a `text/plain` body comes from agentgateway. It returns `authentication failed` for
every `jwtAuth` refusal, and writes the reason to its access log (see
[Troubleshooting](#troubleshooting)). A `text/html` body means the invoker check is still on.

## 7. Know what a healthy start looks like

```bash
gcloud logging read "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"$SERVICE\" AND textPayload:\"started\"" \
  --project "$PROJECT" --freshness 30m --limit 5 --format 'value(textPayload)'
```

```text
2026-10-09T22:47:31.427378Z	info	proxy::gateway	started bind	bind="bind/8080"
2026-10-09T22:47:31.427319Z	info	proxy::gateway	started bind	bind="bind/9000"
Starting new instance. Reason: DEPLOYMENT_ROLLOUT - Instance started due to traffic shifting between revisions due to deployment, traffic split adjustment, or deployment health check.
```

`bind/8080` is the gateway. `bind/9000` appears only with the custody-check listener from
[step 9](#9-check-that-no-caller-credential-reaches-the-upstream). This output was recorded on
v1.6.0. v1.7.0 also logs two warnings before `started bind` when the config is mounted read-only,
as it is on Cloud Run. This is from a local run of the v1.7.0 image with the step 3 config:

```text
warn	config	failed to write generated session key	err=failed to write to file `/etc/agw-config/config.yaml`: Read-only file system (os error 30) path=/etc/agw-config/config.yaml
warn	config	no session key configured; MCP session IDs are not encrypted. Set SESSION_KEY or config.session.key
info	proxy::gateway	started bind	bind="bind/8080"
```

Both are harmless for this setup: the session key only encrypts MCP session IDs, and this gateway
serves no MCP. If you add MCP, set `SESSION_KEY` or `config.session.key`.

## 8. Check the gateway from GitHub Actions

Copy [`gateway-check.yml`](examples/inference-gateway/gateway-check.yml) to `.github/workflows/`
in an allowed repository, set a repository variable `GATEWAY_URL` to the service URL, and run it.
It mints a token for your audience and checks one prompt per API, then the refusals. The job needs
`id-token: write` to mint the token. Set `AUDIENCE` to your audience:

<!-- gateway-doc-check: excerpt examples/inference-gateway/gateway-check.yml -->
```yaml
permissions:
  id-token: write
  contents: read
jobs:
  check:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    env:
      GATEWAY_URL: ${{ vars.GATEWAY_URL }}
      AUDIENCE: fullsend-inference
```

The job's one step defines two shell helpers. `tok` fetches a token for an audience, and `call`
prints each call's label, HTTP status and the first 700 bytes of the response body. It then calls
each API, the custody-check models and the refusal cases, and finally checks whether any response
body contains the token.

```bash
gh variable set GATEWAY_URL -R example-org/example-repo -b https://gateway.example.com
gh workflow run gateway-check.yml -R example-org/example-repo
id=$(gh run list -R example-org/example-repo -w gateway-check.yml -L1 --json databaseId -q '.[0].databaseId')
gh run watch "$id" -R example-org/example-repo
gh run view "$id" -R example-org/example-repo --log
```

The `echo` calls need the custody-check models from [step 9](#9-check-that-no-caller-credential-reaches-the-upstream);
drop those lines if you skip it.

Output from the allowed repository. The claims line comes from a GitHub Actions run. The rest of
this step and the custody output in step 9 come from a local run of the v1.7.0 image. That run
used this page's config, Vertex AI credentials, and tokens carrying the same claims, signed by a
local key set that `jwtAuth` was pointed at:

```text
== claims (non-secret)
{"iss":"https://token.actions.githubusercontent.com","aud":"fullsend-inference","repository":"example-org/example-repo","ttl":300}

== GET /v1/models
HTTP 200 {"object":"list","data":[{"id":"claude-sonnet-4-6","object":"model","created":...,"owned_by":"gcp.vertex_ai"},{"id":"gemini-2.5-flash","object":"model","created":1750118400,"owned_by":"gcp.vertex_ai","name":"Gemini 2.5 Flash","context_window":1048576,"context_length":1048576,"max_input_tokens":1048576,"max_tokens":65536,"architecture":{...},"capabilities":{...},"supported_parameters":[...]},{"id":"echo-noremove",...},{"id":"echo",...}]}

== POST /v1/messages (claude-sonnet-4-6)
HTTP 200 {"id":"msg_vrtx_...","type":"message","role":"assistant","model":"claude-sonnet-4-6","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":13,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}},"content":[{"text":"pong","type":"text"}],"container":null,"stop_details":null}

== POST /v1/chat/completions (gemini-2.5-flash)
HTTP 200 {"model":"gemini-2.5-flash","service_tier":"default","usage":{"prompt_tokens":5,"completion_tokens":22,"total_tokens":27,"completion_tokens_details":{"reasoning_tokens":21}},"choices":[{"message":{"content":"pong","role":"assistant"},"index":0,"finish_reason":"stop"}],"id":"...","created":...,"object":"chat.completion"}

== POST /v1/responses (echo)
HTTP 200 {"id":"resp_...","status":"completed","output":[{"type":"message","content":[{"type":"output_text","annotations":[],"logprobs":null,"text":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":false,\"canary_present\":false}"}], ...

== POST /v1/responses (claude-sonnet-4-6)
HTTP 400 failed to process LLM request: unsupported conversion: from Responses to provider gcp.vertex_ai (supported: [AnthropicMessages])

== negative: token minted for another audience
HTTP 401 authentication failed

== negative: token only in x-api-key
HTTP 401 authentication failed

== negative: no credential
HTTP 401 authentication failed

== does any body contain the token? 0
```

- **The token lives 300 seconds** (`ttl:300`). The fullsend runner re-fetches it before it expires,
  so runs longer than that keep working.
- **`/v1/responses` works only for upstreams that accept it.** The gateway translates Responses to
  Chat Completions for a Chat-only upstream (the `echo` call), but not to Vertex Claude. Map
  Claude models to `anthropic-messages` in the runner's model list, and Gemini models to
  `openai-completions`.
- **Every `401` body reads `authentication failed`.** The reason is in the gateway's access log, as
  `error="authentication failure: ..."` with `reason=JwtAuth`; see
  [Troubleshooting](#troubleshooting).
- **`/v1/models` lists each model with its provider** in `owned_by` (`gcp.vertex_ai` here). Models
  in agentgateway's catalog, such as Gemini, also carry fields like `context_window` and
  `max_tokens`.
- The gateway's log line for each call names the model, the upstream and the caller. The repository
  appears in `jwt.sub`. Its format depends on the repository: one that uses GitHub's immutable
  subject claims logs `repo:example-org@1111/example-repo@2222:ref:...` instead. Write
  `authorization` rules on `jwt.repository` and the id claims, never by parsing `jwt.sub`:

  ```text
  info	request ... endpoint=aiplatform.googleapis.com:443 ... http.path=/v1/chat/completions http.status=200 jwt.sub=repo:example-org/example-repo:ref:refs/heads/main protocol=llm gen_ai.operation.name=chat gen_ai.provider.name=gcp.vertex_ai gen_ai.request.model=gemini-2.5-flash ...
  ```

### Check that another repository is refused

Run the same workflow, with the same audience, in a repository that is **not** in the allow rules:

```text
== claims (non-secret)
{"iss":"https://token.actions.githubusercontent.com","aud":"fullsend-inference","repository":"example-org/other-repo","ttl":300}

== GET /v1/models
HTTP 200 {"object":"list","data":[]}

== POST /v1/messages (claude-sonnet-4-6)
HTTP 403 {"error":{"message":"Model authorization denied","type":"invalid_request_error","code":"model_authorization_denied"}}

== POST /v1/chat/completions (gemini-2.5-flash)
HTTP 403 {"error":{"message":"Model authorization denied","type":"invalid_request_error","code":"model_authorization_denied"}}
```

A valid GitHub token from the wrong repository gets an empty model list and `403` on every model.
Every other call in that run also returned `403 model_authorization_denied`, and the audience and
missing-token negatives returned the same `401`s as above.

## 9. Check that no caller credential reaches the upstream

> **Order:** this step changes the gateway config and adds a throwaway upstream key that the
> echo listener compares against. Apply both before steps 4–6 (validate, store, deploy), so the
> deployed service mounts that key and step 8's custody check can pass.

This optional check adds two models whose upstream is a second listener inside the same container.
It answers every request with what it received: whether `authorization` equals the gateway's own
upstream key (`GATEWAY_KEY`), and whether `x-api-key` and a canary header arrived. Its key is read
from the mounted `gateway-upstream-key` file, so it also proves the file-mounted key works.

Append the `binds` block in [`echo-upstream.yaml`](examples/inference-gateway/echo-upstream.yaml)
to `config.tmpl.yaml`, at the top level. It defines the echo listener on port 9000 as a
`directResponse` route, whose CEL body reports what the request carried. Then add the two models
under `llm.models`:

<!-- gateway-doc-check: custody-models -->
```yaml
  - name: echo
    provider: { custom: { formats: [ { type: completions } ] } }
    params: { baseUrl: http://127.0.0.1:9000/v1, apiKey: { file: /etc/agw-upstream/key } }
    requestHeaders: { remove: [x-api-key] }
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'
  - name: echo-noremove
    provider: { custom: { formats: [ { type: completions } ] } }
    params: { baseUrl: http://127.0.0.1:9000/v1, apiKey: { file: /etc/agw-upstream/key } }
    authorization:
      rules:
      - allow: 'jwt.repository == "ALLOWED_REPO"'
```

Generate a throwaway key for the check and put it in both places. The config is itself stored as a
secret:

```bash
openssl rand -hex 16 > upstream-key
sed -e "s/VERTEX_PROJECT/$PROJECT/g" -e "s/AUDIENCE/$AUDIENCE/" -e "s/ECHO_KEY/$(cat upstream-key)/" \
    -e "s#ALLOWED_REPO#example-org/example-repo#g" config.tmpl.yaml > config.yaml
```

The workflow in step 8 sends a valid bearer plus `x-api-key: canary` and `x-canary: 1`:

```text
== custody: echo, requestHeaders.remove [x-api-key]
HTTP 200 {... "content":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":false,\"canary_present\":true}" ...}

== custody: echo-noremove, no requestHeaders
HTTP 200 {... "content":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":true,\"canary_present\":true}" ...}
```

- `authorization` is `GATEWAY_KEY` in both: the job's token was replaced by the gateway's own key.
- `x-api-key` reached the upstream **only** on the model without `requestHeaders.remove`.
- The canary reached the upstream in both: other caller headers pass through unchanged.

Remove the two `echo` models and the `binds` block once the check passes, then roll the change
out as described in [Change the config](#change-the-config).

## Serve the gateway under a path prefix

> **Not executed in this walkthrough:** the steps above give the gateway its own origin. This
> section describes what fullsend expects when it shares one; the ingress configuration depends on
> your load balancer and is not shown.

A gateway behind a shared ingress often gets a path prefix rather than its own host name, for
example `https://ingress.example.com/team-a/v1/...`. Fullsend supports this: hand out the gateway
URL **with** the prefix and **without** the trailing `/v1`:

| Gateway serves | Gateway URL to hand out |
|---|---|
| `https://gateway.example.com/v1/...` | `https://gateway.example.com` |
| `https://ingress.example.com/team-a/v1/...` | `https://ingress.example.com/team-a` |
| `https://ingress.example.com/api/v1/...` | `https://ingress.example.com/api` |

The runner then calls `<url>/v1/responses`, `<url>/v1/messages` and `<url>/v1/chat/completions`,
and the run's egress profile binds the credential to `<prefix>/v1/**` on the ingress host only, not
to the whole host, so the job's token never reaches another service behind the same ingress.

- The ingress must forward `<prefix>/v1/...` to the gateway's `/v1/...` (strip the prefix) unless
  the gateway itself serves the prefixed paths. Check with the step 8 workflow, setting
  `GATEWAY_URL` to the URL with the prefix: `GET /v1/models` then calls `<prefix>/v1/models`.
- The prefix must be a clean path: no `..`, `.` or empty segments, no percent-encoding, and only
  letters, digits, `-`, `.`, `_` and `~` in each segment. The host must still serve `https` on
  port 443.
- One gateway per repository still applies: a repository's
  `inference.gateway.url` names one host and prefix.

## Header custody for an x-api-key gateway

> **Not executed in this walkthrough:** the agentgateway setup above reads the caller's credential
> from `Authorization` and needs none of this. This section describes what fullsend expects of a
> gateway that reads it from `x-api-key`; the configuration depends on the gateway and is not
> shown.

Some gateways read the caller's key on the Messages API (`/v1/messages`) from `x-api-key`, the
header Anthropic documents for API keys on that API. A repository in the
`api-key` mode can send its key there by setting
`messages_auth_header: x-api-key` in its `inference.gateway` block. Then:

- Claude Code, and pi models with `api: anthropic-messages`, send the key as `x-api-key` and set
  no credential in `Authorization`.
- pi `openai-responses` and `openai-completions` models, and codex, keep `Authorization: Bearer`
  on `/v1/responses` and `/v1/chat/completions`.
- The setting is refused with `auth: oidc`, and fullsend never tries one header and retries in the
  other after a `401`.

On such a gateway, `x-api-key` is first the caller's credential and then the header an Anthropic
upstream reads for the provider key. The gateway must therefore:

- authenticate the caller from `x-api-key`, then remove that header before the request goes
  upstream;
- set the upstream's `x-api-key` from its own provider key by **replacing** the header, never by
  appending to it, so the caller's value cannot reach the upstream as a second header or as the
  first of two;
- still remove any caller `Authorization` header, on every path and toward every upstream;
- pass the [step 9](#9-check-that-no-caller-credential-reaches-the-upstream) custody check with a
  canary value in the caller's `x-api-key` on `/v1/messages`, and show that the upstream sees only
  the gateway's own key.

## Change the config

> **Not executed in this walkthrough:** the service was deleted after the checks, so these commands
> were not run here.

**The first time only, pin the running config first.** The step 6 revision mounts `latest`, and
Cloud Run reads a mounted secret when the file is read, not when the revision is created. Adding
a version would change what that revision loads on its next start, and leave no revision to roll
back to. Roll a revision that mounts the running version, `1`, before you add a new one:

```bash
gcloud run services update "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --update-secrets "/etc/agw-config/config.yaml=gateway-config:1"
```

For every change, add `config.yaml` as a new secret version, roll a revision that mounts that
version by number, and send all traffic to it:

```bash
v=$(gcloud secrets versions add gateway-config --project "$PROJECT" --data-file=config.yaml \
  --format='value(name.basename())')
gcloud run services update "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --update-secrets "/etc/agw-config/config.yaml=gateway-config:$v"
gcloud run services update-traffic "$SERVICE" --project "$PROJECT" --region "$REGION" --to-latest
```

Then rerun the workflow from step 8. The `--to-latest` step matters only after a rollback, which
pins traffic to an older revision; otherwise it changes nothing.

- **Mount a version number, not `latest`.** A revision that mounts a numbered version keeps the
  config it was deployed with. A revision that mounts `latest` reads the newest version instead,
  so it cannot restore an older config.
- **To roll back,** list the recent revisions, then send all traffic to the previous one:

  ```bash
  gcloud run revisions list --service "$SERVICE" --project "$PROJECT" --region "$REGION" --limit 3
  gcloud run services update-traffic "$SERVICE" --project "$PROJECT" --region "$REGION" \
    --to-revisions <previous-revision>=100
  ```
- **Keep the older version enabled** while any revision you may roll back to mounts it. If the
  version is disabled, that revision cannot read its config, and the gateway exits at startup.

## Remove a repository

> **Not executed in this walkthrough:** the service was deleted after the checks, so no
> repository was removed from a running gateway here.

1. **Remove every grant the repository has** in the gateway config
   ([step 3](#3-write-the-gateway-config)):
   - its `jwt.repository` rule on every model. Where a rule allows several repositories with `||`,
     delete only that repository's term;
   - if you issued it a gateway key ([`auth: api-key`](#accept-a-static-key-as-well-auth-api-key)),
     its `apiKey.purpose` rule on every model and its entry under `apiKey.keys`. Removing only the
     `jwt.repository` rule leaves the key working.

   **If that was a model's last `allow` rule, remove the model too.** A model left with no
   `authorization` block or an empty `rules` list lets every caller through (see
   [step 3](#3-write-the-gateway-config)).
2. **Roll the change out** as in [Change the config](#change-the-config).
3. **Check that the repository is refused:**

   | Mode | Check | Refused looks like |
   |---|---|---|
   | `oidc` | The step 8 workflow in that repository, or [`fullsend inference gateway status`](../../cli/inference.md#inference-gateway-status) in a job there | `GET /v1/models` returns `{"object":"list","data":[]}` or `403 model_authorization_denied`, as in [Check that another repository is refused](#check-that-another-repository-is-refused). `status` prints `Gateway authenticated the assertion, but no models are authorised for <owner/repo>` or `Gateway refused the assertion (HTTP 403)` |
   | `api-key` | `GET /v1/models` with `Authorization: Bearer <old key>` from your own machine (`status` stops at the config checks in this mode) | `401 authentication failed`, because the key's `apiKey.keys` entry is gone. Then ask the repo admin to delete the `FULLSEND_INFERENCE_GATEWAY_API_KEY` secret |

## Watch health and capacity

> **Not executed in this walkthrough:** the service was deleted after the checks.

- **Every model call leaves one log line.** It carries `http.status`, the calling repository in
  `jwt.sub`, the model, token counts (`gen_ai.usage.*`) and `duration`. The sample under
  [step 8](#8-check-the-gateway-from-github-actions) shows the shape. Filter it for `429` and
  `5xx`. A `429` with `reason=RateLimit` is the gateway's own `localRateLimit` (below); any other
  `429` or `5xx` usually comes from the upstream.
- **Cloud Run's own `429`s never reach the gateway.** When every request slot on every instance
  up to `--max-instances` is busy, Cloud Run refuses the request itself. It shows only in Cloud
  Run's request log (`run.googleapis.com/requests`), not in the gateway's lines. Step 6 sets no
  `--concurrency`, so Cloud Run's default applies: 80 slots per vCPU, so 80 with `--cpu 1`.
- **Cap bursts with agentgateway's `localRateLimit`** (requests and tokens per `fillInterval`).
  Set its `key` so that callers outside your allow rule draw from a separate bucket. Any GitHub
  Actions job can mint a token for your audience (only pull-request runs from forks cannot: they
  get no `id-token: write`). That token passes `jwtAuth` and reaches the rate limit before the
  model's rule refuses it, so with one shared bucket an outside repository can use up your
  repositories' share. The caps apply per instance and reset on each new instance,
  so they are a burst guard, not a spend limit. Set a budget alert at the model provider as well.
- **Scale from what you measure.** Raise the rate caps on `reason=RateLimit` `429`s. Raise
  `--concurrency` on Cloud Run `429`s; Cloud Run sets the default only when it creates the
  service, so raise `--concurrency` yourself after a CPU change. Raise CPU or memory when the
  instance runs hot. If you raise `--max-instances`, divide the rate caps by the same factor:
  each instance has its own buckets. A streamed model call holds its request slot for the whole
  response.

## Delete the gateway

```bash
gcloud run services delete "$SERVICE" --project "$PROJECT" --region "$REGION" --quiet
gcloud secrets delete gateway-config --project "$PROJECT" --quiet
gcloud secrets delete gateway-upstream-key --project "$PROJECT" --quiet
gcloud projects remove-iam-policy-binding "$PROJECT" --member "serviceAccount:$SA" \
  --role roles/aiplatform.user --condition=None --format='value(etag)'
gcloud iam service-accounts delete "$SA" --project "$PROJECT" --quiet
gcloud artifacts repositories delete "$AR_REPO" --project "$PROJECT" --location "$REGION" --quiet
```

```text
Deleting [inference-gateway]...
...........................done.
Deleted service [inference-gateway].
Deleted secret [gateway-config].
Deleted secret [gateway-upstream-key].
Updated IAM policy for project [example-project].
BwZd...
deleted service account [gateway-runtime@example-project.iam.gserviceaccount.com]
Delete request issued for: [example-images]
Waiting for operation [projects/example-project/locations/us-east5/operations/...] to complete...
.....done.
Deleted repository [example-images].
```

Remove the project binding before deleting the service account. Deleting a secret also removes its
accessor binding. Afterwards, `describe` on each resource returns `NOT_FOUND`, and the service URL
returns `404`.

## Troubleshooting

Every `jwtAuth` refusal returns `401` with the body `authentication failed`. The reason is in the
gateway's access log line for that request, as `error="authentication failure: ..."` with
`reason=JwtAuth`. Find it with:

```bash
gcloud logging read "resource.labels.service_name=\"$SERVICE\" AND textPayload:\"reason=JwtAuth\"" \
  --project "$PROJECT" --freshness 1h --limit 5 --format 'value(textPayload)'
```

For the step 6 check's non-JWT bearer, on v1.7.0:

```text
2026-10-30T15:07:24.725330Z	error	request gateway=default/default listener=default route=internal/llm:request ... http.method=GET ... http.path=/v1/models http.version=HTTP/1.1 http.status=401 ... protocol=http error="authentication failure: the token header is malformed: Error(InvalidToken)" reason=JwtAuth duration=0ms
```

The rows below are keyed on that `error=` text.

| You see | Cause | Action |
|---|---|---|
| `401` `text/html` … `Your client does not have permission to the requested URL` | Cloud Run's invoker IAM check rejected a non-Google bearer before it reached the gateway. | `gcloud run services update "$SERVICE" --project "$PROJECT" --region "$REGION" --no-invoker-iam-check` |
| `401`; log `error="authentication failure: the token is invalid or malformed: Error(InvalidAudience)"` | The job minted its token for a different audience. | Make the runner's configured audience equal the gateway's `audiences` value, character for character. |
| `401`; log `error="authentication failure: no bearer token found"` | The request carried no `Authorization: Bearer`, or sent the token only in `x-api-key`. | agentgateway reads the token from `Authorization: Bearer` by default (configurable with `location`). For pi, set `authHeader` for `anthropic-messages` to `authorization`; the fullsend runner does this for you. |
| `401`; log `error="authentication failure: the token header is malformed: Error(InvalidToken)"` | The bearer is not a JWT. | Check the runner fetched an OIDC token (`id-token: write` permission) rather than passing another secret. |
| `401`; log `error` ends in `Error(ExpiredSignature)` (seen in an earlier live test, not this walkthrough) | The token is past its `exp` (GitHub tokens live `exp − iat`, 300 s in practice, plus about 60 s of gateway leeway). | The runner must re-fetch the token before `exp`. |
| `GET /v1/models` returns `{"object":"list","data":[]}` and every model `403 model_authorization_denied` | The token is valid but its `repository` matches no `authorization` rule. | Add the repository to the rules (exact `owner/name`), then [change the config](#change-the-config). |
| `400 failed to process LLM request: unsupported conversion: from Responses to provider gcp.vertex_ai (supported: [AnthropicMessages])` | A Vertex Claude model was called on `/v1/responses`. | Map Claude models to `anthropic-messages` in the runner's model list. |
| `Permission denied (os error 13)` from `--validate-only` | The mounted file isn't readable by the container's non-root user. | `chmod -R a+rX` the mounted directories. |
| Gateway exits at startup after adding an `apiKey.file` model | The file it names is not mounted. | Mount the key secret at that path in its own directory (`--set-secrets`). |

## Related

- [OpenAI Workload Identity](openai-workload-identity.md): the route without a gateway, where the
  OIDC token is exchanged with OpenAI directly.
- [pi-inference-gateway](https://github.com/fullsend-ai/pi-inference-gateway): the pi extension
  that calls the gateway, and its agentgateway guide.
- agentgateway v1.7.0 configuration reference: `schema/config.md` in the
  [agentgateway repository](https://github.com/agentgateway/agentgateway/blob/v1.7.0/schema/config.md).
- [ADR 0137](../../ADRs/0137-inference-gateway-credential-route.md): the inference gateway
  credential route, including the gateway-side requirements this page implements.
