# SPEC: GitHub Apps and `.fullsend` credentials (admin install v1)

**Version:** 1
**Scope:** Behavior implemented by `fullsend admin install` for GitHub-hosted orgs: GitHub App identity per agent **role**, manifest-based creation, org installation checks, and storage of app credentials in the org config repository.

**Normative references (code):** `internal/appsetup`, `internal/layers/secrets.go`, `internal/cli/admin.go`, `internal/config`, `internal/forge/github/types.go`, `internal/forge/forge.go` (`ConfigRepoName`).

**Related decisions:** This spec is the detailed surface for credentials and app lifecycle; it assumes the org config repository name and layout are decided elsewhere ([ADR 0012](../../../../ADRs/0012-admin-install-fullsend-repo-files-v1.md) when accepted) and that repository enrollment is covered by [ADR 0013](../../../../ADRs/0013-admin-install-enrollment-v1.md) when accepted.

---

## 1. Configuration repository

- The org-level fullsend configuration repository **must** be named **`.fullsend`** (same as `forge.ConfigRepoName`).

## 2. Agent roles

- Recognized roles are exactly: `fullsend`, `triage`, `coder`, `review` (`config.ValidRoles` / `DefaultAgentRoles`).
- The install command’s `--agents` flag supplies a comma-separated subset; processing order is the order given in that list.
- Role strings are matched case-sensitively in configuration; **secret and variable names** derive from `strings.ToUpper(role)` (see §5).

## 3. GitHub App naming and slug convention

- App **display names** are defined by `AgentAppConfig(org, role)` (e.g. `fullsend-<org>` for role `fullsend`, `fullsend-<org>-<role>` for the standard roles).
- The **expected app slug** used to find an existing org installation is:
  - `fullsend-<org>` when `role == "fullsend"`;
  - `fullsend-<org>-<role>` otherwise.
- If `config.yaml` in `.fullsend` already lists agents, **role → slug** overrides from that file may be used when resolving existing installations (`knownSlugs`), so the slug on GitHub need not match the expected slug if the config maps the role to the actual slug.

## 4. Install flow outcomes (per role)

For each role, the setup runner (`appsetup.Setup.Run`):

1. **List org installations** and locate one whose app slug matches the override for that role, if any, else the expected slug (§3).
2. **If a matching installation exists:**
   - If a **secret checker** is configured: look for `FULLSEND_<ROLE>_APP_PRIVATE_KEY` in `.fullsend` (§5).
     - If the secret **exists:** prompt to reuse. If the user accepts reuse, the flow succeeds with **no new PEM** (empty PEM in memory); downstream layers **must not** write a new secret in that case.
     - If the secret **does not exist:** fail with an error that instructs deleting the GitHub App and re-running (the private key cannot be recovered from the org alone).
     - If the user **declines** reuse: fail; the user must delete the app before recreating.
   - If no secret checker is configured: treat as reuse with the metadata available from the installation (no PEM).
3. **If no matching installation exists:** run the **manifest flow** (local callback server, browser, exchange of manifest code for credentials), then **ensure** the app is installed on the org:
   - If not yet installed, print the GitHub “install app” URL (`https://github.com/apps/<slug>/installations/new`), optionally open the browser, wait for the user to confirm (Enter), then re-list installations and **fail** if the app still does not appear for the org.

**Out of scope for this spec:** Persisting OAuth **client_secret** or **webhook_secret** from the manifest response into `.fullsend`. Current code stores only the **PEM** and **numeric App ID** in the repo (§5).

## 5. Repository secrets and variables (credential surface v1)

All of the following are **repository-level** Actions secrets and variables on **`.fullsend`** in the target organization.

| Kind     | Name pattern                          | Value |
|----------|----------------------------------------|-------|
| Secret   | `FULLSEND_<ROLE>_APP_PRIVATE_KEY`      | PEM text of the GitHub App private key |
| Variable | `FULLSEND_<ROLE>_APP_ID`               | Decimal string of the GitHub App numeric ID |

- `<ROLE>` is the agent role in **ASCII uppercase** (e.g. `FULLSEND_TRIAGE_APP_PRIVATE_KEY`).
- For each role processed in install, if PEM is non-empty, the implementation **must** create/update the secret and variable as above; if PEM is empty (reuse path), the implementation **must** skip writing that role’s secret/variable.

## 6. Analyze / health semantics for the secrets layer

- For each expected role, the layer considers both the secret and the variable for that role.
- **Installed:** all expected pairs present.
- **Not installed:** none present.
- **Degraded:** some present and some missing (report which would be created or repaired).

## 7. Uninstall

- Removing secrets is **not** a separate automated step: deleting the `.fullsend` repository removes repository secrets and variables with it.
- GitHub Apps themselves may still exist; operators are pointed to manual deletion URLs for each configured app slug (`admin uninstall` behavior).
