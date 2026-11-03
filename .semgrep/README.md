# Semgrep configuration

`scripts/semgrep-scan` downloads a commit-pinned archive of
[`semgrep/semgrep-rules`](https://github.com/semgrep/semgrep-rules). The full
commit is in `scripts/semgrep-ruleset-commit`; the downloaded archive is
verified against `scripts/semgrep-ruleset.sha256` before it is extracted. The
source-file allowlist in `scripts/semgrep-ruleset-files.txt` narrows the
scan to Community Edition files represented in the curated `p/security-audit`
config snapshot whose SHA-256 is recorded in that file. The manifest contains
111 files with 117 rules. Rule contents still come from the commit-pinned
upstream archive; the config snapshot is used only to define pack membership.
Some Registry rules are not available in the public rules repository, so this
remains a documented subset of the pack rather than a byte-for-byte copy. The
scanner verifies that every allowlisted file exists under
`*/security/audit/` and rejects upstream test fixtures.

The dedicated Semgrep job sets `SEMGREP_REQUIRED=1`, so it fails closed if the
pinned source or locked Semgrep environment cannot be prepared. Other contexts,
including agent pre-commit gates, warn and skip when `uv` is missing or the
source, cache, or environment dependency cannot be prepared. A completed scan
still fails on findings.
The pre-commit hook is verbose, so its warning is visible when a scan is skipped
even though the hook passes. Local runs reuse a verified cache keyed by the
SHA-256. Use `SKIP=semgrep` with pre-commit or `make test` to skip the hook
explicitly while offline.

The scheduled `Semgrep Ruleset Drift` workflow reports when the upstream
`develop` branch has advanced beyond the pinned commit. That notification does
not change the rules used by scans. To update them, review the upstream changes
under `security/audit/`, update the commit and archive hash, and review the
resulting findings. Add `nosemgrep: <rule-id>` comments only with a rationale.
There is no separate baseline file. The rules are fetched at runtime and are
not stored in this repository.

The CI job reports failures independently. A failing check blocks merges only
when the branch ruleset requires it; the main ruleset will require Semgrep after
this workflow merges.

The pre-commit hook scans changed files of all types, except paths matched by
its `exclude:` expression in `.pre-commit-config.yaml`. Semgrep's directory
scan in CI honors `.semgrepignore`; explicit paths passed to Semgrep bypass
that file, so keep the pre-commit expression aligned with `.semgrepignore`.
Both exclude `*_test.*` and `test_*.py` files, `test/` and `tests/` directories,
`.semgrep/`, and synthetic behaviour fixtures and captured results. CI uploads
the SARIF report as a run artifact for 30 days.
