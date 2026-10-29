# Semgrep configuration

`security-audit.yml` is the checked-in snapshot of the Semgrep Registry's
`p/security-audit` ruleset, downloaded from
`https://semgrep.dev/c/p/security-audit` on 2026-10-29. The upstream response
was 492,566 bytes with SHA-256
`83d7cde9157b3c79cc5cfaea26e7bd2ba9c740f2ba07635ad475b2585556c32d`.

The snapshot keeps local hooks and CI independent of registry availability or
unreviewed upstream rule changes. To update it, download that URL over HTTPS,
review the full rule diff and new findings, then update this provenance record
and the accepted baseline in the same change.

`.semgrepignore` explicitly excludes Go test files, `test/` and `tests/`
directories, the vendored rules directory, and synthetic behaviour fixtures
and captured results. The pre-commit hook receives only changed Go, Python,
JavaScript, and TypeScript source files and applies the same test and fixture
exclusions. CI uploads the SARIF report as a run artifact for 30 days.
