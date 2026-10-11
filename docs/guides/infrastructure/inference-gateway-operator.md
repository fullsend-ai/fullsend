# Run an inference gateway that trusts GitHub Actions OIDC

This guide is for the **platform operator** who runs the inference gateway that fullsend's agents
call (pi, codex and Claude Code). When you finish, you have an [agentgateway](https://github.com/agentgateway/agentgateway)
service on Cloud Run that:

- accepts a GitHub Actions job's OIDC token as its only credential, and only for the repositories
  you list;
- holds the upstream model credential itself, so no forge secret stores a reusable key;
- serves the three APIs the runtimes use: `/v1/messages` (pi, Claude Code), `/v1/chat/completions`
  (pi) and `/v1/responses` (pi, codex).

The runner side (how a fullsend run fetches the token and points the agent's runtime at the gateway) is in the
[user guide for the hosted gateway route](../getting-started/getting-inference.md#inference-gateway-with-github-oidc-wif).
This page covers only the gateway.

## What you end up with

You hand these three values to the people who enrol repositories. None of them is a secret.

| Value | Example in this guide | What it is |
|---|---|---|
| **Gateway URL** | `https://gateway.example.com` | The Cloud Run service URL, or your own domain in front of it. |
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
  docker://ghcr.io/agentgateway/agentgateway:v1.6.0 \
  "docker://$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.6.0"
```

```text
Create request issued for: [example-images]
Waiting for operation [projects/example-project/locations/us-east5/operations/...] to complete...
.....done.
Created repository [example-images].
```

`--all` copies every architecture in the release. Check that the copy is byte-for-byte the release:

```bash
skopeo inspect --raw docker://ghcr.io/agentgateway/agentgateway:v1.6.0 | shasum -a 256
skopeo inspect --raw --creds "oauth2accesstoken:$(gcloud auth print-access-token)" \
  "docker://$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.6.0" | shasum -a 256
```

```text
9d3e6044ddcdc0878b1787f77bd401252b95e22684203fb5e874c4c42d2ed90c  -
9d3e6044ddcdc0878b1787f77bd401252b95e22684203fb5e874c4c42d2ed90c  -
```

The two digests must match.

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
  `jwt.repository == "example-org/a" || jwt.repository == "example-org/b"`. GitHub's token also
  carries numeric `repository_id` and `repository_owner_id` claims, which survive a rename. Consider
  them if a renamed or re-created repository must not inherit access (not tested in this
  walkthrough).
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

### An upstream that needs an API key

For a provider that takes a key rather than the service account (OpenAI, for example), the gateway
reads the key from a file. You mount that file from Secret Manager in [step 6](#6-deploy-to-cloud-run).

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

A repository that sets `auth: api-key`
([ADR 0138](../../ADRs/0138-inference-gateway-api-key-credential-mode.md)) sends a key you issue
instead of an OIDC token, in the same `Authorization: Bearer` header. Serve these repositories only
if you must: a key is a long-lived secret, and a leaked key works until you revoke it. Prefer
OIDC wherever it is possible.

> **Not executed here:** this section has no live run, so no output is shown. Validate the config
> as in [step 4](#4-validate-the-config-locally) before you deploy it. To repeat the
> [step 9](#9-check-that-no-caller-credential-reaches-the-upstream) custody check with a key in
> place of the token, grant a test key the echo models for the check only: add `echo` and
> `echo-noremove` to its `allowedModels`, add its `apiKey.purpose` rule to both echo models, and
> remove both grants once the check passes.

Add an agentgateway `apiKey` policy under `llm.policies`. Store each key's SHA-256 hash, never the
key, and list the models each key may call:

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
  ghcr.io/agentgateway/agentgateway:v1.6.0 -f /etc/agw-config/config.yaml --validate-only
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
  --image "$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/agentgateway:v1.6.0" \
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
- Size the service for your load. The values above are what this walkthrough ran with.

Check that the gateway, not Cloud Run, answers a non-Google bearer:

```bash
curl -s -i https://gateway.example.com/v1/models -H 'authorization: Bearer not-a-google-token'  # gitleaks:allow
```

```text
HTTP/2 401
content-type: text/plain
server: Google Frontend
content-length: 74

authentication failure: the token header is malformed: Error(InvalidToken)
```

A `text/plain` body starting `authentication failure:` comes from agentgateway. A `text/html` body
means the invoker check is still on (see [Troubleshooting](#troubleshooting)).

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
[step 9](#9-check-that-no-caller-credential-reaches-the-upstream).

## 8. Check the gateway from GitHub Actions

Add this workflow to an allowed repository, set a repository variable `GATEWAY_URL` to the service
URL, and run it. It mints a token for your audience and checks one prompt per API, then the
refusals.

```yaml
name: gateway-check
on: workflow_dispatch
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
    steps:
    - name: check the gateway with this job's OIDC token
      shell: bash
      run: |
        set -u +e
        tok() { curl -sf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
          "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$1" | jq -r .value; }
        call() { # call <label> <path> [curl args...]: prints label, HTTP status, body (first 700 bytes)
          label=$1; path=$2; shift 2
          code=$(curl -s -o /tmp/body -w '%{http_code}' "$GATEWAY_URL$path" "$@")
          cat /tmp/body >> /tmp/all; echo "== $label"; echo "HTTP $code $(head -c 700 /tmp/body)"; echo
        }
        TOKEN=$(tok "$AUDIENCE")
        echo "== claims (non-secret)"
        cut -d. -f2 <<<"$TOKEN" | tr '_-' '/+' | base64 -d 2>/dev/null | jq -c '{iss,aud,repository,ttl:(.exp-.iat)}'
        echo
        J=(-H "authorization: Bearer $TOKEN" -H 'content-type: application/json')
        call "GET /v1/models" /v1/models -H "authorization: Bearer $TOKEN"
        call "POST /v1/messages (claude-sonnet-4-6)" /v1/messages "${J[@]}" -H 'anthropic-version: 2023-06-01' \
          -d '{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"Reply with exactly: pong"}]}'
        call "POST /v1/chat/completions (gemini-2.5-flash)" /v1/chat/completions "${J[@]}" \
          -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Reply with exactly: pong"}]}'
        call "POST /v1/responses (echo)" /v1/responses "${J[@]}" -d '{"model":"echo","input":"hi"}'
        call "POST /v1/responses (claude-sonnet-4-6)" /v1/responses "${J[@]}" -d '{"model":"claude-sonnet-4-6","input":"hi"}'
        call "custody: echo, requestHeaders.remove [x-api-key]" /v1/chat/completions "${J[@]}" \
          -H 'x-api-key: canary' -H 'x-canary: 1' -d '{"model":"echo","messages":[{"role":"user","content":"hi"}]}'
        call "custody: echo-noremove, no requestHeaders" /v1/chat/completions "${J[@]}" \
          -H 'x-api-key: canary' -H 'x-canary: 1' -d '{"model":"echo-noremove","messages":[{"role":"user","content":"hi"}]}'
        WRONG=$(tok some-other-audience)
        call "negative: token minted for another audience" /v1/models -H "authorization: Bearer $WRONG"
        call "negative: token only in x-api-key" /v1/messages -H "x-api-key: $TOKEN" -H 'content-type: application/json' \
          -H 'anthropic-version: 2023-06-01' -d '{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}'
        call "negative: no credential" /v1/models
        echo "== does any body contain the token? $(grep -c -e "${TOKEN:0:40}" -e "${WRONG:0:40}" /tmp/all || true)"
```

```bash
gh variable set GATEWAY_URL -R example-org/example-repo -b https://gateway.example.com
gh workflow run gateway-check.yml -R example-org/example-repo
id=$(gh run list -R example-org/example-repo -w gateway-check.yml -L1 --json databaseId -q '.[0].databaseId')
gh run watch "$id" -R example-org/example-repo
gh run view "$id" -R example-org/example-repo --log
```

The `echo` calls need the custody-check models from [step 9](#9-check-that-no-caller-credential-reaches-the-upstream);
drop those lines if you skip it. Output from the allowed repository:

```text
== claims (non-secret)
{"iss":"https://token.actions.githubusercontent.com","aud":"fullsend-inference","repository":"example-org/example-repo","ttl":300}

== GET /v1/models
HTTP 200 {"data":[{"id":"claude-sonnet-4-6","object":"model","created":1791586051,"owned_by":"openai"},{"id":"echo-noremove","object":"model","created":1791586051,"owned_by":"openai"},{"id":"gemini-2.5-flash","object":"model","created":1791586051,"owned_by":"openai"},{"id":"echo","object":"model","created":1791586051,"owned_by":"openai"}],"object":"list"}

== POST /v1/messages (claude-sonnet-4-6)
HTTP 200 {"id":"msg_vrtx_...","type":"message","role":"assistant","model":"claude-sonnet-4-6","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":13,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}},"content":[{"text":"pong","type":"text"}],"container":null,"stop_details":null}

== POST /v1/chat/completions (gemini-2.5-flash)
HTTP 200 {"model":"gemini-2.5-flash","usage":{"prompt_tokens":5,"completion_tokens":20,"total_tokens":25,"completion_tokens_details":{"reasoning_tokens":19}},"choices":[{"message":{"content":"pong","role":"assistant"},"index":0,"finish_reason":"stop"}],"id":"...","created":1791586171,"object":"chat.completion"}

== POST /v1/responses (echo)
HTTP 200 {"id":"resp_...","status":"completed","output":[{"type":"message","content":[{"type":"output_text","annotations":[],"logprobs":null,"text":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":false,\"canary_present\":false}"}], ...

== POST /v1/responses (claude-sonnet-4-6)
HTTP 400 failed to process LLM request: unsupported conversion: from Responses to provider gcp.vertex_ai (supported: [AnthropicMessages])

== negative: token minted for another audience
HTTP 401 authentication failure: the token is invalid or malformed: Error(InvalidAudience)

== negative: token only in x-api-key
HTTP 401 authentication failure: no bearer token found

== negative: no credential
HTTP 401 authentication failure: no bearer token found

== does any body contain the token? 0
```

- **The token lives 300 seconds** (`ttl:300`). The fullsend runner re-fetches it before it expires,
  so runs longer than that keep working.
- **`/v1/responses` works only for upstreams that accept it.** The gateway translates Responses to
  Chat Completions for a Chat-only upstream (the `echo` call), but not to Vertex Claude. Map
  Claude models to `anthropic-messages` in the runner's model list, and Gemini models to
  `openai-completions`.
- The gateway's log line for each call names the model, the upstream and the caller. The repository
  appears in `jwt.sub`:

  ```text
  info	request gateway=default/default listener=default route=internal/llm:request endpoint=aiplatform.googleapis.com:443 ... http.path=/v1/chat/completions http.status=200 ... jwt.sub=repo:example-org@1111/example-repo@2222:ref:refs/heads/main protocol=llm gen_ai.operation.name=chat gen_ai.provider.name=gcp.vertex_ai gen_ai.request.model=gemini-2.5-flash ... duration=575ms
  ```

### Check that another repository is refused

Run the same workflow, with the same audience, in a repository that is **not** in the allow rules:

```text
== claims (non-secret)
{"iss":"https://token.actions.githubusercontent.com","aud":"fullsend-inference","repository":"example-org/other-repo","ttl":300}

== GET /v1/models
HTTP 200 {"data":[],"object":"list"}

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

Add to `config.tmpl.yaml`, at the top level:

```yaml
binds:
- port: 9000
  listeners:
  - name: echo
    hostname: "*"
    routes:
    - name: echo
      policies:
        directResponse:
          status: 200
          headers:
            content-type: '"application/json"'
          bodyExpression: >-
            toJson({"id":"echo","object":"chat.completion","created":0,"model":"echo",
            "choices":[{"index":0,"message":{"role":"assistant","content": toJson({
            "authorization": !("authorization" in request.headers) ? "absent" : (request.headers["authorization"] == "Bearer ECHO_KEY" ? "GATEWAY_KEY" : "OTHER len=" + string(size(request.headers["authorization"]))),
            "x_api_key_present": "x-api-key" in request.headers,
            "canary_present": "x-canary" in request.headers})},"finish_reason":"stop"}],
            "usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}})
```

and under `llm.models`:

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
HTTP 200 {"model":"echo",...,"choices":[{"message":{"content":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":false,\"canary_present\":true}","role":"assistant"},...

== custody: echo-noremove, no requestHeaders
HTTP 200 {"model":"echo",...,"choices":[{"message":{"content":"{\"authorization\":\"GATEWAY_KEY\",\"x_api_key_present\":true,\"canary_present\":true}","role":"assistant"},...
```

- `authorization` is `GATEWAY_KEY` in both: the job's token was replaced by the gateway's own key.
- `x-api-key` reached the upstream **only** on the model without `requestHeaders.remove`.
- The canary reached the upstream in both: other caller headers pass through unchanged.

Remove the two `echo` models and the `binds` block once the check passes, then roll the change
out as described in [Change the config](#change-the-config).

## Change the config

> **Not executed in this walkthrough:** the service was deleted after the checks, so these commands
> were not run here. They add a new secret version and roll a new revision that mounts it.

```bash
gcloud secrets versions add gateway-config --project "$PROJECT" --data-file=config.yaml
gcloud run services update "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --update-secrets "/etc/agw-config/config.yaml=gateway-config:latest"
```

Then rerun the workflow from step 8.

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

| You see | Cause | Action |
|---|---|---|
| `401` `text/html` … `Your client does not have permission to the requested URL` | Cloud Run's invoker IAM check rejected a non-Google bearer before it reached the gateway. | `gcloud run services update "$SERVICE" --project "$PROJECT" --region "$REGION" --no-invoker-iam-check` |
| `401 authentication failure: the token is invalid or malformed: Error(InvalidAudience)` | The job minted its token for a different audience. | Make the runner's configured audience equal the gateway's `audiences` value, character for character. |
| `401 authentication failure: no bearer token found` | The request carried no `Authorization: Bearer`, or sent the token only in `x-api-key`. | agentgateway reads the token from `Authorization: Bearer` by default (configurable with `location`). For pi, set `authHeader` for `anthropic-messages` to `authorization`; the fullsend runner does this for you. |
| `401 authentication failure: the token header is malformed: Error(InvalidToken)` | The bearer is not a JWT. | Check the runner fetched an OIDC token (`id-token: write` permission) rather than passing another secret. |
| `401` `Error(ExpiredSignature)` (seen in an earlier live test, not this walkthrough) | The token is past its `exp` (GitHub tokens live `exp − iat`, 300 s in practice, plus about 60 s of gateway leeway). | The runner must re-fetch the token before `exp`. |
| `GET /v1/models` returns `{"data":[],"object":"list"}` and every model `403 model_authorization_denied` | The token is valid but its `repository` matches no `authorization` rule. | Add the repository to the rules (exact `owner/name`), then [change the config](#change-the-config). |
| `400 failed to process LLM request: unsupported conversion: from Responses to provider gcp.vertex_ai (supported: [AnthropicMessages])` | A Vertex Claude model was called on `/v1/responses`. | Map Claude models to `anthropic-messages` in the runner's model list. |
| `Permission denied (os error 13)` from `--validate-only` | The mounted file isn't readable by the container's non-root user. | `chmod -R a+rX` the mounted directories. |
| Gateway exits at startup after adding an `apiKey.file` model | The file it names is not mounted. | Mount the key secret at that path in its own directory (`--set-secrets`). |

## Related

- [OpenAI Workload Identity](openai-workload-identity.md): the route without a gateway, where the
  OIDC token is exchanged with OpenAI directly.
- [pi-inference-gateway](https://github.com/fullsend-ai/pi-inference-gateway): the pi extension
  that calls the gateway, and its agentgateway guide.
- agentgateway v1.6.0 configuration reference: `schema/config.md` in the
  [agentgateway repository](https://github.com/agentgateway/agentgateway/blob/v1.6.0/schema/config.md).
- [ADR 0137](../../ADRs/0137-inference-gateway-credential-route.md): the inference gateway
  credential route, including the gateway-side requirements this page implements.
