# Standalone mint

This guide covers setting up and running the standalone token mint — a lightweight, self-hosted alternative to the GCP-hosted mint Cloud Function. The standalone mint lets you run your own OIDC token exchange service without any GCP infrastructure, with the option to proxy unhandled roles to the hosted mint.

> **This guide is for users who want to run agents with their own GitHub App identity.** If you are using the hosted mint with the shared fullsend apps, see [Mint service administration](mint-administration.md) instead — you do not need to run your own mint.

## Why use the standalone mint?

The hosted fullsend mint uses shared GitHub Apps owned by the fullsend team. Every organization enrolled in the hosted mint shares the same apps — your agents authenticate as `fullsend-ai-triage`, `fullsend-ai-coder`, etc. This works for most use cases, but there are reasons to run your own mint:

- **Own your agent identity.** Your agents authenticate as GitHub Apps you created and control. Commits, PR reviews, and issue comments come from your app, not a shared one. This matters for compliance, auditing, and brand identity.

- **Custom agent roles.** The hosted mint supports a fixed set of roles (triage, coder, review, fix, retro, prioritize, fullsend). The standalone mint lets you define custom roles with permissions tailored to your workflows — a `scanner` role with `security_events:write`, a `deployer` role with `deployments:write`, or anything else the GitHub API supports.

- **No GCP dependency.** The standalone mint is a single binary that reads PEM keys from a local directory and validates OIDC tokens directly against GitHub's JWKS endpoint. No GCP project, Secret Manager, Cloud Functions, or WIF configuration required.

- **Gradual adoption.** The fallback proxy lets you serve some roles locally while proxying the rest to the hosted mint. You can start with one custom role and expand over time without disrupting existing workflows.

## How it works

The standalone mint is an HTTP server that:

1. Receives token requests from GitHub Actions workflows (same protocol as the hosted mint)
2. Validates the caller's OIDC token directly against GitHub's JWKS endpoint
3. Uses a local PEM key to authenticate as your GitHub App
4. Returns a scoped installation token for the requested repos

For roles without a local PEM, the optional fallback proxy forwards the request to an upstream mint (typically the hosted one), so existing workflows keep working.

## Prerequisites

- **Go 1.26+** to build the binary (or use a pre-built release)
- **A GitHub organization** where you will install your custom GitHub Apps
- **The hosted mint URL** (optional, for fallback proxy): `https://fullsend-mint-gljhbkcloq-uc.a.run.app`
- **Your organization enrolled in the hosted mint** (optional, for fallback proxy) — see [Mint service administration](mint-administration.md)

## Step 1: Create a GitHub App

Each role in the standalone mint corresponds to a GitHub App. Create one for each custom role you want to serve.

1. Go to **https://github.com/organizations/YOUR-ORG/settings/apps/new**

2. Fill in the app details:
   - **Name:** Choose something descriptive (e.g., `myorg-scanner`, `myorg-deployer`)
   - **Homepage URL:** Any URL (e.g., your org's GitHub page)
   - **Webhook:** Uncheck "Active" — the mint does not use webhooks

3. Set the permissions your role needs. For example, a scanner role might need:
   - **Repository permissions:**
     - Contents: Read
     - Security events: Write
     - Metadata: Read (always required)

4. Click **Create GitHub App**

5. Note the **App ID** shown on the app's settings page (a numeric ID like `12345678`)

6. Generate a private key:
   - Scroll to **Private keys** on the app settings page
   - Click **Generate a private key**
   - Save the downloaded `.pem` file — this is the only time GitHub provides it

7. Install the app on your organization:
   - Go to **https://github.com/apps/YOUR-APP-SLUG/installations/new**
   - Select your organization
   - Choose which repositories the app can access (all or selected)
   - Click **Install**

Repeat for each custom role.

## Step 2: Set up PEM storage

Create a directory to hold your PEM keys. Each file must be named `{role}.pem`:

```bash
mkdir -p pems/

# Copy your downloaded PEM files, naming them by role:
cp ~/Downloads/myorg-scanner.2026-06-18.private-key.pem pems/scanner.pem
cp ~/Downloads/myorg-deployer.2026-06-18.private-key.pem pems/deployer.pem
```

If you also serve built-in roles locally (e.g., triage), add those PEMs too:

```bash
cp ~/Downloads/myorg-triage.2026-06-18.private-key.pem pems/triage.pem
```

> **Keep PEM files secure.** Anyone with a PEM key can authenticate as the corresponding GitHub App. Set restrictive file permissions (`chmod 600 pems/*.pem`) and do not commit PEM files to version control.

## Step 3: Build the standalone mint

```bash
cd cmd/mint
go build -o fullsend-mint .
```

## Step 4: Configure and run

The standalone mint is configured entirely through environment variables:

### Required variables

| Variable | Description | Example |
|----------|-------------|---------|
| `ALLOWED_ORGS` | Comma-separated GitHub orgs allowed to request tokens, or `*` for public mint mode (any org; upstream-only workflow provenance) | `myorg,myorg-sandbox` or `*` |
| `ROLE_APP_IDS` | JSON map of role name to GitHub App ID (use plain role names, not org-prefixed) | `{"triage":"4087047","scanner":"5555555"}` |
| `OIDC_AUDIENCE` | OIDC audience claim (must match what workflows send) | `fullsend-mint` |
| `PEM_DIR` | Path to directory containing `{role}.pem` files | `./pems` |

### Optional variables

| Variable | Description | Example |
|----------|-------------|---------|
| `ALLOWED_WORKFLOW_FILES` | Comma-separated workflow file allowlist; `*` for all | `*` |
| `FALLBACK_MINT_URL` | Upstream mint URL for roles without local PEMs | `https://fullsend-mint-gljhbkcloq-uc.a.run.app` |
| `CUSTOM_ROLE_PERMISSIONS` | JSON map of custom role permissions (see below) | `{"scanner":{"contents":"read"}}` |
| `PER_REPO_WIF_REPOS` | Comma-separated repos requiring per-repo WIF | `myorg/private-repo` |
| `PORT` | HTTP listen port | `8080` (default) |

### Public mint mode

Set `ALLOWED_ORGS=*` to enable public mint mode:

- Any org may request tokens (installation lookup still scopes tokens to the requesting org)
- `job_workflow_ref` must reference `fullsend-ai/fullsend/.github/workflows/` only
- Leave `PER_REPO_WIF_REPOS` unset; the basename gate (`ALLOWED_WORKFLOW_FILES`) is not applied
- No WIF or GCP STS setup is required — standalone mint validates OIDC via GitHub JWKS directly

### Example: local roles with fallback proxy

This configuration serves `triage` and `scanner` locally while proxying all other roles (coder, review, etc.) to the hosted mint:

```bash
export ALLOWED_ORGS="myorg"
export ROLE_APP_IDS='{"triage":"4087047","scanner":"5555555"}'
export OIDC_AUDIENCE="fullsend-mint"
export PEM_DIR="./pems"
export ALLOWED_WORKFLOW_FILES="*"
export FALLBACK_MINT_URL="https://fullsend-mint-gljhbkcloq-uc.a.run.app"
export CUSTOM_ROLE_PERMISSIONS='{"scanner":{"contents":"read","security_events":"write","metadata":"read"}}'

./fullsend-mint
```

On startup, the mint logs the configuration:

```
2026/06/18 12:00:00 custom role permissions registered: [scanner]
2026/06/18 12:00:00 fallback mint configured: https://fullsend-mint-gljhbkcloq-uc.a.run.app (local roles: [scanner triage])
2026/06/18 12:00:00 fullsend-mint starting on :8080 (standalone mode)
```

### Example: standalone only (no fallback)

If you do not need the hosted mint at all, omit `FALLBACK_MINT_URL`. Requests for roles without local PEMs will be rejected:

```bash
export ALLOWED_ORGS="myorg"
export ROLE_APP_IDS='{"triage":"4087047","scanner":"5555555"}'
export OIDC_AUDIENCE="fullsend-mint"
export PEM_DIR="./pems"
export ALLOWED_WORKFLOW_FILES="*"
export CUSTOM_ROLE_PERMISSIONS='{"scanner":{"contents":"read","security_events":"write","metadata":"read"}}'

./fullsend-mint
```

## Step 5: Expose the mint to GitHub Actions

GitHub Actions workflows need to reach your mint over HTTPS. Options include:

- **Cloudflare Tunnel** (quick for testing):
  ```bash
  cloudflared tunnel --url http://localhost:8080
  ```

- **Reverse proxy** (nginx, Caddy) with a TLS certificate

- **Cloud VM** with a public IP and Let's Encrypt

Once you have a public URL, set it as a GitHub Actions variable so workflows use your mint:

```bash
# Set at the org level (applies to all repos)
gh api -X POST /orgs/myorg/actions/variables \
  -f name=FULLSEND_MINT_URL \
  -f value="https://your-mint-url.example.com" \
  -f visibility=all

# Or set per-repo
gh api -X POST /repos/myorg/my-repo/actions/variables \
  -f name=FULLSEND_MINT_URL \
  -f value="https://your-mint-url.example.com"
```

> **Note:** Repository-level variables override organization-level variables in GitHub Actions. If a repo already has `FULLSEND_MINT_URL` set at the repo level, update it there — the org-level variable will be ignored for that repo.

## Custom role permissions

### Defining permissions

Custom roles require an explicit permissions map via the `CUSTOM_ROLE_PERMISSIONS` environment variable. This tells the mint what permissions to request when creating installation tokens for the role.

The format is a JSON object mapping role names to permission maps:

```json
{
  "scanner": {
    "contents": "read",
    "security_events": "write",
    "metadata": "read"
  },
  "deployer": {
    "contents": "read",
    "deployments": "write",
    "environments": "write",
    "metadata": "read"
  }
}
```

Permission names and levels match the [GitHub App permissions API](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app). Common permission levels are `read` and `write`.

### Built-in roles cannot be overridden

The following role names are reserved and cannot be used in `CUSTOM_ROLE_PERMISSIONS`:

- `triage`, `coder`, `review`, `fix`, `retro`, `prioritize`, `fullsend`, `e2e`

Attempting to define a custom role with a built-in name will cause the mint to fail at startup:

```
registering custom role permissions: custom role "triage" collides with built-in role
```

This prevents accidental changes to the permission downscoping of built-in roles. If you want to serve a built-in role with your own GitHub App (using the standard permissions for that role), add it to `ROLE_APP_IDS` and provide its PEM — no entry in `CUSTOM_ROLE_PERMISSIONS` is needed.

### Role naming rules

Custom role names must:

- Start with a lowercase letter
- Contain only lowercase letters, digits, hyphens, and underscores
- Not contain double hyphens (`--`)

Valid examples: `scanner`, `deploy-prod`, `code_review`, `my-agent-v2`

Invalid examples: `Scanner` (uppercase), `123scanner` (starts with digit), `my--agent` (double hyphen)

### Permissions must match the GitHub App

The permissions in `CUSTOM_ROLE_PERMISSIONS` must be a subset of what the GitHub App is installed with. If you request a permission the app does not have, GitHub will return an error when the mint tries to create the installation token. The mint does not validate this at startup — the error occurs at token request time.

## Fallback proxy behavior

When `FALLBACK_MINT_URL` is set, the standalone mint acts as a transparent proxy for roles it does not handle locally:

| Request | Behavior |
|---------|----------|
| `POST /v1/token` with a role in `ROLE_APP_IDS` | Handled locally |
| `POST /v1/token` with an unknown role | Forwarded to `FALLBACK_MINT_URL` |
| `GET /health` | Always handled locally |
| `GET /v1/status` | Always handled locally |

The proxy forwards the original OIDC bearer token and request body to the upstream mint, and returns the upstream response verbatim. The upstream mint performs its own OIDC validation — your organization must be enrolled on the upstream mint for proxied requests to succeed.

When `FALLBACK_MINT_URL` is not set, requests for roles without local PEMs are rejected with a `403 Forbidden` response.

## Verifying the setup

### Check the health endpoint

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### Test from a GitHub Actions workflow

Create a test workflow that requests a token for your custom role:

```yaml
name: Test standalone mint
on: workflow_dispatch

permissions:
  id-token: write
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Get OIDC token
        id: oidc
        run: |
          OIDC_TOKEN=$(curl -s -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=fullsend-mint" | jq -r '.value')
          echo "::add-mask::$OIDC_TOKEN"
          echo "token=$OIDC_TOKEN" >> "$GITHUB_OUTPUT"

      - name: Request installation token
        run: |
          curl -s -X POST "${{ vars.FULLSEND_MINT_URL }}/v1/token" \
            -H "Authorization: Bearer ${{ steps.oidc.outputs.token }}" \
            -H "Content-Type: application/json" \
            -d '{"role":"scanner","repos":["${{ github.event.repository.name }}"]}'
```

## Complete example

This example sets up a standalone mint with:
- A custom `scanner` role using your own GitHub App
- The built-in `triage` role using your own GitHub App
- All other roles proxied to the hosted mint

```bash
# 1. Create GitHub Apps (see Step 1 above)
#    - myorg-triage (App ID: 4087047)
#    - myorg-scanner (App ID: 5555555)

# 2. Set up PEMs
mkdir -p pems
cp ~/Downloads/myorg-triage.private-key.pem pems/triage.pem
cp ~/Downloads/myorg-scanner.private-key.pem pems/scanner.pem
chmod 600 pems/*.pem

# 3. Build
cd cmd/mint && go build -o fullsend-mint .

# 4. Run
export ALLOWED_ORGS="myorg"
export ROLE_APP_IDS='{"triage":"4087047","scanner":"5555555"}'
export OIDC_AUDIENCE="fullsend-mint"
export PEM_DIR="./pems"
export ALLOWED_WORKFLOW_FILES="*"
export FALLBACK_MINT_URL="https://fullsend-mint-gljhbkcloq-uc.a.run.app"
export CUSTOM_ROLE_PERMISSIONS='{"scanner":{"contents":"read","security_events":"write","metadata":"read"}}'

./fullsend-mint

# 5. Expose (in another terminal)
cloudflared tunnel --url http://localhost:8080

# 6. Set the mint URL for your org
gh api -X POST /orgs/myorg/actions/variables \
  -f name=FULLSEND_MINT_URL \
  -f value="https://your-tunnel-url.trycloudflare.com" \
  -f visibility=all
```

## See also

- [Mint service administration](mint-administration.md) — Managing the hosted GCP mint
- [Bring Your Own Agent](../user/bring-your-own-agent.md) — Building custom agents and configuring existing ones
- [Getting Started](../getting-started/) — End-user setup
