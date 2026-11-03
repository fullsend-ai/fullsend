# Semgrep configuration

`scripts/semgrep-scan` downloads a commit-pinned archive of
[`semgrep/semgrep-rules`](https://github.com/semgrep/semgrep-rules). The full
commit is in `scripts/semgrep-ruleset-commit`; the downloaded archive is
verified against `scripts/semgrep-ruleset.sha256` before it is extracted. The
scan includes Community Edition rule files under `*/security/audit/` and
excludes upstream `*.test.yml` and `*.test.yaml` fixtures. This is a defined
security-audit scope from the public source tree, not a byte-for-byte copy of
Semgrep Registry's curated `p/security-audit` pack.

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
Both exclude Go test files, `test/` and `tests/` directories, `.semgrep/`, and
synthetic behaviour fixtures and captured results. CI uploads the SARIF report
as a run artifact for 30 days.
