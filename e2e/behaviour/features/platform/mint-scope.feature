@skip:gitlab
Feature: GitHub mint scope and workflow-host authorization

  Real GitHub Actions OIDC requests to the suite's mint (the preview
  mint on dev, the stage mint on stage) for the fullsend-test roles at
  the read and write levels. The mint must scope a same-repository
  token to the calling repository only, refuse wildcard and other
  same-org repository scopes, and accept the request only when the
  workflow that calls it is hosted by an approved workflow host.

  Each row runs one workflow in the leased repository, pushed to a
  temporary branch. It calls a reusable probe workflow hosted by a
  separate, temporary repository: the OIDC repository claim is the
  leased repository (the shim), the job_workflow_ref claim the host.
  The approved host is on the mint's workflow-host list; the unapproved
  host is not. "other repo" is the approved host itself, a repository
  of the same org covered by the role's App installation.

  The same run also makes a control request: same role and level, the
  leased repository only, through the approved host. It must mint, so
  an OIDC, transport, workflow, server or result failure fails the row
  instead of passing as a denial. A scope denial must be one of the
  mint's 403 repos-scope messages; a workflow-host denial is the 401
  the mint answers when the workflow host is not approved. The probe
  checks the granted permissions against the role's canonical set and
  reports only compact fields; no token is kept.

  Scenario Outline: <role> at <level> for <repos> through the <workflow host> workflow host
    Given the enrolled test repository
    And a "<forge>" mint probe through the <workflow host> workflow host
    When the mint probe requests role "<role>" at level "<level>" for repos "<repos>"
    Then the mint probe outcome is "<outcome>"
    And the mint probe target repository is "<target repo>"
    And the mint probe repository selection is "<selection>"

    Examples:
      | forge  | role       | repos      | level | workflow host | outcome      | target repo | selection |
      | github | triage     | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | triage     | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | coder      | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | coder      | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | review     | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | review     | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | retro      | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | retro      | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | prioritize | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | prioritize | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | fullsend   | shim repo  | read  | approved      | minted       | shim repo   | selected  |
      | github | fullsend   | shim repo  | write | approved      | minted       | shim repo   | selected  |
      | github | triage     | *          | write | approved      | scope-denied | none        | none      |
      | github | coder      | *          | read  | approved      | scope-denied | none        | none      |
      | github | review     | other repo | write | approved      | scope-denied | none        | none      |
      | github | retro      | other repo | read  | approved      | scope-denied | none        | none      |
      | github | prioritize | shim repo  | write | unapproved    | host-denied  | none        | none      |
      | github | fullsend   | shim repo  | read  | unapproved    | host-denied  | none        | none      |
