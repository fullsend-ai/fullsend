@skip:gitlab
Feature: Runner secrets resolved from Vault through the job's workload identity

  ADR 0155 lets trusted configuration select a Vault connection for the
  eligible runner secrets. The run job logs in to Vault with its GitHub
  Actions OIDC token and reads each selected secret from its ordered
  KV v2 paths. The feature shares one OpenBao dev-mode server across its
  scenarios. The server is started for the first scenario, reached
  through a quick tunnel, bound to the leased repositories and stopped
  at job cleanup. Each scenario stores synthetic secrets under its own
  prefix and selects them in the leased repository's config.base.yaml.

  A host-side pre-script prints a fingerprint of each secret it
  received, never the value, so the steps can check which value reached
  which consumer. The scenarios are skipped outside GitHub Actions or
  without Podman (BEHAVIOUR_OPENBAO). When a scenario fails, the OpenBao
  audit log under BEHAVIOUR_ARTIFACT_DIR/openbao records each request
  with the run's traceparent.

  Background:
    Given the Vault test server is running
    And the enrolled test repository
    And the Vault test server allows the repository
    And a Vault fingerprint script "scripts/vault-fingerprint.sh"

  Scenario: Trace headers from Vault authenticate to the protected trace collector
    Given the Vault test server holds the trace collector headers as "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
    And the repository's native "OTEL_EXPORTER_OTLP_TRACES_HEADERS" secret holds a wrong token
    And the trusted base configuration selects the Vault test server for "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
    And a custom harness "vault-traces" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-traces
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-traces"
      """
    And a dummy agent that would:
      | description                    | op            | args                                               | expect |
      | Prove execution                | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |        |
      | Read the traces headers        | assert_env    | OTEL_EXPORTER_OTLP_TRACES_HEADERS                  | fail   |
    And an issue
    When the issue is labeled "ready-for-vault-traces"
    Then the harness "vault-traces" workflow completes successfully
    And the harness "vault-traces" workflow logs contain "Resolved runner secrets from Vault: OTEL_EXPORTER_OTLP_TRACES_HEADERS"
    And the trace collector received a trace from the harness "vault-traces" run
    And the agent will succeed to Prove execution
    And the agent will fail to Read the traces headers

  Scenario: A Jira token from Vault reaches only the host-side script that declares it
    Given the Vault test server holds a synthetic "JIRA_TOKEN"
    And the trusted base configuration selects the Vault test server for "JIRA_TOKEN"
    And a custom harness "vault-jira" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-jira
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      pre_script: scripts/vault-fingerprint.sh
      env:
        runner:
          JIRA_TOKEN: "${JIRA_TOKEN}"
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-jira"
      """
    And a dummy agent that would:
      | description          | op            | args                                               | expect |
      | Prove execution      | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |        |
      | Read the Jira token  | assert_env    | JIRA_TOKEN                                         | fail   |
    And an issue
    When the issue is labeled "ready-for-vault-jira"
    Then the harness "vault-jira" workflow completes successfully
    And the harness "vault-jira" workflow logs contain "Resolved runner secrets from Vault: JIRA_TOKEN"
    And the harness "vault-jira" workflow logs contain the fingerprint of the Vault "JIRA_TOKEN"
    And the agent will succeed to Prove execution
    And the agent will fail to Read the Jira token

  Scenario: Generic OTLP headers from Vault stay distinct from the trace-specific headers
    Given the Vault test server holds a synthetic "OTEL_EXPORTER_OTLP_HEADERS"
    And the trusted base configuration selects the Vault test server for "OTEL_EXPORTER_OTLP_HEADERS"
    And a custom harness "vault-otlp" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-otlp
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      pre_script: scripts/vault-fingerprint.sh
      env:
        runner:
          OTEL_EXPORTER_OTLP_HEADERS: "${OTEL_EXPORTER_OTLP_HEADERS}"
          OTEL_EXPORTER_OTLP_TRACES_HEADERS: "${OTEL_EXPORTER_OTLP_TRACES_HEADERS}"
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-otlp"
      """
    And a dummy agent that would:
      | description               | op            | args                                               | expect |
      | Prove execution           | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |        |
      | Read the OTLP headers     | assert_env    | OTEL_EXPORTER_OTLP_HEADERS                         | fail   |
    And an issue
    When the issue is labeled "ready-for-vault-otlp"
    Then the harness "vault-otlp" workflow completes successfully
    And the harness "vault-otlp" workflow logs contain "Resolved runner secrets from Vault: OTEL_EXPORTER_OTLP_HEADERS"
    And the harness "vault-otlp" workflow logs contain the fingerprint of the Vault "OTEL_EXPORTER_OTLP_HEADERS"
    And the harness "vault-otlp" workflow logs do not contain the fingerprint of the Vault "OTEL_EXPORTER_OTLP_HEADERS" as "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
    And the agent will succeed to Prove execution
    And the agent will fail to Read the OTLP headers

  Scenario: A runner secrets bundle from Vault stays host-side
    Given the Vault test server holds a synthetic "FULLSEND_GITHUB_RUNNER_SECRETS"
    And the trusted base configuration selects the Vault test server for "FULLSEND_GITHUB_RUNNER_SECRETS"
    And a custom harness "vault-bundle" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-bundle
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      pre_script: scripts/vault-fingerprint.sh
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-bundle"
      overlays:
        - when: 'runtime.forge == "github"'
          env:
            runner:
              BEHAVIOUR_VAULT_RUNNER_SECRET: "${BEHAVIOUR_VAULT_RUNNER_SECRET}"
      """
    And a dummy agent that would:
      | description             | op            | args                                               | expect |
      | Prove execution         | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |        |
      | Read the bundle         | assert_env    | FULLSEND_GITHUB_RUNNER_SECRETS                     | fail   |
      | Read the bundle entry   | assert_env    | BEHAVIOUR_VAULT_RUNNER_SECRET                      | fail   |
    And an issue
    When the issue is labeled "ready-for-vault-bundle"
    Then the harness "vault-bundle" workflow completes successfully
    And the harness "vault-bundle" workflow logs contain "Resolved runner secrets from Vault: FULLSEND_GITHUB_RUNNER_SECRETS"
    And the harness "vault-bundle" workflow logs contain the fingerprint of the Vault "BEHAVIOUR_VAULT_RUNNER_SECRET"
    And the harness "vault-bundle" workflow logs do not contain "vault-bundle-visible-to-script"
    And the agent will succeed to Prove execution
    And the agent will fail to Read the bundle
    And the agent will fail to Read the bundle entry

  Scenario: An invalid Vault connection in trusted configuration stops the run
    Given a fullsend file "config.base.yaml" with:
      """
      connections:
        bt-openbao:
          kind: vault
          vault_address: http://vault.invalid
      secrets:
        JIRA_TOKEN:
          connection: bt-openbao
          vault_secrets:
            - secret/data/fullsend-bt/invalid/jira
      """
    And a custom harness "vault-invalid" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-invalid
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-invalid"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-vault-invalid"
    Then the harness "vault-invalid" workflow fails with "vault_address must use https" in its logs
    And the harness "vault-invalid" agent did not run

  Scenario: A pull request cannot redirect a Vault secret
    Given the Vault test server holds a synthetic "JIRA_TOKEN"
    And the trusted base configuration selects the Vault test server for "JIRA_TOKEN"
    And a custom harness "vault-override" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-override
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      pre_script: scripts/vault-fingerprint.sh
      env:
        runner:
          JIRA_TOKEN: "${JIRA_TOKEN}"
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-override"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And a pull request that points "JIRA_TOKEN" at another Vault secret
    When the pull request is labeled "ready-for-vault-override"
    Then the harness "vault-override" workflow completes successfully
    And the harness "vault-override" workflow logs contain the fingerprint of the Vault "JIRA_TOKEN"
    And the harness "vault-override" workflow logs do not contain the fingerprint of the overriding "JIRA_TOKEN"
    And the agent will succeed to Prove execution

  Scenario: A failed Vault lookup does not fall back to the native secret
    Given the repository has the runner secret "BEHAVIOUR_RUNNER_SECRET"
    And the trusted base configuration selects a missing Vault secret for "FULLSEND_GITHUB_RUNNER_SECRETS"
    And a custom harness "vault-missing" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-vault-missing
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-vault-missing"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-vault-missing"
    Then the harness "vault-missing" workflow fails with "not falling back to the native source" in its logs
