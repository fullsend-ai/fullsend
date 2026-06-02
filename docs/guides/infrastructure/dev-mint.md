# Standalone token mint

The standalone mint is a local HTTP server that replaces the GCP token mint infrastructure (Secret Manager, Workload Identity Federation, Cloud Functions) for development and evaluation. It stores GitHub App PEM keys on disk and mints real installation tokens — no cloud infrastructure required for token minting.

> **This guide is for developers and evaluators** who want to try fullsend without deploying the full GCP mint stack. For production deployments, see the infrastructure reference in the admin guides.

> **Note:** The standalone mint eliminates GCP for **token minting** only. Agents still need an LLM to do their work — fullsend currently uses Claude on Vertex AI, which requires a GCP project with the Vertex AI API enabled. The `--inference-project` flag during install configures this. Without it, agents will start but fail when they try to call the model.

## Prerequisites

- **fullsend CLI** (v0.5.0+ or built from source):

  ```bash
  go build -o ./fullsend ./cmd/fullsend/
  ```

- **GitHub account** with admin access to the target organization

- **GitHub CLI** (`gh`) authenticated with org admin scopes, or a `GH_TOKEN` environment variable

- **cloudflared** — required so GitHub Actions runners can reach the mint on your machine. Install from [Cloudflare Downloads](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/) or download the binary:

  ```bash
  curl -L -o ~/.local/bin/cloudflared \
    https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64
  chmod +x ~/.local/bin/cloudflared
  ```

- **(Required for agents)** A GCP project with the Vertex AI API enabled and Claude model access. Without this, token minting works but agents cannot call the LLM.

## Step 1: Start the mint

Start the standalone mint with the built-in cloudflared tunnel so GitHub Actions runners can reach it:

```bash
fullsend mint run \
  --data-dir ~/.fullsend-mint \
  --port 8321 \
  --tunnel
```

The `--tunnel` flag starts a cloudflared quick tunnel automatically. The output will include a tunnel URL like:

```
Tunnel URL: https://random-words.trycloudflare.com
```

Copy this URL — you will pass it as `--mint-url` in the next step.

> **Note:** Quick tunnel URLs are ephemeral — they change every time cloudflared restarts. Keep the mint running for the duration of your session.

If you prefer to manage the tunnel yourself, omit `--tunnel` and run cloudflared separately:

```bash
# Terminal 1: start the mint
fullsend mint run --data-dir ~/.fullsend-mint --port 8321

# Terminal 2: start the tunnel
cloudflared tunnel --url http://localhost:8321
```

Verify the mint is reachable:

```bash
# Local
curl http://localhost:8321/health

# Through the tunnel
curl https://<tunnel-url>/health
```

Both should return `{"status":"ok"}`.

## Step 2: Install fullsend

Run the install command, pointing at the mint tunnel URL:

```bash
fullsend admin install <org> \
  --mint-url https://<tunnel-url> \
  --skip-mint-check \
  --app-set <org-name> \
  --inference-project <gcp-project-id>
```

| Flag | Purpose |
|------|---------|
| `--mint-url` | The cloudflared tunnel URL from Step 1 |
| `--skip-mint-check` | Bypasses GCP mint validation; uses the standalone mint instead |
| `--app-set` | Prefix for GitHub App names (e.g., `myorg` creates `myorg-fullsend`, `myorg-triage`, etc.). Use this to avoid slug conflicts with existing public apps. |
| `--inference-project` | GCP project ID for Vertex AI. Required for agents to call Claude. |

The installer will:

1. Create GitHub Apps for each agent role via a browser-based manifest flow (you click through in your browser for each app)
2. Write each App's private key (PEM) to the mint's data directory
3. Create a `.fullsend` config repo in the org
4. Write `config.yaml` and scaffold files
5. Set `FULLSEND_MINT_URL` as an org variable pointing to the tunnel URL
6. Run the enrollment workflow to set up target repositories

> **Tip:** When the installer asks about an existing app (`App X already exists — install it into <org>? [Y/n]`), this means a public app with that slug already exists. Say **Y** to reuse it, or **n** to create a new one. If you say **n** and the slug is taken, the install will fail — use `--app-set` with a unique prefix instead.

## Step 3: Verify

Check that the mint has all roles registered:

```bash
curl http://localhost:8321/v1/status | jq .
```

Expected output:

```json
{
  "org": "myorg",
  "roles": [
    { "role": "fullsend", "app_id": "12345" },
    { "role": "triage", "app_id": "12346" },
    { "role": "coder", "app_id": "12347" },
    { "role": "review", "app_id": "12348" },
    { "role": "retro", "app_id": "12349" },
    { "role": "prioritize", "app_id": "12350" }
  ]
}
```

All six roles should be present with non-empty `app_id` values.

Test token minting directly:

```bash
curl -X POST http://localhost:8321/v1/token \
  -H 'Content-Type: application/json' \
  -d '{"role": "triage", "repos": ["some-repo"]}'
```

This should return a real GitHub installation token:

```json
{
  "token": "ghs_...",
  "expires_at": "2026-06-01T15:00:00Z"
}
```

Trigger an agent to test the full flow — for example, create an issue in an enrolled repo and run `/fs-triage` on it. Monitor the workflow run in the `.fullsend` repo's Actions tab.

## Re-running install

If you restart the mint or the tunnel URL changes, you need to update the `FULLSEND_MINT_URL` org variable. Re-running the install command will do this:

```bash
fullsend admin install <org> \
  --mint-url https://<new-tunnel-url> \
  --skip-mint-check \
  --app-set <org-name> \
  --inference-project <gcp-project-id>
```

The installer detects existing apps and the `.fullsend` repo, prompting you to reuse them. When prompted for PEM files for existing apps, point to the mint's stored copies:

```
~/.fullsend-mint/pems/<role>.pem
```

## API reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check — returns `{"status":"ok"}` |
| `POST` | `/v1/token` | Mint a GitHub App installation token |
| `GET` | `/v1/status` | List registered org, roles, and app IDs |

### POST /v1/token

Mint a GitHub App installation token for the given role and repositories.

```json
{
  "role": "triage",
  "repos": ["my-repo"]
}
```

Returns:

```json
{
  "token": "ghs_...",
  "expires_at": "2026-06-01T15:00:00Z"
}
```

## Data directory

The standalone mint loads PEMs from the `--data-dir` directory at startup:

```
~/.fullsend-mint/
├── config.json          # org name, role-to-appID mapping
└── pems/
    ├── fullsend.pem
    ├── triage.pem
    ├── coder.pem
    ├── review.pem
    ├── retro.pem
    └── prioritize.pem
```

This state survives restarts — if you stop and restart the mint with the same `--data-dir`, it reloads the stored PEMs and resumes serving tokens. To pick up new or changed PEM files, restart the mint.

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `"no PEM configured for role X"` | PEM not stored for that role | Check `curl /v1/status`. Re-run install or place the PEM file in `{data-dir}/pems/{role}.pem` and restart. |
| `502` from tunnel URL | cloudflared tunnel dropped | Restart the tunnel; update `FULLSEND_MINT_URL` if the URL changed. |
| App slug conflict during install | Slug already taken by another app | Use `--app-set <unique-prefix>` to pick a different prefix. |
| `google-github-actions/auth` failure | No inference credentials configured | Add `--inference-project <gcp-project>` to the install command. |
| `"App X exists but its private key is missing"` | Re-running install with existing apps | Enter the path `~/.fullsend-mint/pems/<role>.pem` when prompted. |
| DNS resolution failure for tunnel URL | Local DNS doesn't resolve `trycloudflare.com` | GitHub Actions runners use public DNS and will resolve fine. Test locally with `curl --resolve`. |

## Limitations

- **OIDC verification via JWKS** — the standalone mint verifies OIDC tokens by directly fetching JWKS from GitHub's OIDC issuer, bypassing GCP Workload Identity Federation. Use `--insecure-no-auth` to disable verification for local testing only.
- **Ephemeral tunnel URLs** — quick tunnel URLs change on every cloudflared restart. Re-running install updates the org variable.
- **PEMs stored unencrypted** — private keys are written as plain files in the data directory. Protect the directory with filesystem permissions.
- **Single org** — each mint instance serves one GitHub organization. To serve multiple orgs, run separate instances on different ports.
- **Static PEM loading** — PEMs are loaded once at startup. Restart the mint to pick up changes.
