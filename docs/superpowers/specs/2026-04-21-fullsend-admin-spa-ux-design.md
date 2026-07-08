# UX design: Fullsend admin SPA (screens, states, and errors)

Date: 2026-04-21
Status: Draft
Companion: [`2026-04-06-fullsend-admin-spa-design.md`](2026-04-06-fullsend-admin-spa-design.md)

## Purpose and scope

This document specifies **what users see and how they interact** with the Fullsend **admin SPA** hosted under **`/admin`**: high-level flows, screen layouts, per-control behavior, **row-level states**, and **where errors appear**. It is intentionally **UI/UX detailed** where the companion architecture spec stays high-level.

### In scope

- Routing gate, login, post-auth loading, **account/navigation bar**.
- **Organisation selection** (primary landing when authenticated).
- **Organisation dashboard** (Fullsend status summary + **repository list**).
- **Cross-cutting** loading patterns, **global** auth/rate-limit messaging, and **per-row** error/retry patterns.

### Out of scope (see companion spec)

- OAuth/Turnstile/Worker contracts, permission matrix rows, CSP, preview hash handoff — see companion **Section 2** and **Appendix A**.
- **Org-level** install/repair/uninstall **wizards** (multi-step flows): semantics and ordering remain in companion **Section 4**; this UX spec defines only **entry points** (buttons that will route into those flows when implemented) and **does not** specify wizard interiors.
- Automated test matrices beyond lightweight acceptance notes.

### Scope clarification (repositories)

There is **no separate repository onboarding or settings wizard** planned. **All repository-related outcomes** (including config/orphan edge cases from the companion spec) are represented **as row states and actions on the org dashboard’s repository list**. Multi-step work still occurs through **GitHub** (for example pull requests); the SPA surfaces **links** and **status**, not a dedicated repo wizard shell.

### GitHub terminology

User-visible labels for concepts that come from GitHub use **GitHub’s terms** (organisation, repository, pull request, login, display name). When additional SCMs are supported, equivalent terminology for that provider should be used instead.

---

## Document shape

This spec uses a **screen catalog**: one section per major screen, with **state tables** suitable for implementation and QA. **Global UX patterns** are defined once and referenced from each screen.

---

## User journeys (routes)

1. **Anonymous deep link:** user opens any `/admin` URL → **Login** → after successful auth and profile load → **original URL** is restored; if none was stored, land on **Organisation selection**.
2. **Authenticated browse:** user opens `/admin` → **Organisation selection** (unless a more specific route exists and is implemented).
3. **Org dashboard:** user chooses **Configure** from an org row (or equivalent navigation) → **Organisation dashboard** for that org.
4. **Sign out:** user clicks **Sign out** in the nav bar → session cleared per companion spec → return to **Login** (recommended default for consistency).

Deep-link and `history.replaceState` behavior for OAuth callbacks follows the companion spec (code/state handling on the SPA entry URL).

---

## Global UX patterns

### Post–OAuth return loading

After GitHub redirects back to the app, show a **large centered indeterminate spinner** until:

1. The **session token** is established (exchange / storage per companion spec), and
2. **User profile fields** required for the navigation bar are available: **avatar URL**, **login** (account name), and **display name** (if GitHub provides none, omit the second line in the nav block rather than showing an empty line).

If this bootstrap **fails**, use the **global banner** pattern (below) with **Retry** where safe, or **Re-authenticate** when the failure is auth-shaped (401 / invalid code). Do not leave a blank screen.

### Global banners (screen-level, below the nav bar)

| Condition | Presentation | Primary actions |
| --- | --- | --- |
| **Expired or invalid session (401)** | Persistent banner | **Re-authenticate** (restart sign-in; preserve intended route via companion `sessionStorage` guidance where applicable) |
| **GitHub rate limiting** | Non-blocking banner; copy may include retry timing if headers expose it | **Dismiss** (optional), automatic backoff on **Refresh** / row retries |
| **Worker / network / 5xx** for non-row-scoped fetches | Banner with short human-readable message | **Retry** |
| **Turnstile / Worker misconfiguration** (`missing_turnstile_keys`, etc.) | Banner | **Retry** only when meaningful; otherwise explain that the deployment is misconfigured |

**Implementation (401 from user-token GitHub REST in the browser):** Calls made with **`createUserOctokit`** dispatch **`GITHUB_USER_UNAUTHORIZED_EVENT`** (`fullsend:github-unauthorized`) from the request hook before rethrowing. **`fetchOrgs`** (and any other wrapper that surfaces `401`) should call **`notifyGitHubUserUnauthorized()`** from `web/admin/src/lib/auth/githubUnauthorized.ts` so the shell still updates if a future code path bypasses that hook. **`App.svelte`** listens and runs **`signOut({ suggestReauth: true })`**, which shows the banner above. **Do not** handle `401` with a screen-local banner that only offers **Retry** and hides the global **Re-authenticate** path.

Row-level fetches **do not** duplicate a paragraph of error text inline on the row.

### Per-row error pattern (org list and repo list)

When a **row-scoped** fetch or status check fails (network, 5xx, unexpected response), the row’s trailing control area shows **all** of the following (replacing the spinner and normal action cluster for that row until resolved):

1. **Red warning triangle** (distinct from the **yellow** triangle used for policy-style “cannot deploy” / permission outcomes).
2. The word **`Error`** in normal body text beside the icon.
3. A **circled “i”** control: **click** / **keyboard activate** opens a **popover** with **technical detail suitable for admins** (HTTP status, short server message, correlation id if available). **Dismiss** on outside click and **Escape**.
4. A **`Retry`** button: re-runs **only** that row’s loading pipeline.

There is **no** separate multiline inline error string on the row body; detail lives in the **popover**.

### Per-row “cannot deploy” pattern (org list only)

When Fullsend is **not** deployed and the user **cannot** deploy, use:

- **Yellow warning triangle** + text **`Cannot deploy`** + **circled “i”** (popover explains **why**: insufficient permissions, GitHub role hint, or engine-derived reason). **No** `Retry` unless the reason is **transient** (implementation may show **Retry** only for retriable classification; otherwise omit).

### Search and “showing 15” cap (organisation list)

- **Search-as-you-type** filters the **full** organisation set the user has access to, then the UI displays **at most 15** matching rows (sorted **alphabetically** by organisation name unless product later defines another stable sort).
- If the filtered set has **more than 15** matches, show **red** helper text beneath the search field: **`Showing up to 15 organisations`** (exact string).

### Search and “showing 15” cap (repository list)

On the **organisation dashboard** repository list (**Pane B**), apply the same pattern as the organisation picker:

- **Search-as-you-type** filters the **full** repository union (see **Pane B — row source**), then the UI displays **at most 15** matching rows (sorted **alphabetically** by repository name unless product later defines another stable sort).
- If the filtered set has **more than 15** matches, show **red** helper text beneath the search field: **`Showing up to 15 repositories`** (exact string).

### List interaction model

- **Primary navigation** from list rows is via **explicit buttons** (for example **Configure**, **Deploy Fullsend**, **Onboard**, **Repair**, **Remove**, **Retry**), not by clicking the row background. **Links** (PR numbers) remain directly clickable.

---

## Screen: Login

### Login — when shown

Any `/admin` route when there is **no** valid session.

### Login — layout

- Centered **primary** control: **`Sign in with GitHub`** — large button including the **GitHub logo** mark and label text.
- **Future:** additional SCM sign-in buttons may be added in the same cluster (secondary placement: below the primary button or in a horizontal group; exact layout left to implementation as long as GitHub remains visually primary).

### Login — behavior

- Clicking **Sign in with GitHub** starts the documented **full-page** OAuth flow (no pop-up window).
- After successful return and bootstrap (see **Post–OAuth return loading**), navigate to the **stored intended** `/admin` path or **Organisation selection**.

### Login — errors

- OAuth failures, exchange failures, or profile load failures: **global banner** (and/or full-page centered error state if the nav chrome cannot yet render — prefer **banner** once a minimal shell exists).

---

## Screen: Account / navigation bar

### Account bar — when shown

Top of **every** authenticated admin screen (hidden on **Login**).

### Account bar — user cluster

- **GitHub avatar** image.
- **Login** (GitHub account name) in **bold**, stacked above **display name** in **normal** weight — visually similar to GitHub’s own account menu header. If display name is **absent**, omit the second line entirely.

### Account bar — breadcrumb

- When an **organisation** is selected: show **organisation avatar** + **organisation name** to the right of the user cluster.
- When a **repository** context is selected (if the product exposes repo-scoped routes): append **repository name** after the organisation segment.
- Between each **area** segment (user block counts as one area; org; repo), show a **`/`** separator with **generous horizontal spacing** on both sides.

### Account bar — sign out

- **`Sign out`** button.

---

## Screen: Organisation selection

### Organisation selection — purpose

Pick an organisation to **deploy** Fullsend into, **configure** an existing deployment, or understand why neither is available.

### Organisation selection — header

- **Title:** `Select an organisation to deploy or configure Fullsend`
- **Search** field: **search-as-you-type**; placeholder **`Type to filter`** when empty.
- **Refresh** control: triggers a **manual** re-fetch / re-evaluation of visible rows (respecting session caching policy from the companion spec). When the list is **success-empty**, the implementation may also run **a few delayed background re-fetches** without an extra click (see [`2026-04-29-admin-spa-org-list-installations-design.md`](2026-04-29-admin-spa-org-list-installations-design.md)); **Refresh** remains the primary action after returning from GitHub.

### Organisation selection — list

- Up to **15** rows after filtering (see **Global UX patterns**).
- While the organisation set is still being discovered (paginated **`GET /user/installations`**, per [`2026-04-29-admin-spa-org-list-installations-design.md`](2026-04-29-admin-spa-org-list-installations-design.md)), **paint organisations as soon as they are known**, subject to the **progressive display rule** below.
- **Progressive display:** once **10** rows are on screen, **hold** further row updates until either discovery **finishes** or **at least five** additional filtered rows are available to show, then continue updating — always capped at **15** visible rows. (This reduces layout churn when many organisations appear quickly.)
- **In-list loading:** when discovery may still be in flight and at least one row is already visible, show an **indeterminate spinner** in the blank area **below** the list; reserve vertical space similar to **five** row heights so users see that loading continues.
- Each row, **left:** organisation **logo/avatar** + **organisation name**.
- Each row, **right** (mutually exclusive **trailing** cluster):

| State | Trailing UI |
| --- | --- |
| Loading org deployment / permission evaluation | **Indeterminate spinner** |
| Fullsend **partially or fully** deployed on the org | **Grey** button **`Configure`** → **Organisation dashboard** |
| Fullsend **not** deployed; user **may** deploy | **Blue** button **`Deploy Fullsend`** → org install entry (companion Section 4) when implemented |
| Fullsend **not** deployed; user **may not** deploy | **Yellow** triangle + **`Cannot deploy`** + **circled “i”** popover (reason); **no** `Retry` unless classified transient |
| Row fetch / evaluation **failed** | **Per-row error pattern** (red triangle, **`Error`**, **i**, **`Retry`**) |

### Organisation selection — empty states

- Loaded successfully, user has **zero** organisations in the **installations-backed** list: show the **primary installations empty hint** (no orgs with the admin app yet + pointer to install below), then the always-on install block. If no dedicated hint is available, fall back to neutral copy such as **No organisations found for this account.**
- Search active, **zero** matches: **No matching organisations.**

---

## Screen: Organisation dashboard

### Organisation dashboard — purpose

See **Fullsend status** for the selected org and **inspect every repository** relevant to Fullsend (including config-only and orphan entries) **without** a separate repo wizard.

### Organisation dashboard — navigation bar

- Shows **user** cluster and **organisation** cluster as defined above.

### Pane A — Fullsend status

Static label: **`Fullsend status:`** immediately followed by **one** of:

| State | Trailing UI |
| --- | --- |
| Checking | **Spinner** + text **`Checking`** |
| Fully deployed and up to date | **Green** circle + **`Deployed`** |
| Partially deployed or broken | **Yellow** triangle + **`Partially deployed / broken`** + **`Repair`** button (org repair entry — companion Section 4) |
| Installation outdated vs expected version | **Orange** circle + **`Outdated`** + **`Upgrade`** button |

Pane-level fetch failures use the **global banner** pattern; do not fragment org status across rows.

### Pane B — Repository list

#### Repository list — chrome

- **Search-as-you-type** (filters row **names** / identifiers shown in the list).
- **Refresh** (re-fetches repo union and row statuses).
- **Visible row cap** and **“showing 15”** helper for repositories: [Search and “showing 15” cap (repository list)](#search-and-showing-15-cap-repository-list).

#### Repository list — row source

The list is the **union** of:

1. Repositories **visible via GitHub** for this organisation (per companion **Section 3**), and
2. Repository **names present** in Fullsend **configuration** (for example `config.yaml`) even if the repository is **missing** from the GitHub API view.

#### Repository list — row layout

- **Left:** repository **name** (use GitHub’s naming; for orphans, show the **configured name**).
- **Right:** one **trailing state cluster** from the tables below.

#### Repo row states (complete set)

States are **mutually exclusive** on the trailing side except where noted (action buttons sit in the same trailing cluster).

| # | Semantic state | Trailing UI |
| --- | --- | --- |
| R0 | Loading status for this row | **Spinner** |
| R1 | **Not onboarded** — onboarding never started for this repo | **Blue** button **`Onboard`** |
| R2 | **Onboarding** — onboarding change is in flight via a **PR** | **Orange** circle + text **`Onboarding — check PR #nnn`** where **`PR #nnn`** links to the GitHub pull request |
| R3 | **Off-boarding** — removal is in flight via a **PR** | Same visual weight as **R2** (**orange** circle) + text **`Off-boarding — check PR #nnn`** with link to **`PR #nnn`** |
| R4 | **Onboarded** (healthy / complete enrollment) | **Green** circle + **`Onboarded`** + **red** button **`Remove`** |
| R5 | **Partially onboarded / broken** | **Yellow** triangle + **`Partially onboarded / broken`** + **`Repair`** + **red** button **`Remove`** |
| R6 | **Visible in GitHub**, **not** present in Fullsend config (companion “not in config”) | Neutral or informational icon + short label **`Not in Fullsend config`** + primary action **`Onboard`** (same action class as **R1**; copy may differ slightly but one consistent control) |
| R7 | **Orphan** — named in config **but repository missing** from GitHub (deleted/renamed/permissions) | **Yellow** or **red** warning (implementation picks one consistently) + **`Repository missing`** + **circled “i”** explaining orphan + **red** **`Remove from config`** (or **`Remove`**) that performs config cleanup **without** implying GitHub repo deletion |

#### Repository list — `Remove` (red) rules

- Show **`Remove`** for **R4** and **R5** (fully onboarded, or partially onboarded / broken).
- **Do not** show **`Remove`** for **R1** (not started) unless product explicitly adds “dismiss” later.
- **R2** / **R3**: **no** `Remove` beside the PR link (user tracks work in the PR); if cancellation is added later, it would be a **separate** explicit control with confirmation — **out of scope** here.

#### Repository list — row errors

- Any failure to load or evaluate a given row uses the **per-row error pattern** (red triangle, **`Error`**, **i**, **`Retry`**), not inline stack text.

---

## Accessibility and keyboard notes (minimum bar)

- **Sign in with GitHub**, **`Retry`**, **`Repair`**, **`Remove`**, **`Configure`**, **`Deploy Fullsend`**, **`Onboard`**: real **`<button>`** elements (or equivalent roles) with visible focus rings.
- **Circled “i”**: `button` or `button` + `aria-expanded` tied to popover; **Escape** closes popover.
- Icons (**Deployed**, **Onboarding**, **Error**) require **text** beside or immediately associated **accessible names** (do not rely on color alone).

---

## Acceptance notes (non-exhaustive)

- [ ] Unauthenticated `/admin/deep` → Login → returns to **deep** after success.
- [ ] Post-OAuth spinner covers until **nav bar** can render accurately.
- [ ] Org list: **>15** matches when filtered → **red** “showing 15 organisations” helper appears; **≤15** → helper hidden.
- [ ] Repo list (org dashboard): **>15** matches when filtered → **red** “showing 15 repositories” helper appears; **≤15** → helper hidden.
- [ ] Org row failure → **red** error row pattern with popover + **Retry**; **cannot deploy** remains **yellow** without the red pattern.
- [ ] Repo list includes **union** rows and shows **R6** and **R7** distinctly.
- [ ] **R4**/**R5** always show **red** **`Remove`** in addition to status text / **Repair** as specified.
- [ ] **R3** mirrors **R2** styling with **Off-boarding** copy and PR link.
- [ ] Repo row failure uses the **red** per-row error pattern (**`Error`**, **i**, **`Retry`**), not inline error paragraphs.

---

## Related documents

- [`2026-04-06-fullsend-admin-spa-design.md`](2026-04-06-fullsend-admin-spa-design.md) — architecture, auth, org/repo model, org-level wizards.
