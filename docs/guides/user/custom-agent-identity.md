# Custom Agent Identity

When an agent acts on GitHub — commits, comments, reviews, status checks — it
does so as a **GitHub App**. Which App, and what that App is allowed to do, is
decided by one field in your harness: **`role`**.

```yaml
role: triage        # ← decides identity AND permissions at the mint
slug: my-org-triage # ← install-time hint only; the mint never reads it
```

The mint service maps `role` to a GitHub App (its ID + private key) and to a
permission ceiling, then issues a token scoped to your repo. `slug` only helps
`fullsend github setup` find or name the App at install time — it is **not**
what authenticates your agent, and changing it does not change identity or
permissions. If you set a `role` the mint doesn't serve, you get a `403`, not a
new identity.

So the real question is: **whose mint issues your token?**

## Two paths, one decision

| | Default (hosted) mint | Your own mint (standalone) |
|---|---|---|
| **URL** | `https://mint.fullsend.sh` (default) | your `FULLSEND_MINT_URL` |
| **Roles you can use** | a **fixed** built-in set — `triage`, `coder`, `review`, `retro`, `prioritize`, `fullsend` | those built-ins **plus** any custom role you define |
| **Identity** | the shared fullsend App for that role (e.g. `fullsend-ai-triage[bot]`) | **your** GitHub App — your name, your avatar |
| **Custom permissions** | no — each role's ceiling is fixed | yes — `security_events:write`, `deployments:write`, anything the GitHub API allows |
| **Who sets it up** | nobody — it just works | you (create App → install → register the role) |

**The default mint cannot mint a new identity for you.** It serves a fixed set
of Apps that fullsend operates (see [why](../infrastructure/standalone-mint.md)).
Picking a `role:` it doesn't serve — or a custom `slug:` — will not create one;
it produces the `403` above.

## Most customization needs no custom identity

Before reaching for your own mint, check whether you actually need a new
identity. You usually don't:

- **Changing behavior** — model, timeout, skills, prompt, env — keeps a
  built-in `role:`. Use [`base:` composition](customizing-agents.md#configuration-with-base-composition);
  the agent still runs as that role's App.
- **A brand-new agent** (its own trigger, scripts, schema) can still run on the
  hosted mint — set its `role:` to the built-in role whose permissions match
  what it does. A code-writing agent uses `role: coder`; a triage-like agent
  uses `role: triage`. The agent's own name lives in `name:` in its `.md`, not
  in `role:`.

> **Rule of thumb.** Pick the built-in role whose permission ceiling is the
> closest fit for what your agent needs to do. You only need your own mint when
> **no** built-in role fits — because you need a distinct identity, or
> permissions no built-in role has.

## When you do need your own mint

Choose a standalone mint when one of these is true:

- **Custom permissions** — your agent needs GitHub App permissions beyond any
  built-in role (for example, write access to packages, deployments, or
  security events).
- **Custom identity / branding** — you want actions to appear under your own
  GitHub App name and avatar, not the shared fullsend App.
- **Compliance** — your organization requires GitHub App credentials to stay
  inside your own infrastructure.

Set it up, then point your repo at it:

1. **Define the role and App on your mint** — follow the
   [Standalone mint guide](../infrastructure/standalone-mint.md): create the
   GitHub App, install it, store its PEM, and register the role's permissions
   (`CUSTOM_ROLE_PERMISSIONS`, or `fullsend mint add-role`).

2. **Reference the role in your harness:**
   ```yaml
   role: my-role          # a role YOUR mint serves
   slug: my-org-my-role   # install-time App discovery; still not read by the mint
   ```

3. **Point your repo at your mint** — set `FULLSEND_MINT_URL` to your mint's
   URL. Without this, tokens are requested from the hosted mint, which does not
   serve `my-role`.

> **Tip: adopt gradually.** With `FALLBACK_MINT_URL`, your standalone mint
> serves your custom roles locally and proxies the built-in ones to the hosted
> mint — so you can start with a single custom role without disrupting existing
> agents. See [Standalone mint — Fallback proxy behavior](../infrastructure/standalone-mint.md#fallback-proxy-behavior).

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Configuring Agent Behavior](customizing-agents.md) — change model, skills, and env while keeping a built-in role
- [Standalone Mint](../infrastructure/standalone-mint.md) — full standalone mint setup guide
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML reference including `role` and `slug`
