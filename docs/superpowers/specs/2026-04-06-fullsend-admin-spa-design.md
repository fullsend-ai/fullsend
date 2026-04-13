# Design: Fullsend admin installation SPA (static, GitHub App–centric)

Date: 2026-04-06
Status: Draft (brainstorm consolidated)

## Context

Today, fullsend organization installation and analysis are delivered through the Go CLI (`fullsend admin install|uninstall|analyze <org>`), which uses a **layer stack** (config repo, workflows, secrets, enrollment) and **GitHub App** setup for agent roles (`internal/cli/admin.go`, `internal/layers/*`, `internal/appsetup/*`). Org-level configuration is conventionally stored in an org-owned **`.fullsend`** repository (see ADR 0003 in `docs/ADRs/0003-org-config-repo-convention.md`).

This document specifies a **single-page application** that provides a **guided, friendly** path through the **same responsibilities** as the CLI, without a Fullsend-hosted backend.

## Goals

- **Static SPA only:** no Fullsend server or generic OAuth backend; all GitHub API calls from the browser with tokens obtained via GitHub’s documented flows.
- **Sign-in:** use a **GitHub App** for the admin UI (not a separate OAuth App for login). **Additional GitHub Apps** for fullsend **agent roles** are created or wired during onboarding, consistent with today’s CLI.
- **Org dashboard:** list **all** org memberships (alphabetical, search-as-you-type); per org, async **checking** then show **permission sufficiency**, **onboarding status** (not / partial / healthy, aligned with CLI **analyze** / layer semantics), and appropriate actions or disabled state with **short reasons**.
- **Org drill-down:** show **union** of API-visible repos and `config.yaml` repo names—surface **repos not in config** and **orphaned config entries** (repo missing) to support cleanup.
- **Full CLI parity over time:** install, repair, uninstall, and the insights of **analyze**; **analyze** and **dry-run** are **implicit** via continuous status and a **final review** step before mutating changes.
- **Hosting:** **official** deployment + **self-hosted** static deploy + **per-PR previews** on **unique hostnames**.
- **Implementation approach:** **TypeScript** in the SPA **reimplements** layer behavior and GitHub integration (Approach 1); **automated** CLI↔SPA parity tests are **not** required in the initial phase (see Parity section).

## Non-goals (initial phase)

- **Automated** golden/fixture parity tests or CI enforcement between CLI and SPA (future milestone).
- Defining exact **GitHub App** permission scopes in this document—those must be derived from code and recorded in the **permission matrix** appendix as implementation proceeds.

## Architectural approach

**Chosen: Approach 1 — TypeScript implementation in the SPA**

Mirror the existing **layer model** and the **GitHub REST/GraphQL** usage of `internal/forge/github` in TypeScript. **Mitigate drift** through **manual** and **review-time** discipline until automated checks exist (see Parity).

**Deferred:** compiling Go to WASM for shared logic (revisit only if dual maintenance becomes unacceptable).

**UI stack (tentative):** Svelte with TypeScript—subject to team preference; the design is otherwise stack-agnostic.

## Section 1 — Product shape and constraints

- The SPA is the **primary guided experience** for admins who prefer a browser over the CLI.
- **Hard constraints:** static hosting only; **short-lived** tokens; **`localStorage`** on the SPA origin for session persistence; explicit handling of **token expiry** and **long-lived tabs** (refresh, re-auth, clear UX on **401**).
- **Delivery:** scope is **full** parity with CLI admin capabilities, but **shipping may be split** across multiple tasks and PRs.

## Section 2 — Authentication, tokens, production, self-hosted, previews

### Production admin SPA

- **Production GitHub App** registered for the **official admin origin** (homepage + fixed callback path, e.g. `/oauth/callback`).
- User completes **user authorization** for that app; the SPA exchanges the `code` for tokens per **current GitHub documentation** for **GitHub App user access tokens**.
- **Verification gate before implementation:** Task 1 in [`2026-04-12-fullsend-admin-spa.md`](../plans/2026-04-12-fullsend-admin-spa.md) records hands-on outcomes; GitHub’s docs already require **`client_secret`** for the web-application exchange (see **Open items** and **Appendix A**). If maintainer Part B/C notes differ, update those sections after the experiment.
- Tokens: **short TTL**; store in **`localStorage`**; **sign-out** clears storage; handle **refresh** if GitHub provides it, otherwise **re-auth**.

### Self-hosted

- Operators use **their own** GitHub App configuration with **homepage and callback** matching **their** admin **origin**.
- Redirect allowlist stays **small and stable** for that origin.
- Documentation: checklist for app settings, required GitHub permissions (see matrix), and preview behavior if they use ephemeral preview URLs.

### Preview deployments (unique per-PR hostnames)

- **Separate “preview” GitHub App** from production: lower trust, easy to disable; **do not** register many preview URLs on the **production** app.
- **No CI-driven editing** of redirect URL allowlists (too fragile).
- **Full-page flow (no popups):** user on `https://<preview-host>/` navigates to **production** to start OAuth for the **preview** app; **PKCE** and `state` (including validated **`return_to`**) live on the **production** origin (e.g. **`sessionStorage`** during the round trip). GitHub redirects to **production** callback only; production exchanges `code` for tokens, then **redirects** to the preview URL with credentials in the **URL fragment (hash)** (not query string). Preview reads hash once, persists to **`localStorage`**, strips hash via **`history.replaceState`**.
- **Risks:** fragment in **history**, **XSS** on preview, **shoulder surfing**; mitigate with **short-lived** tokens, **minimal** preview scopes, **clear “preview only”** labeling, and **tight CSP** where hosts allow.
- **Open redirect:** `return_to` must be **allowlisted** or **cryptographically bound** in `state`.
- **Dedicated production routes** for preview OAuth (e.g. `/oauth/preview-start`, `/oauth/preview-callback`) are **recommended** vs overloading the production callback.

### Security (cross-cutting)

- **`localStorage` + XSS** is the main browser-side risk; avoid logging tokens; keep dependencies pinned.

## Section 3 — Org and repo dashboard, permission checks, status model

### Org list

- **All** org memberships, **alphabetical** sort, **search-as-you-type**.
- Per row: **checking** → resolved **permission** outcome + **onboarding status** (from TS layer engine).
- **Actions:** Start onboarding / Continue / Open org; **disabled** with **specific reason** when permissions are insufficient.
- **Caching** within the session to limit API churn; **manual refresh** available.

### Org detail

- Rollup status, links to **repair**, **uninstall**, **repo onboarding**.

### Repo list

- **Union** of org API repos and `config.yaml` names.
- Rows: normal repos with **not / partial / full** enrollment-style status; **not in config**; **orphan** (in config, repo gone).

### Status model

- Align with **`LayerReport`** semantics (`not installed`, `degraded`, `installed`) and CLI **analyze** wording where possible.
- **No separate “analyze mode”:** dashboard always reflects **current** state; wizards end with **review** of pending mutations before **Confirm**.

### Errors

- **Rate limits:** backoff and retry UX.
- **Token expiry:** re-auth without silently losing wizard progress where **`sessionStorage`** on the same origin can help.

## Section 4 — Wizards (onboard, repair, uninstall), agent apps, secrets

### General wizard rules

- Linear steps, **Back / Next**, **final review** before **mutating** fullsend installation (config repo content, secrets API writes, workflow files, enrollment).
- **Implicit analyze:** re-check relevant layers when entering a step or on **Refresh**.

### Org onboarding

Steps follow CLI **install** ordering: **`.fullsend` / config** → **agent GitHub Apps** (per role) → **secrets** (LLM and app keys via GitHub APIs) → **workflows** in config repo → **enrollment**; then **final review → Apply** in **stack install** order with **idempotent** operations and **per-step retry**.

### Exception — agent GitHub Apps

- App creation / user confirmation on **github.com** may **interrupt** the wizard; this is **expected** and **not** subject to “review before any GitHub interaction.”
- **Staging:** after GitHub steps, the SPA may persist **intermediate credentials** (e.g. app id, slug, PEM) in **`localStorage`** on the **current origin** to **resume** the wizard.
- **Policy:** clear staging on **success**, **cancel**, **sign-out**, or documented **abandon** behavior; store **only** necessary fields; never put secrets in URLs or logs.
- **Final review** still applies to **bulk apply** to the org’s fullsend installation (secrets to GitHub, config/workflow/enrollment changes).

### Repair / partial

- Enter at the **first failing layer** in stack order.

### Repo-scoped onboarding

- Enrollment-focused wizard plus **`config.yaml`** updates as needed; same **review → confirm** pattern.

### Uninstall

- Match CLI **uninstall** behavior and ordering; strong confirmation; surface **manual GitHub App deletion** instructions where automation cannot remove apps.

## Section 5 — Permission matrix, parity guidance, phased delivery

### Permission matrix

- Maintain a **table** in this spec (appendix) or linked doc: each **SPA capability** → **GitHub API** operations → **required permissions / roles** (derived from `internal/layers`, `internal/appsetup`, `internal/forge/github`). Update when either CLI or SPA behavior changes.

### Parity with CLI (initial phase)

- **Do not** add or modify automated tests solely for CLI↔SPA parity in this phase.
- **Contributor guidance:** use CLI **analyze/install/uninstall** and Go **layers** as **source of truth**; cross-reference in PR descriptions when touching one surface; align user-visible status language with CLI; **manually** verify critical scenarios when both surfaces exist.
- **Future:** automated parity (fixtures, goldens, CI)—explicitly **out of scope** for initial delivery.

### Phased delivery (priorities)

**Early**

1. **Minimal SPA + CI/CD** that deploys **per-PR previews** on **unique hostnames**, with **preview GitHub App** and **production hash handoff** so PRs are **browser-reviewable** immediately.
2. **Limited self-hosted for local dev:** documented local static serve / dev server, **localhost** callback, **dev GitHub App** checklist—before full operator-facing self-hosted docs.

**Then**

3. Production sign-in and **org dashboard** (checking, permissions, status).
4. Read-only **org/repo** views and TS **status engine**.
5. **Wizards:** onboard → repair → repo-scoped → uninstall.
6. **Full** self-hosted operator documentation, hardening (CSP, rate limits, a11y).

## Appendix A — Permission matrix

During implementation, add a **row per GitHub capability** the SPA uses (REST paths or GraphQL operations), with **documented permission or role** expectations and notes for **enterprise** edge cases. Derive rows from `internal/forge/github` and from each layer’s `Install` / `Analyze` / `Uninstall` path. The **first PR** that introduces a new API surface **adds** the corresponding row(s) here.

| Capability | HTTP | Notes |
|------------|------|-------|
| User access token exchange (GitHub App web application flow) | `POST https://github.com/login/oauth/access_token` | Form body / query parameters per GitHub: `client_id`, **`client_secret`** (required in official docs), `code`, optional `redirect_uri` (must match a registered callback URL), optional PKCE `code_verifier` when `code_challenge` was used at authorize time. JSON response includes `access_token` (user tokens use the `ghu_` prefix), `token_type` (`bearer`), and optionally `expires_in` / `refresh_token` (`ghr_`) when expiring tokens are enabled. See [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app#using-the-web-application-flow-to-generate-a-user-access-token). |

## Appendix B — Related code references

- `internal/cli/admin.go` — install, uninstall, analyze entrypoints
- `internal/layers/*` — `ConfigRepoLayer`, `WorkflowsLayer`, `SecretsLayer`, `EnrollmentLayer`
- `internal/appsetup/*` — GitHub App setup per agent role

## Open items

- **GitHub App user access token exchange vs pure static SPA (Task 1, 2026-04-12):**
  - **Documented contract:** GitHub’s **web application flow** for GitHub Apps lists **`client_secret`** as **required** on `POST https://github.com/login/oauth/access_token` together with `client_id` and the authorization `code` (optional `redirect_uri`, optional PKCE `code_verifier`). A successful JSON body includes `access_token` (user tokens use the `ghu_` prefix) and `token_type` (`bearer`). Source: [Using the web application flow to generate a user access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app#using-the-web-application-flow-to-generate-a-user-access-token).
  - **Part B (browser `fetch` without `client_secret`, real `code`):** In an embedded / restricted browser context, `fetch` to `https://github.com/login/oauth/access_token` was blocked by the **page’s Content-Security-Policy** (`connect-src` / `default-src chrome:`), so the request never reached a stage where GitHub’s **CORS** policy could be observed. The [`oauth-localhost-part-b/serve.py`](../experiments/oauth-localhost-part-b/serve.py) helper uses a **same-origin** callback → local `POST` → server-side GitHub exchange only (so the one-time `code` is not spent on a cross-origin browser attempt first). For a **manual** CORS check, use DevTools on a normal **`http://localhost:<PORT>`** tab and the plan’s `fetch` snippet—not `file://` or locked-down embedded previews. Use **`localhost`** consistently (not `127.0.0.1`) so the tab origin matches the GitHub callback URL. Task 2’s Vite dev server is also fine once it exists.
  - **Part C (terminal `curl` with `client_secret`, secret never in repo or browser bundle):** Optional; same outcome as a server-side `POST` exchange. **2026-04-12:** full exchange validated via [`oauth-localhost-part-b/serve.py`](../experiments/oauth-localhost-part-b/README.md) with `CLIENT_SECRET` in the process environment only (proxy → GitHub), yielding a usable `ghu_` token when the authorization `code` was valid. `refresh_token` / `expires_in` appear when expiring user tokens are enabled on the app.
  - **Smallest production-shaped adjustment:** keep `client_secret` **only** on a confidential path (for example a **Cloudflare Worker** handler with `GITHUB_APP_CLIENT_SECRET` in Wrangler secrets) that performs the `POST` exchange and returns tokens to the SPA over the **same admin origin**; do **not** embed the secret in static assets. **Device flow** is for headless clients and is **not** a substitute for the browser admin experience.
- **Frontend stack (decided 2026-04-12):** **Svelte 5** + **TypeScript** + **Vite** for the admin SPA (see implementation plan).
- Expand **Appendix A** from code during first implementation PRs (beyond the OAuth exchange row above).
