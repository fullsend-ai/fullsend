# Vouch System

- First-time external contributors must be vouched before their PRs are accepted. The `vouch-check` workflow auto-closes PRs from unvouched users.
- Org members and collaborators with write access bypass the vouch gate automatically.
- Maintainers vouch users by commenting `/vouch` on a Vouch Request discussion. The `vouch-command` workflow appends the username to `.github/VOUCHED.td` on the `vouched` branch.
- Agent bot identities (`fullsend-ai-*[bot]`, `renovate-fullsend[bot]`, `github-actions[bot]`) are skipped automatically because they have `user.type: 'Bot'`.
- The `vouched` branch is protected — only the `vouch-command` workflow (via `GITHUB_TOKEN`) can push to it. Do not push to, rebase, or target PRs at the `vouched` branch.
- The vouch gate is separate from the e2e authorization gate. Vouch determines whether a PR stays open; e2e authorization determines whether tests run.
- PRs from unvouched external contributors are automatically closed with a comment linking to the vouch process.
- PRs should follow the PR template structure: Summary, Related Issue, Changes, Testing, Checklist.
