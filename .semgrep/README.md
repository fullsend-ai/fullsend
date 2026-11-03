# Semgrep configuration

`scripts/semgrep-scan` downloads the Semgrep Registry's `p/security-audit`
configuration from the URL in `scripts/semgrep-ruleset-url` and verifies it
against the SHA-256 in `scripts/semgrep-ruleset.sha256`. A cached or downloaded
ruleset that does not match the pin fails in every run. The dedicated Semgrep
job sets `SEMGREP_REQUIRED=1`, so it fails closed if the ruleset or the locked
Semgrep environment cannot be prepared. Other contexts, including agent
pre-commit gates, warn and skip when `uv` is missing or a ruleset, cache, or
environment dependency cannot be prepared. A completed scan still fails on
findings.
The pre-commit hook is verbose, so its warning is visible when a scan is skipped
even though the hook passes. Local runs reuse a verified cache keyed by the
SHA-256. Use `SKIP=semgrep` with pre-commit or `make test` to skip the hook
explicitly while offline.

The scheduled `Semgrep Ruleset Drift` workflow compares the live response with
the pin and opens or updates an issue when it changes. To update the ruleset,
review the new configuration and scan results, update the pin, and fix new
findings or add reviewed `nosemgrep: <rule-id>` comments with their rationale.
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
