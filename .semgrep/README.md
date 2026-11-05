# Semgrep configuration

`scripts/semgrep-scan` downloads a commit-pinned archive of
[`semgrep/semgrep-rules`](https://github.com/semgrep/semgrep-rules). The full
commit is in `scripts/semgrep-ruleset-commit`; the downloaded archive is
verified against `scripts/semgrep-ruleset.sha256` before it is extracted. The
source-file allowlist in `scripts/semgrep-ruleset-files.txt` selects the
Community Edition rules represented in the curated `p/security-audit` config
snapshot whose SHA-256 is recorded in that file. The manifest contains 216
files with all 225 rules from the snapshot. Rule contents come from the
commit-pinned upstream archive; the config snapshot defines pack membership.
The scanner verifies that every allowlisted file exists under a `security/`
directory and rejects upstream test fixtures.

The dedicated Semgrep job sets `SEMGREP_REQUIRED=1`, so it fails closed if the
pinned source or locked Semgrep environment cannot be prepared. Other contexts,
including agent pre-commit gates, warn and skip when `uv` is missing or the
source, cache, or environment dependency cannot be prepared. A completed scan
still fails on findings.
The pre-commit hook is verbose. If a local prerequisite is unavailable, it
prints `SKIPPED` and says that no files were analyzed, even though the hook
passes. Local runs reuse a verified cache keyed by the SHA-256. Use
`SKIP=semgrep` with pre-commit or `make test` to skip the hook explicitly while
offline.

The production `math/rand` uses flagged by the security scan were replaced
with shared crypto-backed helpers, including non-security jitter and shuffle
call sites. This keeps production randomness consistent and avoids suppressing
the same rule at those call sites; deterministic test-only randomness remains
outside the scan.

The scheduled `Semgrep Ruleset Drift` workflow compares allowlisted rule-file
contents at the pinned commit and upstream `develop`. It reports only when
those selected rules differ; unrelated commits do not create drift. The
notification does not change the rules used by scans. To update them, review
the upstream changes under `security/`, update the commit and archive hash, and
review the resulting findings. Add `nosemgrep: <rule-id>` comments only with a
rationale.
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
