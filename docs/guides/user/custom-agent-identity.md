# Custom Agent Identity

By default, agents authenticate using shared fullsend GitHub Apps via the `slug` field in the harness. Each agent's `role` maps to a GitHub App installation managed by the hosted mint service.

A standalone mint makes sense when:

- **Custom permissions** -- your agent needs GitHub App permissions beyond what the shared Apps grant (e.g., write access to packages or deployments).
- **Compliance** -- your organization requires that GitHub App credentials stay within your own infrastructure.
- **Branding** -- you want agent actions (commits, comments, status checks) to appear under your own GitHub App name and avatar.

If none of these apply, the shared Apps work without extra setup -- set `slug` in your harness and go. To run your own, follow the [Standalone mint guide](../infrastructure/standalone-mint.md).

Once your standalone mint is running, configure your agent to use it:

1. **Reference your role in the harness:**
   ```yaml
   role: my-role
   slug: my-org-my-role
   ```

2. **Set `FULLSEND_MINT_URL`** in your repo to point to your standalone mint.

When configured with `FALLBACK_MINT_URL`, the standalone mint serves custom roles locally while proxying unhandled roles to the hosted mint (see [Standalone mint — Fallback proxy behavior](../infrastructure/standalone-mint.md#fallback-proxy-behavior)).

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Standalone Mint](../infrastructure/standalone-mint.md) — full standalone mint setup guide
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML reference including `role` and `slug`
