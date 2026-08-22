# Custom Agent Identity

By default, agents authenticate using shared fullsend GitHub Apps via the `slug` field in the harness. If you need your own GitHub App — for custom permissions, compliance, or branding — you can run a **standalone mint**. Follow the [Standalone mint guide](../infrastructure/standalone-mint.md) to set one up.

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
- [Harness Field Reference](harness-reference.md) — complete harness YAML reference including `role` and `slug`
