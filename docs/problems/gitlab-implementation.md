# GitLab Support Implementation Details

> **Superseded.** This document was written for [ADR 0028](../ADRs/0028-gitlab-support.md), which has been superseded by [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md). See [gitlab-support.md](../plans/gitlab-support.md) for the current implementation plan.

This document contains implementation details for the original GitLab support approach in fullsend. For the current architectural decision and rationale, see [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md).

## Table of Contents

1. [Shim Pipeline Security](#shim-pipeline-security)
2. [Cross-Repo Dispatch Mechanism](#cross-repo-dispatch-mechanism)
3. [Stage Markers](#stage-markers)
4. [Event Mapping](#event-mapping)
5. [State Machine Primitives](#state-machine-primitives)
6. [Implementation Phases](#implementation-phases)
7. [Forge Interface Evolution](#forge-interface-evolution)
8. [CLI Changes Required](#cli-changes-required)
9. [Security Considerations](#security-considerations)

## Shim Pipeline Security

**GitHub**: `pull_request_target` ensures the shim workflow runs the base branch version, preventing untrusted PR code from modifying the workflow to exfiltrate secrets.

**GitLab**: No `pull_request_target` equivalent. The protected-branch pipeline approach (using `CI_COMMIT_REF_PROTECTED == "true"`) conflicts with MR-event triggering (which runs on unprotected MR source branches), so a different architecture is required.

**Webhook-based approach**: Instead of a shim pipeline in the enrolled repo, use GitLab webhooks to trigger `.fullsend` pipelines directly:

1. **Webhook configuration**: Enrolled repos configure webhooks that POST to `.fullsend` project's pipeline trigger endpoint
2. **Webhook authentication**: GitLab webhooks include a secret token, which `.fullsend` validates before processing
3. **Trigger pipeline on protected branch**: The webhook triggers a pipeline in `.fullsend` on the `main` branch (protected), not in the enrolled repo
4. **No untrusted code execution**: MR code never executes in a pipeline context — webhook payload is parsed by GitLab's webhook system, then triggers `.fullsend`

**Why webhooks for GitLab but not GitHub**: ADR-0009 (pull_request_target security model for GitHub) explicitly rejected webhook-based dispatch because it "requires a hosted webhook receiver, breaking compute-platform agnosticism." GitLab's situation is similar but with a critical difference:

**Webhook-to-trigger API incompatibility**: GitLab webhooks send JSON event payloads (merge request objects, issue events), while the pipeline trigger API (`/api/v4/projects/:id/trigger/pipeline`) expects form-encoded parameters (`token`, `ref`, `variables[KEY]=value`). These are not wire-compatible — pointing a webhook URL directly at the trigger endpoint results in a malformed request. This means an intermediary is required to translate webhook payloads to trigger API calls.

**Options for webhook translation**:
1. **GitLab CI/CD webhook integration**: Use a lightweight `.gitlab-ci.yml` job in the enrolled repo (there is no native GitLab mechanism to trigger a pipeline directly from a webhook event; the enrolled repo would need a pipeline triggered by another mechanism that then calls the trigger API) and calls the `.fullsend` trigger API. This keeps everything within GitLab CI/CD but **does not solve the security model** — enforcing protected-branch-only execution via `workflow:rules` prevents the pipeline from reacting to merge request events (which occur on unprotected branches), defeating the purpose. Without protected-branch enforcement, MR code can modify the webhook job.
2. **GitLab serverless functions**: Use GitLab's serverless integration to deploy a function that receives webhooks and translates to trigger API calls. Maintains compute-platform agnosticism (runs within GitLab infrastructure) but requires GitLab Premium/Ultimate tier.
3. **Minimal bridge service**: Deploy a lightweight translation service (e.g., Cloud Run, Lambda) that receives webhooks and POSTs to the trigger API. This reintroduces the "hosted webhook receiver" concern from ADR-0009 but may be acceptable given GitLab's lack of a direct webhook-to-pipeline primitive.

**Resolved**: [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md) chose option 3 (minimal bridge service deployed as a Cloud Function) as the webhook translation mechanism. See that ADR for the full rationale and the companion implementation plan at [gitlab-support.md](../plans/gitlab-support.md).

**Security requirements for webhook translation intermediary**:

The intermediary (serverless function or bridge service) is a critical security component. It MUST:

1. **Hardcode target ref**: Always call `.fullsend` trigger API with `ref=main`. MUST NOT derive ref from webhook payload fields or allow ref to be specified by the webhook sender.
2. **Validate webhook secret**: Verify the webhook secret token matches the expected value before forwarding to trigger API.
3. **Use least-privilege credentials**: Deploy with only the trigger token, no other `.fullsend` repository access.
4. **Input validation**: Validate webhook payload structure before parsing to prevent injection attacks.

**Rationale**: The intermediary controls which branch the `.fullsend` dispatch pipeline runs on. If compromised or misconfigured to accept attacker-controlled `ref` values, an insider with `.fullsend` write access could:
1. Push a malicious branch to `.fullsend` that exfiltrates secrets
2. Compromise the intermediary to trigger pipelines on that branch via `ref=malicious-branch`

Defense-in-depth (protected variables, see Security Considerations section) mitigates this, but the intermediary MUST enforce ref=main as primary control.

### Webhook Payload Validation

This snippet illustrates the security-critical validation logic. For the complete dispatch pipeline including stage fan-out, see [Cross-Repo Dispatch Mechanism](#cross-repo-dispatch-mechanism).

```yaml
# fullsend-stage: dispatch
# Triggered by webhooks from enrolled repos

dispatch:
  stage: dispatch
  image: alpine:latest
  rules:
    # Only run on protected branch (main)
    - if: $CI_COMMIT_REF_PROTECTED == "true"
  script:
    - apk add --no-cache yq jq curl bash
    # NOTE: The script below uses bash for ${!VAR} indirect expansion. Alpine's
    # default /bin/sh (ash) does not support this bashism. Use 'bash -c' or
    # switch to 'eval' / 'printenv' for POSIX compatibility.
    - bash <<'BASH'
      # Validate inputs and webhook token
      SOURCE_PROJECT="${SOURCE_PROJECT}"
      WEBHOOK_TOKEN="${WEBHOOK_TOKEN}"

      # Validate SOURCE_PROJECT format before using in variable lookup
      # GitLab project paths support dots (.), nested groups (group/subgroup/project),
      # and standard word characters. Expanded regex for production use:
      # ^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+$
      # This illustrative code uses simplified two-segment validation for clarity.
      # Production implementations should use the expanded regex above and update
      # the yq query to use bracket notation for project names containing dots
      # (e.g., yq '.["repos"]["my.project"]["enabled"]').
      # TODO(implementation): Expand regex to support dots and nested groups
      if [[ ! "$SOURCE_PROJECT" =~ ^[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+$ ]]; then
        echo "ERROR: Invalid source project format"
        exit 1
      fi

      # Look up expected token from masked CI/CD variable
      # Variable name format: WEBHOOK_TOKEN_<sanitized_project_path>
      # Token variable naming: Use SHA256 hash of project path for collision-free
      # encoding (GitLab var names must match [A-Za-z_][A-Za-z0-9_]*).
      # Alternative sanitization (/ → __, - → _H_) is collision-prone for
      # pathological names like "foo_H_bar/baz" vs "foo-bar/baz".
      # Examples:
      #   myorg/myrepo → WEBHOOK_TOKEN_<sha256_hash>
      #   my-org/my-repo → WEBHOOK_TOKEN_<different_sha256_hash> (distinct)
      PROJECT_HASH=$(echo -n "${SOURCE_PROJECT}" | sha256sum | cut -d' ' -f1)
      EXPECTED_TOKEN_VAR="WEBHOOK_TOKEN_${PROJECT_HASH}"
      EXPECTED_TOKEN="${!EXPECTED_TOKEN_VAR}"

      if [ -z "${EXPECTED_TOKEN}" ]; then
        echo "ERROR: No webhook token configured for ${SOURCE_PROJECT}"
        exit 1
      fi

      # SECURITY: Use hash comparison to mitigate timing side-channel attacks
      # that could leak the token byte-by-byte. Compare SHA256 hashes instead
      # of raw strings. Note: The bash [ ] comparison is not constant-time, but
      # it compares hash bytes (not token bytes), limiting information leakage.
      WEBHOOK_HASH=$(echo -n "$WEBHOOK_TOKEN" | openssl dgst -sha256 -hex | cut -d' ' -f2)
      EXPECTED_HASH=$(echo -n "$EXPECTED_TOKEN" | openssl dgst -sha256 -hex | cut -d' ' -f2)
      if [ "$WEBHOOK_HASH" != "$EXPECTED_HASH" ]; then
        echo "ERROR: Invalid webhook token"
        exit 1
      fi

      # Validate source project is enrolled
      # Use full project path to avoid collisions (group1/myproject vs group2/myproject)
      # For nested groups (group/subgroup/project), config.yaml uses dot-separated keys
      # (e.g., repos."group.subgroup.project".enabled)
      CONFIG_KEY=$(echo "${SOURCE_PROJECT}" | tr '/' '.')
      ENABLED=$(yq ".repos.\"${CONFIG_KEY}\".enabled" config.yaml)
      if [ "$ENABLED" != "true" ]; then
        echo "ERROR: Project not enrolled: ${SOURCE_PROJECT}"
        exit 1
      fi

      # Webhook payload will be base64-encoded before passing to child pipeline
      # to prevent YAML injection attacks via event content
      BASH
```

### Enrollment Setup

- `fullsend admin install` creates webhook in enrolled repo via GitLab API
- Webhook URL: Points to the translation intermediary (serverless function or bridge service) that forwards to `.fullsend` trigger API. See "Webhook-to-trigger API incompatibility" above for translation options.
- Webhook triggers: Merge Request events, Issue events, Note events
- Webhook secret token: stored as masked CI/CD variable in `.fullsend` project (variable name: `WEBHOOK_TOKEN_<sha256(project_path)>` for collision-free project identification), validated by dispatch pipeline after translation

### Security Properties

- Webhook payload constructed by GitLab, not by MR author code
- Dispatch pipeline runs on `.fullsend` protected `main` branch
- Token validation prevents unauthorized repos from triggering workflows (implementations MUST use constant-time comparison to prevent timing side-channel attacks)
- Webhook token variable names use SHA256 hashing for collision-free project identification (`WEBHOOK_TOKEN_<sha256(project_path)>`)
- MR source code never executes in a pipeline with access to fullsend secrets

**Key difference from GitHub**: Webhooks replace the in-repo shim. This is architecturally cleaner for GitLab's security model but requires webhook management (creation, token rotation) in the admin install flow.

## Cross-Repo Dispatch Mechanism

**End-to-end flow**: Enrolled repos send webhooks to `.fullsend` project's pipeline trigger endpoint → webhook triggers the dispatch pipeline on `.fullsend` protected `main` branch → dispatch pipeline validates the webhook token and source project → dispatch pipeline scans for stage workflows matching the requested stage → dispatch pipeline generates a child pipeline config and triggers it → child pipeline runs the stage workflow (triage, code, review, or fix) with the event payload and source project context.

**Relationship to Shim Pipeline Security**: The shim pipeline security section presents the webhook validation portion of the dispatch pipeline, focusing on the security properties (protected branch execution, token validation). This section presents the complete dispatch pipeline architecture, including the stage fan-out logic. Both sections describe the same `dispatch.yml` pipeline — the shim section shows the validation front-end, this section shows the full implementation including child pipeline generation. **Implementation note**: The validation logic (SOURCE_PROJECT format check, enrollment check, token validation) appears in both snippets for clarity in this design document. During implementation, this should be extracted into a shared script or bash function to avoid maintenance drift between the two job definitions.

**GitHub**: `workflow_dispatch` API call with input parameters
**GitLab**: Pipeline trigger API with pipeline variables + child pipelines

### Trigger Token Creation

- Created via GitLab API: `POST /projects/:id/triggers` for the `.fullsend` project
- The token itself authorizes triggering pipelines only in the `.fullsend` project
- Stored as project-level CI/CD variable `FULLSEND_DISPATCH_TOKEN` in the `.fullsend` project (scoped to `.fullsend` only; enrolled repos never see this token)

**Security consideration - dispatch token exposure**: The webhook-based architecture (chosen approach, see [Shim Pipeline Security](#shim-pipeline-security)) avoids exposing `FULLSEND_DISPATCH_TOKEN` to enrolled repos entirely. Enrolled repos send webhooks with webhook secret tokens for authentication, and the `.fullsend` dispatch pipeline uses its internal `FULLSEND_DISPATCH_TOKEN` to trigger child pipelines — enrolled repo code never sees the dispatch token. This is a key security advantage of the webhook approach over alternatives like in-repo shim workflows.

**Defense-in-depth with protected variables**: All CI/CD variables containing secrets (including `FULLSEND_DISPATCH_TOKEN`) MUST be marked as "protected" in GitLab. This ensures they are only exposed to pipelines running on protected branches (main). This provides defense-in-depth even though the webhook-based architecture is designed to always trigger on `ref=main`:

- **Protects against intermediary compromise**: If the webhook translation service has a bug or is compromised to call trigger API with `ref=attacker-branch`, secrets will not be exposed
- **Protects against insider threats**: If an insider with `.fullsend` write access creates a malicious branch, they cannot exfiltrate secrets even if the dispatch pipeline somehow runs on that branch
- **Low-cost, high-value control**: Marking variables as protected has no operational overhead and provides strong guarantees

See [Protected CI/CD Variables](#protected-cicd-variables-defense-in-depth) section for detailed threat model.

### Dispatch Workflow

`.gitlab/ci/dispatch.yml`:

```yaml
# fullsend-stage: dispatch
# Dispatcher pipeline that fans out to stage pipelines via child pipelines
# Split into two jobs: generate-config creates the child pipeline config,
# trigger-stage triggers it as a downstream pipeline

workflow:
  rules:
    # Run dispatch logic only when not already in a child pipeline
    - if: $IS_CHILD_PIPELINE != "true"

generate-config:
  stage: prepare
  image: alpine:latest
  script:
    - apk add --no-cache yq jq gettext bash
    # NOTE: The script below uses bash for [[ regex ]] syntax. Alpine's default
    # /bin/sh (ash) does not support this bashism.
    - bash <<'BASH'
      # Validate and extract inputs
      # NOTE: The validation logic below (SOURCE_PROJECT format, enrollment
      # check) is duplicated from the shim security snippet. Both sections
      # describe the same dispatch.yml pipeline. See the shim security
      # section for the security rationale behind this validation.
      STAGE="${STAGE}"
      SOURCE_PROJECT="${SOURCE_PROJECT}"

      # Validate SOURCE_PROJECT format before using in variable lookup
      # NOTE: This regex should be expanded during implementation to include
      # dots (.) and nested groups, which are valid in GitLab project paths
      # (e.g., my.org/my.project or group/subgroup/project)
      # TODO(implementation): Expand regex to support dots and nested groups
      if [[ ! "$SOURCE_PROJECT" =~ ^[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+$ ]]; then
        echo "ERROR: Invalid source project format"
        exit 1
      fi

      # Validate source project is enrolled
      # Use full project path to avoid collisions (group1/myproject vs group2/myproject)
      # For nested groups (group/subgroup/project), config.yaml uses dot-separated keys
      # (e.g., repos."group.subgroup.project".enabled)
      CONFIG_KEY=$(echo "${SOURCE_PROJECT}" | tr '/' '.')
      ENABLED=$(yq ".repos.\"${CONFIG_KEY}\".enabled" config.yaml)
      if [ "$ENABLED" != "true" ]; then
        echo "ERROR: Project not enrolled: ${SOURCE_PROJECT}"
        exit 1
      fi

      # Scan for workflows with matching stage marker
      # NOTE: This assumes one file per stage (e.g., only one file has
      # "# fullsend-stage: triage"). If multiple files match, only the first
      # match is used. The architecture expects each stage workflow to be in
      # a separate file (triage.yml, code.yml, review.yml, fix.yml).
      MATCHED=false
      for pipeline_file in .gitlab/ci/*.yml; do
        STAGE_MARKER=$(grep -E '^# fullsend-stage:' "$pipeline_file" | head -1 | sed 's/^# fullsend-stage: *//')

        if [ "$STAGE_MARKER" = "$STAGE" ]; then
          echo "Generating child pipeline config for: $pipeline_file"
          # Create child pipeline config without injecting EVENT_PAYLOAD
          # Event payload passed via trigger API variables, not embedded in YAML
          # NOTE: include:local: resolves files relative to the same repository
          # and ref as the parent pipeline. Since the dispatch pipeline runs on
          # .fullsend's protected main branch, the child pipeline includes stage
          # files (triage.yml, code.yml, etc.) from the same protected ref.
          # Two-step substitution pattern (heredoc + envsubst):
          # The heredoc uses single-quoted 'EOF' to prevent shell expansion,
          # writing $pipeline_file literally into the YAML. Then envsubst
          # performs the substitution in a controlled way. This prevents
          # accidental variable expansion during the heredoc write step.
          # Stage files are constrained to .gitlab/ci/*.yml by the loop,
          # so $pipeline_file is safe for substitution (no shell metacharacters).
          export pipeline_file
          cat > .gitlab-ci-child.yml <<'EOF'
      include:
        - local: '$pipeline_file'

      variables:
        IS_CHILD_PIPELINE: "true"
      EOF
          # envsubst replaces $pipeline_file with its value from environment
          # Restrict substitution to $pipeline_file only (not all env vars)
          envsubst '$pipeline_file' < .gitlab-ci-child.yml > .gitlab-ci-child.yml.tmp
          mv .gitlab-ci-child.yml.tmp .gitlab-ci-child.yml
          MATCHED=true
          break  # Exit after first match
        fi
      done

      if [ "$MATCHED" != "true" ]; then
        echo "ERROR: No workflow found for stage: $STAGE"
        exit 1
      fi
      BASH
  artifacts:
    paths:
      - .gitlab-ci-child.yml
    expire_in: 1 hour
  rules:
    - if: $STAGE

trigger-stage:
  stage: deploy
  needs:
    - generate-config
  trigger:
    include:
      - artifact: .gitlab-ci-child.yml
        job: generate-config
    strategy: depend
  variables:
    # Pass event payload and source project via trigger variables (safe serialization)
    # Base64-encode EVENT_PAYLOAD to prevent YAML injection
    EVENT_PAYLOAD_B64: ${EVENT_PAYLOAD_B64}
    SOURCE_PROJECT: ${SOURCE_PROJECT}
  rules:
    - if: $STAGE
```

**Two-job pattern**: GitLab CI requires separating config generation (`script:`) from pipeline triggering (`trigger:`). The `generate-config` job creates the child pipeline YAML as an artifact without embedding untrusted event content. The `trigger-stage` bridge job then triggers the child pipeline, passing the event payload safely via base64-encoded variables. This prevents YAML injection attacks where attacker-controlled event content (issue titles, MR descriptions) could break out of the `variables:` block and inject arbitrary pipeline configuration.

**Child pipeline approach**: Uses GitLab's `trigger: include: artifact:` pattern to create child pipelines. The `IS_CHILD_PIPELINE` variable prevents the dispatch workflow from running recursively.

## Stage Markers

**Pattern**: Same `# fullsend-stage: <name>` comment convention as GitHub
**Location**: Top of `.gitlab/ci/*.yml` files

This keeps the dispatch scanning logic identical across GitHub and GitLab.

## Event Mapping

| GitHub Event | GitLab Event | Trigger Mechanism |
|-------------|--------------|-------------------|
| issues.labeled | issue (labels changed) | Webhook → .fullsend dispatch pipeline |
| issue_comment.created | note (on issue) | Webhook → .fullsend dispatch pipeline |
| pull_request_target | merge_request_event | Webhook → .fullsend dispatch pipeline |
| pull_request_review.submitted | Merge request approval + Note events (review comments) | Webhook → .fullsend dispatch pipeline (requires multiple GitLab event types to replicate full GitHub review behavior: approvals via MR approval API, review comments via Notes) |

**GitLab webhook limitations**:
- No direct equivalent to GitHub's granular event types
- Must filter events in dispatch pipeline by inspecting webhook payload (`EVENT_PAYLOAD_B64`), not via CI predefined variables (dispatch pipelines triggered via trigger API have `CI_PIPELINE_SOURCE == "trigger"`, so MR-specific variables like `CI_MERGE_REQUEST_EVENT_TYPE` are not available)
- Issue webhooks don't include label details in all cases (may need API call)

## State Machine Primitives

**Labels**: GitLab labels work nearly identically to GitHub
- Same label names: `ready-to-code`, `ready-for-review`, `ready-for-merge`, `needs-info`
- Applied via GitLab API: `PUT /projects/:id/merge_requests/:iid` with `labels`, `add_labels`, or `remove_labels` parameters
- Scoped to projects (not group-level by default)

**Approval rules**: GitLab has native approval mechanisms:
- Required approvals count
- Code owners approvals (similar to GitHub CODEOWNERS)
- Can integrate with fullsend review stage

## Implementation Phases

**Phase 1: Forge abstraction**
- Implement `internal/forge/gitlab/gitlab.go` with `forge.Client` interface
- Add GitLab API client (use `go-gitlab` library)
- Implement equivalent methods for repos, MRs, labels, CI/CD variables, pipeline triggers

**Phase 2: CI/CD templates**
- Create `.gitlab/ci/dispatch.yml` dispatcher
- Create stage pipelines (triage.yml, code.yml, review.yml, fix.yml)
- Create `templates/shim-pipeline.yml` template
- Add scripts for GitLab-specific operations (parallel to `.github/scripts/`)

**Phase 3: Forge-neutral interface evolution**
- Add forge-neutral methods to `forge.Client` (`CreateRoleCredential`, `TriggerPipeline`, `CreateWebhook`)
- Implement GitLab-specific versions of these methods in `internal/forge/gitlab/`
- Update `appsetup` to use `CreateRoleCredential()` instead of GitHub App-specific code
- Update `layers` to ask forge.Client for template paths and enrollment snippets (pushes forge-specific logic into Client implementations)
- Move forge detection to `internal/forge/detect.go` per ADR-0005 boundary rule
- Add `--forge` flag to `fullsend admin install` for manual override

**Phase 4: Configuration**
- Add `forge: github` or `forge: gitlab` to `config.yaml`
- Support forge-specific settings (GitLab instance URL for self-hosted)
- Update config schema and validation

**Phase 5: Testing**
- E2E tests against GitLab.com or GitLab test instance
- Parallel GitHub/GitLab test suite
- Migration testing (GitHub → GitLab config equivalence)

## Forge Interface Evolution

**Challenge**: ADR-0005 promises "Adding a new forge requires implementing `forge.Client` — no changes to layers, CLI, or app setup code." However, the current `forge.Client` interface contains GitHub-specific methods (`ListOrgInstallations`, `GetAppClientID`) and operations (`DispatchWorkflow`) that don't map directly to GitLab.

### Proposed Forge-Neutral Interface Additions

These methods follow ADR-0005's forge-neutral vocabulary convention (e.g., `ChangeProposal` instead of "pull request" or "merge request"). The term `RoleCredential` is the forge-neutral abstraction for GitHub Apps (GitHub) and Project Access Tokens (GitLab).

```go
// Credential management (replaces GitHub App-specific methods)
// CreateRoleCredential creates a scoped credential for a specific role
// (triage, code, review, fix). For GitHub, this would create/configure
// a GitHub App. For GitLab, this would create a Project Access Token.
// The forge-neutral term "RoleCredential" abstracts over forge-specific
// authentication mechanisms.
CreateRoleCredential(ctx context.Context, role, owner, repo string, permissions []string) (credentialID string, err error)

// RevokeRoleCredential removes a previously created role credential.
RevokeRoleCredential(ctx context.Context, owner, repo, credentialID string) error

// GetRoleCredentialValue retrieves the secret value for a role credential
// (for storing in CI/CD secrets). For GitHub Apps, this generates an
// installation token. For GitLab PATs, this returns the token value.
GetRoleCredentialValue(ctx context.Context, owner, repo, credentialID string) (string, error)

// Pipeline/workflow triggering (replaces DispatchWorkflow)
// TriggerPipeline initiates a CI/CD pipeline for a specific stage.
// For GitHub, this calls workflow_dispatch. For GitLab, this uses the
// pipeline trigger API with variables.
TriggerPipeline(ctx context.Context, owner, repo, stage string, variables map[string]string) error

// Webhook management (GitLab-specific for security model)
// CreateWebhook configures a webhook from source repo to .fullsend project.
// For GitHub, this is a no-op (uses in-repo shim). For GitLab, this
// creates a project webhook with a secret token.
CreateWebhook(ctx context.Context, owner, repo, targetURL, secretToken string, events []string) error
DeleteWebhook(ctx context.Context, owner, repo, webhookID string) error
```

### Existing GitHub-Specific Methods

- `ListOrgInstallations(ctx, org) ([]Installation, error)` — GitHub App-specific. GitLab equivalent would list Project Access Tokens, but tokens are scoped per-project not org-wide. This method may need to become forge-specific or return an empty list for non-GitHub forges.
- `GetAppClientID(ctx, slug) (string, error)` — GitHub App-specific. No GitLab equivalent. This should be deprecated or moved to a GitHub-specific extension interface.
- `DispatchWorkflow(ctx, owner, repo, workflowFile, ref, inputs)` — GitHub Actions-specific (targets a specific workflow file). Replaced by forge-neutral `TriggerPipeline` above.

### Backward Compatibility and Migration Strategy

To prevent interface bloat while maintaining backward compatibility:

1. **Deprecation phase**: Mark GitHub-specific methods with deprecation comments and update callers to use forge-neutral equivalents (`TriggerPipeline` instead of `DispatchWorkflow`, etc.). This phase allows gradual migration without breaking existing code.
2. **Extension interfaces**: Move forge-specific methods that have no neutral equivalent (e.g., `GetAppClientID`) to optional extension interfaces:
   ```go
   type GitHubForgeClient interface {
       Client
       GetAppClientID(ctx context.Context, slug string) (string, error)
   }
   ```
   Callers that need GitHub-specific behavior can type-assert to the extension interface.
3. **Breaking change timeline**: After all internal callers migrate to forge-neutral methods, remove deprecated methods in a major version bump. Document this timeline in the interface godoc (e.g., "deprecated: use TriggerPipeline, will be removed in v2.0.0").

This strategy limits interface growth to forge-neutral primitives while preserving GitHub-specific functionality via opt-in extension interfaces.

### Minimizing Layer/CLI/Appsetup Changes

By adding the forge-neutral methods above, the implementation phases can be revised to keep layer/CLI changes minimal:

- **appsetup**: Use `CreateRoleCredential` instead of directly creating GitHub Apps or Project Access Tokens. The forge implementation handles the forge-specific details.
- **layers/workflows**: Use forge-agnostic template deployment (the forge.Client implementation knows whether to deploy `.github/` or `.gitlab/` based on forge type).
- **CLI**: Forge detection (`detectForge`) moves to `internal/forge/detect.go` per ADR-0005's boundary rule. CLI calls `forge.DetectForge(repoURL)` instead of implementing detection itself.

**Note on interface design scope**: This document proposes the architectural direction for forge-neutral interface evolution (add `CreateRoleCredential`, `TriggerPipeline`, etc.) to uphold ADR-0005's promise of minimal layer/CLI changes. The detailed API signatures, error handling, and return types for these methods require separate design work and should be documented in a follow-up design document or implementation PR. The exact method signatures shown above are illustrative, not normative.

## CLI Changes Required

### Forge Detection

```go
// NOTE: Per ADR-0005's boundary rule ("No code outside internal/forge/ imports
// forge-specific packages directly"), this function should be implemented in
// internal/forge/detect.go rather than internal/cli/admin.go, and called by the
// CLI. The location shown here (internal/cli/admin.go) is for illustration only.
func detectForge(repoURL string) (string, error) {
    u, err := url.Parse(repoURL)
    if err != nil {
        return "", fmt.Errorf("invalid repo URL: %w", err)
    }
    host := strings.ToLower(u.Hostname())

    // Use allowlist for known forge domains to prevent subdomain spoofing
    // (e.g., evil.github.com should not be detected as GitHub)
    knownGitHubDomains := map[string]bool{
        "github.com": true,
        // Add GitHub Enterprise domains as needed: "ghe.example.com": true,
    }
    knownGitLabDomains := map[string]bool{
        "gitlab.com": true,
        // Add self-hosted GitLab domains as needed: "gitlab.example.com": true,
    }

    if knownGitHubDomains[host] {
        return "github", nil
    }
    if knownGitLabDomains[host] {
        return "gitlab", nil
    }

    // For self-hosted instances, require explicit --forge flag
    return "", fmt.Errorf("unknown forge: %s (use --forge flag for self-hosted instances)", repoURL)
}
```

### Install Command Changes

- Add `--forge {github|gitlab}` flag (auto-detected if not specified)
- Add `--gitlab-url` for self-hosted GitLab instances
- Update app setup flow to create Project Access Tokens for GitLab
- Update workflows layer to deploy `.gitlab/` instead of `.github/`

### Config Schema Changes

```yaml
# config.yaml
forge: gitlab  # or "github"
gitlab_instance_url: https://gitlab.example.com  # optional, defaults to gitlab.com
```

### New Packages

- `internal/forge/gitlab/` - GitLab client implementation
- `internal/scaffold/fullsend-repo/.gitlab/` - GitLab CI/CD templates
- `internal/scaffold/fullsend-repo/.gitlab/scripts/` - GitLab-specific scripts

### Modified Packages

Modified packages (minimized via forge.Client abstraction):

- `internal/forge/forge.go` - Add forge-neutral methods (`CreateRoleCredential`, `TriggerPipeline`, `CreateWebhook`) and deprecate GitHub-specific methods
- `internal/forge/detect.go` (new) - Forge detection logic (moved from CLI per ADR-0005 boundary rule)
- `internal/config/config.go` - Add `forge` field to config schema (minimal change: one field addition)
- `internal/appsetup/` - Use `forge.Client.CreateRoleCredential()` instead of GitHub App-specific code (forge-agnostic caller, forge-specific implementation)
- `internal/layers/workflows.go` - Ask forge.Client for template directory path instead of conditionally choosing `.github/` or `.gitlab/` (pushes forge-specific logic into Client implementation)
- `internal/layers/enrollment.go` - Ask forge.Client for enrollment snippet instead of hardcoding shim workflow syntax (pushes forge-specific logic into Client implementation)

## Security Considerations

### Protected Branch Requirement

- Must be enforced before enrollment
- CLI validates via GitLab API: `GET /projects/:id/protected_branches/:branch`
- Error if `main` is not protected

### Protected CI/CD Variables (Defense-in-Depth)

**All secrets stored in `.fullsend` project MUST be marked as "protected" variables.** This is a critical defense-in-depth control.

**Required protected variables**:
- `FULLSEND_DISPATCH_TOKEN` (trigger token for child pipelines)
- `FULLSEND_TRIAGE_TOKEN`, `FULLSEND_CODE_TOKEN`, `FULLSEND_REVIEW_TOKEN`, `FULLSEND_FIX_TOKEN` (per-role credentials)
- `WEBHOOK_TOKEN_<sha256(project_path)>` (webhook validation tokens for each enrolled repo)
- Any GCP/Anthropic/cloud provider credentials used by agents

**How protected variables work**: GitLab restricts protected variables to pipelines running on protected branches only. Pipelines triggered on unprotected branches cannot access these variables, regardless of how the pipeline was triggered (webhook, trigger API, manual, etc.).

**Threat model**: This defends against:

1. **Webhook intermediary compromise**: If the translation service is compromised or contains a bug that allows triggering `.fullsend` pipelines on attacker-controlled refs (e.g., `ref=evil-branch` instead of `ref=main`), the secrets will not be exposed because the branch is not protected.

2. **Insider threat with .fullsend write access**: An insider who can push branches to `.fullsend` (but cannot bypass protected branch rules on `main`) could create a malicious branch that exfiltrates secrets. If the dispatch pipeline somehow runs on that branch, protected variables prevent secret exposure.

3. **Misconfiguration**: If the trigger API call is misconfigured to use a variable ref instead of hardcoded `ref=main`, protected variables limit the blast radius.

**GitLab API documentation confirms**: The `/api/v4/projects/:id/trigger/pipeline` endpoint accepts a required `ref` parameter that specifies which branch/tag to run the pipeline on. Anyone with a trigger token can specify any ref with no documented restrictions. Protected variables are the primary control preventing secret exposure on non-protected branches.

**Implementation**:
1. Navigate to `.fullsend` project > Settings > CI/CD > Variables
2. For each secret variable, click Edit
3. Check "Protect variable" checkbox
4. Save changes

**Verification**: `fullsend admin install` should validate that all required variables are marked as protected before completing enrollment.

### Token Scoping

- Project Access Tokens scoped to specific projects, not group-wide
- Separate token per enrolled project for code/review/fix roles
- Dispatch token is group-level variable but only triggers `.fullsend` project

### Webhook Authenticity

- GitLab webhooks include secret token for verification
- Dispatch pipeline validates webhook token against config.yaml before processing
- Invalid tokens result in immediate pipeline failure

### MR Source Checkout Prevention

- Webhook-based architecture eliminates MR code execution risk
- Dispatch pipeline runs on `.fullsend` protected branch, not enrolled repo
- MR metadata passed via webhook payload, constructed by GitLab (not MR author)

### Multi-Project Pipelines

GitLab supports [multi-project pipelines](https://docs.gitlab.com/ee/ci/pipelines/downstream_pipelines.html) that trigger pipelines across project boundaries. This feature is **not suitable** for fullsend's architecture:

**Why not multi-project pipelines**:
- Would require triggering FROM `.fullsend` TO enrolled repos, running MR code with potential access to secrets
- GitLab explicitly warns: "Do not use this method to pass masked variables to a multi-project pipeline. The CI/CD masking configuration is not passed to the downstream pipeline."
- Violates the security principle that untrusted MR code never executes in a context with access to fullsend secrets

**Chosen approach**: Parent-child pipelines within `.fullsend` project
- Dispatch pipeline → child pipeline (triage/code/review/fix)
- All run in same project, same protected `main` ref
- Child pipelines inherit protected branch status and can access protected variables
- MR code runs in separate sandboxed environments created by agents, not in CI/CD pipelines with secret access

## Open questions

### Webhook-to-Trigger Translation Architecture

**Problem**: GitLab webhooks (JSON payloads) and the pipeline trigger API (form-encoded parameters) are not wire-compatible. An intermediary is required to translate webhook events to trigger API calls.

**Options**:
1. **GitLab CI/CD webhook integration**: Runs in enrolled repo, but cannot enforce protected-branch-only execution without blocking MR reactions entirely. Reintroduces security concern.
2. **GitLab serverless functions**: Keeps compute within GitLab infrastructure, but requires GitLab Premium/Ultimate tier.
3. **Minimal bridge service**: Works on GitLab Free tier, but reintroduces hosted webhook receiver concern from ADR-0009.

**Status**: Resolved. [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md) chose the minimal bridge service (option 3) with a GCP Cloud Function deployment model.

### Forge Interface Design Details

**Problem**: The forge-neutral interface methods (`CreateRoleCredential`, `TriggerPipeline`, `CreateWebhook`) proposed in this document are illustrative. The detailed API signatures, error handling, return types, and edge case behaviors require separate design work.

**Status**: The architectural direction is established (add forge-neutral primitives that push forge-specific logic into Client implementations). Implementation should be documented in a follow-up design document or implementation PR with concrete API signatures, error semantics, and migration strategy for existing callers.

### Agent Execution Environment (Sandbox/Compute Architecture)

**Problem**: This document does not specify how agents execute on GitLab runners - executor types (docker, kubernetes, shell), isolation model, runner registration requirements, or OpenShell integration specifics.

**Status**: **Explicitly scoped out of this document.** The agent execution environment is orthogonal to the CI/CD dispatch architecture. GitLab runner configuration, sandbox isolation, and compute architecture should be documented separately, potentially in:
- Agent Infrastructure design doc (for compute/isolation model)
- Implementation PR for GitLab runner setup (for executor configuration)
- Deployment guide (for runner registration and management)

**Assumption**: Agents will execute in isolated environments (containers or VMs) managed by GitLab runners, similar to the current GitHub Actions model. The dispatch pipelines (covered in this doc) trigger agent jobs; the agent execution details are implementation-specific.

## References

- [ADR 0028: GitLab Support Architecture](../ADRs/0028-gitlab-support.md) (superseded by [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md))
- [ADR 0043: GitLab Support via Webhook Bridge](../ADRs/0043-gitlab-support-via-webhook-bridge.md)
- [ADR 0005: Forge abstraction layer](../ADRs/0005-forge-abstraction-layer.md)
- [ADR 0009: pull_request_target security model](../ADRs/0009-pull-request-target-in-shim-workflows.md)
- GitLab CI/CD documentation: https://docs.gitlab.com/ee/ci/
- GitLab Project Access Tokens: https://docs.gitlab.com/ee/user/project/settings/project_access_tokens.html
- GitLab Pipeline Triggers: https://docs.gitlab.com/ee/ci/triggers/
