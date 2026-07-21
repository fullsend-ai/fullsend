Feature: URL-sourced harness dispatch

  Background:
    Given the enrolled test repository
    And a harness-hosting repository "url-harness-host"

  Scenario: URL-sourced harness with CEL trigger dispatches agent
    Given a URL-sourced custom harness "url-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-url-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-url-ping"
      """
    And a dummy agent that would:
      | description              | op           | args                                               |
      | Issue URL set            | assert_env   | GITHUB_ISSUE_URL                                   |
      | Prove URL execution      | write_fixture| output/dispatch-url-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-url-ping"
    Then the harness "url-ping" workflow completes successfully
    And the agent will succeed to Prove URL execution

  Scenario: Config mixes URL-sourced and local harnesses
    Given a custom harness "local-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-local-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-mixed-ping"
      """
    And a URL-sourced custom harness "url-mixed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-url-mixed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-url-mixed-ping"
      """
    And a dummy agent that would:
      | description              | op           | args                                               |
      | Prove local execution    | write_fixture| output/dispatch-local-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-mixed-ping"
    Then the harness "local-ping" workflow completes successfully
    And the agent will succeed to Prove local execution

  Scenario: URL source with bad integrity hash is skipped and dispatch continues
    Given a custom harness "good-local" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-good-local
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-integrity-test"
      """
    And a URL-sourced custom harness "bad-hash" with bad integrity hash:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-bad-hash
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-integrity-test"
      """
    And a dummy agent that would:
      | description              | op           | args                                               |
      | Prove fallback execution | write_fixture| output/dispatch-fallback-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-integrity-test"
    Then the harness "good-local" workflow completes successfully
    And the agent will succeed to Prove fallback execution
    And the harness "bad-hash" agent did not run

  Scenario: URL source not in allowlist is skipped and dispatch continues
    Given a custom harness "good-allowed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-good-allowed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-allowlist-test"
      """
    And a URL-sourced custom harness "no-allow" not in allowlist with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-no-allow
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-allowlist-test"
      """
    And a dummy agent that would:
      | description                | op           | args                                                    |
      | Prove allowlist fallback   | write_fixture| output/dispatch-allowlist-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-allowlist-test"
    Then the harness "good-allowed" workflow completes successfully
    And the agent will succeed to Prove allowlist fallback
    And the harness "no-allow" agent did not run
