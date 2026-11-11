# Vouch System

- First-time external contributors must be vouched before their PRs are accepted. The `vouch-check` workflow auto-closes PRs from unvouched users.
- `vouch-check` runs on a schedule (every 10 minutes) and on `workflow_dispatch`, not on `pull_request_target` ([#9245](https://github.com/fullsend-ai/fullsend/issues/9245)). It runs [`scripts/vouch-sweep.sh`](../../scripts/vouch-sweep.sh), which reads PR metadata through the API and never checks out PR code. Only PRs opened or reopened in the last 60 minutes are checked, so overlapping runs cover a missed or late cron run and older open PRs are left alone. An unvouched PR can therefore stay open for up to about 10 minutes before it is closed.
- To preview a sweep without closing or commenting, run `scripts/vouch-sweep.sh --dry-run fullsend-ai/fullsend` with a `GH_TOKEN` that can read the repository.
- Org members and collaborators with write access bypass the vouch gate automatically.
- Maintainers vouch users by commenting `/vouch` on a Vouch Request discussion. The `vouch-command` workflow appends the username to `.github/VOUCHED.td` on the `vouched` branch.
- Agent bot identities (`fullsend-ai-*[bot]`, `renovate-fullsend[bot]`, `github-actions[bot]`) are skipped automatically because they have `user.type: 'Bot'`.
- The `vouched` branch is protected — only the `vouch-command` workflow (via `GITHUB_TOKEN`) can push to it. Do not push to, rebase, or target PRs at the `vouched` branch.
- The vouch gate is separate from the e2e authorization gate. Vouch determines whether a PR stays open; e2e authorization determines whether tests run.
- PRs from unvouched external contributors are automatically closed with a comment linking to the vouch process. The PR is closed before the comment is posted, and the comment (marker `<!-- vouch-check -->`) is posted once per PR, even when overlapping runs or a reopen close it again.
- An API error never closes a PR: a failed `VOUCHED.td` read aborts the sweep, and a failed permission lookup skips that PR until the next run.
- PRs should follow the PR template structure: Summary, Related Issue, Changes, Testing, Checklist.
