# Cloudflare Infrastructure

Manages Cloudflare DNS, edge settings, and security for fullsend-ai using [OpenTofu](https://opentofu.org/).

## Prerequisites

- [OpenTofu](https://opentofu.org/) >= 1.5.0
- Cloudflare API Token with:
  - `Zone:Zone:Read`
  - `Zone:Zone Settings:Edit`
- Target domain name (defaults to `fullsend.sh`)

## Configuration

Copy the example variables file:

```bash
cd infrastructure-as-code/cloudflare
cp terraform.tfvars.example terraform.tfvars
```

Modify the values as needed.

Authenticate via environment variable:

```bash
export CLOUDFLARE_API_TOKEN="<your-token>"
```

## Usage

Run from the repo root:

```bash
tofu -chdir=infrastructure-as-code/cloudflare init
tofu -chdir=infrastructure-as-code/cloudflare plan -compact-warnings
tofu -chdir=infrastructure-as-code/cloudflare apply
```

## Managed Resources

- `cloudflare_zone_setting.always_use_https`: Enforces HTTPS on all requests (`value = "on"`).

## CI/CD Deployment

Workflow: [`.github/workflows/cloudflare.yml`](../../.github/workflows/cloudflare.yml)

- Triggers on changes to `infrastructure-as-code/cloudflare/**`.
- Pull Requests: runs `tofu fmt` and `tofu validate` only — PR-head code never sees the API token.
- Push to `main`: plans and applies changes via `tofu apply`.

### Required GitHub Secrets / Variables

- `CLOUDFLARE_IAC_API_TOKEN` (Secret): zone-scoped Cloudflare API token. Separate from the Workers deploy token (`CLOUDFLARE_API_TOKEN`) used by the docs site.
- `CLOUDFLARE_ZONE` (Secret or Variable, optional if defined via `TF_VAR_cloudflare`): Target domain name.
