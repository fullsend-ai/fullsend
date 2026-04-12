# Plan: Inference Provider Credential Provisioning (Vertex AI)

## Problem

Agents running in GitHub Actions need credentials to call an inference API (currently GCP Vertex AI). During `fullsend admin install`, we need to:

1. Provision or accept GCP service account credentials
2. Upload the credential JSON as `FULLSEND_GCP_SA_KEY_JSON` repo secret on `.fullsend` (NOT org-scoped)
3. Upload the GCP project ID as `GCP_PROJECT_ID` repo secret on `.fullsend`
4. Record the chosen inference provider in `config.yaml`

This must be coded behind an abstract interface so we can support other inference providers (e.g., Bedrock, OpenAI) in the future.

## Three Modes of Operation

| Mode | Input | Actions |
|------|-------|---------|
| 1. Minimal | GCP project ID only | Create service account → create key → upload key + project ID |
| 2. Existing SA | GCP project ID + SA name | Verify SA exists → create key → upload key + project ID |
| 3. Pre-made key | GCP project ID + key JSON | Upload key + project ID |

## Config Change

Add `inference` section to `config.yaml`:

```yaml
version: "1"
dispatch:
  platform: github-actions
inference:
  provider: vertex
defaults:
  # ...
```

## Implementation Plan

### Phase 1: Abstractions and Config

#### 1a. `internal/config/config.go` — Add inference config

- Add `InferenceConfig` struct with `Provider string` field.
- Add `InferenceConfig` field to `OrgConfig`.
- Add validation: provider must be one of `ValidProviders()` (initially just `"vertex"`).
- Update `NewOrgConfig` to accept an inference provider parameter.
- Update `config_test.go` with new validation cases.

#### 1b. `internal/inference/inference.go` — Provider interface

New package `internal/inference/` with:

```go
// Provider provisions inference credentials during install.
type Provider interface {
    // Name returns the provider identifier (e.g. "vertex").
    Name() string

    // Provision acquires credentials and returns secrets to store.
    // Returns a map of secret-name → secret-value pairs to store
    // as repo secrets on .fullsend.
    Provision(ctx context.Context) (map[string]string, error)

    // Validate checks that stored credentials are functional (for Analyze).
    Validate(ctx context.Context) error
}
```

#### 1c. `internal/inference/vertex/vertex.go` — Vertex implementation

New package `internal/inference/vertex/` with:

```go
type Config struct {
    ProjectID          string // required: GCP project ID
    ServiceAccountName string // optional: existing SA name (mode 2)
    CredentialJSON     string // optional: pre-made key JSON (mode 3)
}

type Vertex struct {
    cfg    Config
    gcpAPI GCPClient // interface for testability
}
```

**GCPClient interface** (for mocking in unit tests):

```go
type GCPClient interface {
    // GetServiceAccount checks that a service account exists.
    GetServiceAccount(ctx context.Context, projectID, saName string) error

    // CreateServiceAccount creates a new service account.
    CreateServiceAccount(ctx context.Context, projectID, saName, displayName string) error

    // CreateServiceAccountKey generates a new JSON key for a service account.
    CreateServiceAccountKey(ctx context.Context, projectID, saEmail string) ([]byte, error)
}
```

**LiveGCPClient**: Real implementation using GCP IAM REST API (or `google.golang.org/api/iam/v1`).

**Provision logic:**

```
if cfg.CredentialJSON != "" {
    // Mode 3: key provided directly
    return {"FULLSEND_GCP_SA_KEY_JSON": cfg.CredentialJSON, "GCP_PROJECT_ID": cfg.ProjectID}
}

saName := cfg.ServiceAccountName
if saName == "" {
    // Mode 1: create a new service account
    saName = "fullsend-agent"
    gcpAPI.CreateServiceAccount(ctx, cfg.ProjectID, saName, "Fullsend agent inference")
} else {
    // Mode 2: verify existing SA
    gcpAPI.GetServiceAccount(ctx, cfg.ProjectID, saName)
}

// Create key for the SA (modes 1 and 2)
saEmail := saName + "@" + cfg.ProjectID + ".iam.gserviceaccount.com"
keyJSON := gcpAPI.CreateServiceAccountKey(ctx, cfg.ProjectID, saEmail)
return {"FULLSEND_GCP_SA_KEY_JSON": keyJSON, "GCP_PROJECT_ID": cfg.ProjectID}
```

### Phase 2: Layer

#### 2a. `internal/layers/inference.go` — InferenceLayer

New layer that runs after SecretsLayer (position 4, before DispatchTokenLayer):

```go
type InferenceLayer struct {
    org      string
    client   forge.Client
    provider inference.Provider
    ui       *ui.Printer
}
```

- **Install**: Calls `provider.Provision(ctx)` to get secrets, then stores each as a repo secret on `.fullsend` via `client.CreateRepoSecret()`.
- **Uninstall**: No-op (secrets deleted with repo, same as SecretsLayer).
- **Analyze**: Checks that expected secrets exist (`FULLSEND_GCP_SA_KEY_JSON`, `GCP_PROJECT_ID` for vertex).
- **RequiredScopes**: `["repo"]` for install/analyze.

**Layer ordering** (updated):

1. ConfigRepoLayer
2. WorkflowsLayer
3. SecretsLayer (agent app keys)
4. **InferenceLayer** (inference provider credentials) ← NEW
5. DispatchTokenLayer
6. EnrollmentLayer

Rationale: InferenceLayer needs `.fullsend` repo to exist (created by ConfigRepoLayer) and stores repo-level secrets (like SecretsLayer). It must run before EnrollmentLayer since enrolled repos will need these secrets available.

#### 2b. Unit tests: `internal/layers/inference_test.go`

Test all three modes using a fake GCPClient and forge.FakeClient:
- Mode 1: no SA name, no key → creates SA, creates key, uploads secrets
- Mode 2: SA name provided → verifies SA, creates key, uploads secrets
- Mode 3: key provided → uploads secrets directly (no GCP calls)
- Error cases: SA not found, key creation failure, secret upload failure
- Analyze: all present, none present, partial

### Phase 3: CLI Integration

#### 3a. `internal/cli/admin.go` — New flags and wiring

Add flags to `install` command:
- `--gcp-project` (string): GCP project ID
- `--gcp-service-account` (string): existing SA name (optional)
- `--gcp-credentials-file` (string): path to pre-made key JSON file (optional)

The flags determine which mode is used. Validation:
- If `--gcp-credentials-file` is set, `--gcp-service-account` is ignored (mode 3).
- `--gcp-project` is required when any GCP flag is provided.

Wire the inference provider into `buildLayerStack()` — add `inference.Provider` parameter.

The inference provider choice (`vertex`) gets written into `config.yaml` via the `InferenceConfig` field on `OrgConfig`.

#### 3b. Update `buildLayerStack` and all call sites

Add the InferenceLayer to the stack between SecretsLayer and DispatchTokenLayer. Update:
- `buildLayerStack()` in `admin.go`
- `runDryRun()` — build with a no-op/analyze-only provider
- `runUninstall()` — include InferenceLayer (no-op uninstall)
- `runAnalyze()` — include InferenceLayer for status checking
- `buildTestLayerStack()` in e2e tests

### Phase 4: Tests

#### 4a. Unit tests for `internal/inference/vertex/`

- `vertex_test.go`: Test Provision() for all 3 modes with a fake GCPClient.
- Test SA name defaulting, email construction, error handling.

#### 4b. Unit tests for `internal/layers/inference_test.go`

- Follow existing pattern from `secrets_test.go`.
- Use `forge.FakeClient` for secret storage verification.
- Use a fake inference.Provider that returns canned secrets.

#### 4c. Unit tests for `internal/config/config_test.go`

- Validate inference provider field parsing and validation.
- Test marshal/unmarshal round-trip with inference config.

#### 4d. E2E tests

**In CI**, the `E2E_HALFSEND_VERTEX_KEY` secret contains a pre-made GCP credential JSON. The e2e test will use **mode 3** (key provided directly), since:
- We can't create/delete service accounts in CI (would need org-level GCP permissions).
- A pre-created SA with a pre-made key is the safe, idempotent approach.

**E2E changes:**

- `e2e/admin/testutil.go`: Add `E2E_HALFSEND_VERTEX_KEY` env var loading. Also add `E2E_GCP_PROJECT_ID` (or derive from the key JSON).
- `e2e/admin/admin_test.go`:
  - In `runFullInstall()`: Create a vertex provider in mode 3 using the env var, add InferenceLayer to the test stack.
  - In `verifyInstalled()`: Assert `FULLSEND_GCP_SA_KEY_JSON` and `GCP_PROJECT_ID` repo secrets exist.
  - In `verifyNotInstalled()`: Assert secrets are gone (deleted with repo).
  - In `buildTestLayerStack()`: Accept and include inference provider/layer.
- The e2e test should `t.Skip` the inference portion if `E2E_HALFSEND_VERTEX_KEY` is not set (so existing CI doesn't break until the secret is wired into the workflow).

**Makefile**: Add `E2E_HALFSEND_VERTEX_KEY` to the env vars passed through to `go test` in the `e2e-test` target.

### Phase 5: Documentation Updates

#### 5a. `docs/ADRs/0006-ordered-layer-model.md` — Update layer stack ordering

This is the canonical ADR defining the layer model. The Consequences section lists the current stack as `config-repo → workflows → secrets → dispatch-token → enrollment`. Update to include InferenceLayer at position 4:

`config-repo → workflows → secrets → inference → dispatch-token → enrollment`

#### 5b. `docs/architecture.md` — Update architecture overview

References the layer stack ordering inline. Update the installation model description to reflect the new 6-layer stack.

#### 5c. `docs/normative/admin-install/v1/adr-0011-org-config-yaml/` — Extend config schema

- `SPEC.md`: Add `inference` as a new root-level key with `provider` field. Document that `provider` must be one of the valid providers (initially `vertex`).
- `config.schema.json`: Add `inference` object to the JSON schema with `provider` enum property.

#### 5d. `docs/normative/admin-install/v1/adr-0014-github-apps-and-secrets/SPEC.md` — Extend credential surface

Currently documents only GitHub App secrets (`FULLSEND_<ROLE>_APP_PRIVATE_KEY`, `FULLSEND_<ROLE>_APP_ID`). Add a new section or extend the secrets table to cover inference provider secrets stored on `.fullsend`:

- `FULLSEND_GCP_SA_KEY_JSON` — GCP service account key JSON (repo secret)
- `GCP_PROJECT_ID` — GCP project identifier (repo secret)

Note: these are repo-level secrets on `.fullsend`, not org-level, consistent with the existing credential isolation pattern.

## File Summary

| File | Action |
|------|--------|
| `internal/config/config.go` | Modify — add InferenceConfig |
| `internal/config/config_test.go` | Modify — add inference validation tests |
| `internal/inference/inference.go` | Create — Provider interface |
| `internal/inference/vertex/vertex.go` | Create — Vertex provider + GCPClient interface |
| `internal/inference/vertex/vertex_test.go` | Create — Unit tests |
| `internal/inference/vertex/gcp.go` | Create — LiveGCPClient (real GCP API calls) |
| `internal/layers/inference.go` | Create — InferenceLayer |
| `internal/layers/inference_test.go` | Create — Unit tests |
| `internal/cli/admin.go` | Modify — flags, wiring, buildLayerStack |
| `internal/forge/fake.go` | No change needed (already supports repo secrets) |
| `e2e/admin/testutil.go` | Modify — add vertex key env var |
| `e2e/admin/admin_test.go` | Modify — add inference to install/verify |
| `Makefile` | Modify — pass E2E_HALFSEND_VERTEX_KEY |
| `docs/ADRs/0006-ordered-layer-model.md` | Modify — add InferenceLayer to stack ordering |
| `docs/architecture.md` | Modify — update layer stack reference |
| `docs/normative/.../adr-0011-.../SPEC.md` | Modify — add `inference` section |
| `docs/normative/.../adr-0011-.../config.schema.json` | Modify — add `inference` to schema |
| `docs/normative/.../adr-0014-.../SPEC.md` | Modify — add inference secrets to credential surface |

## Open Questions

1. **SA naming convention**: Should mode 1 use `fullsend-agent` as the default SA name, or something org-specific like `fullsend-{org}`?
2. **Key rotation**: Should we handle the case where the SA already has 10 keys (GCP limit)? Could list and delete oldest.
3. **GCP auth for SA creation**: ~~Should we use Application Default Credentials, or require a separate flag?~~ **Decided:** Use Application Default Credentials via `golang.org/x/oauth2/google.FindDefaultCredentials()`. This eliminates the PATH-hijack risk of shelling out to `gcloud` and is the standard Go approach.
4. **Vertex AI API enablement**: Should we check/enable the Vertex AI API on the project, or just document it as a prerequisite?
5. **Multiple inference providers**: The config supports `provider: vertex` but should we plan for `provider: bedrock` shape now, or keep the interface minimal and extend later?
