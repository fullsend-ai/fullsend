@skip:gitlab
Feature: GitHub bot-role lookup

  Real GitHub Actions OIDC lookups against the suite's mint (the preview
  mint on dev, the stage mint on stage) of the Fullsend role registered
  for a bot login (ADR 0107, POST /v1/bot-role). A registered bot of the
  behaviour app set must be recognized with its role. The suite's own
  e2e App, which no role of that app set uses, must be a successful
  no-match, also when the request claims a role for it. A lookup the
  mint cannot authenticate must fail, and never read as a no-match.

  Each row runs one workflow in the leased repository, pushed to a
  temporary branch. It calls a reusable probe workflow hosted by the
  slot's approved mint-probe host, so the OIDC repository claim is the
  leased repository. The forced-failure row sends the registered review
  bot with a credential whose signature was replaced, after the probe
  verified the real token, so the mint must answer 401.

  The same run also makes a control lookup: the registered review bot,
  which must be recognized as "review". An OIDC, transport, workflow,
  server or result failure fails the row instead of passing as a
  no-match or a lookup failure. The outcome is then projected onto the
  normalized actor and checked against the pre-CEL dispatch gate for a
  non-label event: a no-match (role_verified true) and a failed lookup
  (role_verified false) are both denied, but stay distinguishable. The
  probe reports only compact fields; no token is kept.

  Scenario Outline: bot-role lookup for <identity>
    Given the enrolled test repository
    And a "<forge>" bot-role lookup probe
    When the lookup resolves identity "<identity>" for the leased repository
    Then the lookup outcome is "<outcome>"
    And the actor is kind "<kind>" role "<role>" bot_role "<bot_role>" role_verified "<verified>"
    And non-label dispatch is "<dispatch>"

    Examples:
      | forge  | identity          | outcome       | kind | role | bot_role | verified | dispatch          |
      | github | registered:review | recognized    | bot  | none | review   | true     | allowed           |
      | github | unregistered-e2e  | no-match      | bot  | none | none     | true     | denied-before-cel |
      | github | claimed-in-event  | no-match      | bot  | none | none     | true     | denied-before-cel |
      | github | forced-failure    | lookup-failed | bot  | none | none     | false    | denied-before-cel |
