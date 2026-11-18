# File-level skill overrides and the persona manifest (#6158). A harness
# `skills:` entry in map form names files inside the skill: null removes a
# file from this run's copy, a URL replaces (or adds) one. The runner copies
# the skill into a per-run directory, applies the overrides there, and
# writes the persona manifest (.fullsend/personas.json in the sandbox
# workspace, FULLSEND_PERSONAS_FILE) that every runtime reads. The dummy
# runtime checks that manifest from inside the sandbox, so this needs no
# inference.
#
# The skill is the shipped pr-review skill from fullsend-ai/agents, pinned at
# commit 4a6645268c6c8063c15ce5cb1b88a4f3930c74da. The directory hash is the
# runner's tree hash (sha256 over the sorted "path:sha256(content)" lines of
# every file under skills/pr-review, nested github/ and gitlab/ included).
# security.md is replaced by the same file at the older agents commit
# cc64bb29346c43822ad5d9fd14351c7d98d642ca, whose bytes differ from the
# pinned one but whose frontmatter still says `name: security`; the file
# hash is sha256 of those bytes, so the manifest's sha256 for security must
# equal it. Re-pin the directory and file hashes together.
#
# The run goes through base: composition, the route a user takes to overlay
# a shipped harness: the base harness declares the pinned skill and the
# security replacement, and the dispatched child lists the same skill by a
# relative path it does not have (no .fullsend/skills/pr-review) with its own
# overrides: user-experience.md removed and sub-agents/rbac-review.md added
# from a file in .fullsend/personas. The child's entry matches the base's
# skill by name and keeps the base's source, so the staged tree is the
# pinned skill with both layers of overrides applied.
#
# Skill directories are fetched by github.com tree URL, which the install's
# default allowlist (raw.githubusercontent.com) does not cover, so the
# repository allowlist gains the github.com prefix the harness declares.
Feature: Skill file overrides stage a per-run skill copy and a persona manifest

  Scenario: a base-composed URL skill with removed, replaced and added sub-agents records all three in the persona manifest
    Given the enrolled test repository
    And the repository allows remote resources from "https://github.com/fullsend-ai/agents/"
    # The base declares the skill; it has no trigger, so it is not dispatched itself.
    And a custom harness "skill-overrides-base" with:
      """
      agent: agents/skill-overrides.md
      role: triage
      slug: fullsend-ai-skill-overrides-base
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      allowed_remote_resources:
        - https://github.com/fullsend-ai/agents/
        - https://raw.githubusercontent.com/fullsend-ai/agents/
      skills:
        - https://github.com/fullsend-ai/agents/tree/4a6645268c6c8063c15ce5cb1b88a4f3930c74da/skills/pr-review#sha256=d0a639e6870c3e204db83855cdf1e4e10e6eb3ecfaee1695bbf8630843cb5c6c:
            sub-agents/security.md: https://raw.githubusercontent.com/fullsend-ai/agents/cc64bb29346c43822ad5d9fd14351c7d98d642ca/skills/pr-review/sub-agents/security.md#sha256=93fa30d04fde0af9e96e115e5fcb27d3223ebfb9d456b87ad5638dba67e6e84f
      """
    And a fullsend file "personas/rbac-review.md" with:
      """
      ---
      name: rbac-review
      description: Reviews role and permission changes (behaviour test persona).
      ---
      Review RBAC changes in the pull request.
      """
    # A harness's allowlist is never inherited from its base, so the child repeats it.
    And a custom harness "skill-overrides" with base "skill-overrides-base" and:
      """
      slug: fullsend-ai-skill-overrides
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-skill-overrides"
      allowed_remote_resources:
        - https://github.com/fullsend-ai/agents/
        - https://raw.githubusercontent.com/fullsend-ai/agents/
      skills:
        - skills/pr-review:
            sub-agents/user-experience.md: null
            sub-agents/rbac-review.md: personas/rbac-review.md
      """
    And a dummy agent that would:
      | description                                   | op            | args                                                                                                                                                                                                  |
      | See user-experience.md recorded as removed    | assert_jq     | .fullsend/personas.json, any(.removed[]; .skill == "pr-review" and .path == "sub-agents/user-experience.md")                                                                                         |
      | See no persona named user-experience          | assert_jq     | .fullsend/personas.json, all(.personas[]; .name != "user-experience")                                                                                                                                |
      | See security staged from the override         | assert_jq     | .fullsend/personas.json, any(.personas[]; .skill == "pr-review" and .name == "security" and .path == "sub-agents/security.md" and .origin == "override")                                             |
      | See security carry the override's bytes       | assert_jq     | .fullsend/personas.json, any(.personas[]; .skill == "pr-review" and .name == "security" and .sha256 == "93fa30d04fde0af9e96e115e5fcb27d3223ebfb9d456b87ad5638dba67e6e84f")                           |
      | See rbac-review added from the user's file    | assert_jq     | .fullsend/personas.json, any(.personas[]; .skill == "pr-review" and .name == "rbac-review" and .path == "sub-agents/rbac-review.md" and .origin == "added")                                         |
      | See correctness staged from upstream          | assert_jq     | .fullsend/personas.json, any(.personas[]; .skill == "pr-review" and .name == "correctness" and .path == "sub-agents/correctness.md" and .origin == "upstream")                                       |
      | Emit result                                   | write_fixture | output/agent-result.json, fixtures/dispatch/ok.json                                                                                                                                                   |
    And an issue
    When the issue is labeled "ready-for-skill-overrides"
    Then the harness "skill-overrides" workflow completes successfully
    And the harness "skill-overrides" workflow logs contain "applying its file overrides to base skill"
    And the agent will succeed to See user-experience.md recorded as removed
    And the agent will succeed to See no persona named user-experience
    And the agent will succeed to See security staged from the override
    And the agent will succeed to See security carry the override's bytes
    And the agent will succeed to See rbac-review added from the user's file
    And the agent will succeed to See correctness staged from upstream
    And the agent will succeed to Emit result
