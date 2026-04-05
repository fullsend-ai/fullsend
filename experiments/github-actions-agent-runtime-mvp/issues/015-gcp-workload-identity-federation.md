# 015: Replace Static GCP SA Key with Workload Identity Federation

## Problem

All four agent workflows authenticate to GCP using a static service account JSON key (`GCP_SA_KEY` stored as a GitHub Actions secret). This key is used for Model Armor prompt injection scanning at every agent entry point.

Static keys:
- Never expire unless manually rotated
- Can be exfiltrated if GitHub Actions secrets are compromised
- Are shared across all agent workflows with identical broad permissions
- Must be copied to every repo that adopts the agent workflows

## Current Pattern

```yaml
- name: Authenticate to Google Cloud
  uses: google-github-actions/auth@v3
  with:
    credentials_json: ${{ secrets.GCP_SA_KEY }}
```

Used in `triage-agent.yml`, `implementation-agent.yml`, `review-agent.yml`, and `fix-agent.yml`.

## Proposed: GCP Workload Identity Federation (WIF)

WIF lets GitHub Actions exchange its OIDC token for short-lived GCP credentials — no static key needed.

### GCP Setup

1. Create a Workload Identity Pool and OIDC provider scoped to the GitHub orgs that run the agent workflows
2. Bind the CI service account to the pool, restricting which repos can impersonate it
3. Optionally scope bindings per-repo or per-org

### Workflow Changes

```yaml
permissions:
  id-token: write  # Required for OIDC token exchange
  # ... existing permissions

- name: Authenticate to Google Cloud
  uses: google-github-actions/auth@v3
  with:
    workload_identity_provider: 'projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/<POOL>/providers/<PROVIDER>'
    service_account: '<SA_EMAIL>'
```

The `google-github-actions/auth@v3` action already supports WIF — no action version change needed.

### Multi-Repo Scaling

New repos are onboarded by adding a SA binding for the repo, not by copying a secret:

```bash
gcloud iam service-accounts add-iam-policy-binding <SA_EMAIL> \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/<POOL>/attribute.repository/<org>/<repo>"
```

This complements [007-reusable-workflows.md](007-reusable-workflows.md) — reusable workflows + WIF means a new repo only needs to call the reusable workflow, no secrets to configure.

## Security Comparison

| Aspect | Static Key (`GCP_SA_KEY`) | Workload Identity Federation |
|--------|--------------------------|-----|
| Credential lifetime | Indefinite until rotated | 1 hour (default) |
| Exfiltration risk | Key usable from anywhere | Token only valid from GitHub Actions |
| Repo scoping | Same key for all repos | Per-repo or per-org binding |
| Rotation | Manual | Automatic (no key to rotate) |
| Audit trail | Key ID only | Full GitHub identity (repo, workflow, ref) |
| Onboarding | Copy secret to each repo | Add IAM binding (no secret) |

## Impact

- Eliminates the `GCP_SA_KEY` secret from all agent workflow repos
- `id-token: write` permission must be added to workflow permissions blocks
- No change to Model Armor scanning logic — only the auth step changes
