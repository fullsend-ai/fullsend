#!/bin/bash
set -euo pipefail

# invoke-variant.sh: Variant-specific agent invocation
# Usage: invoke-variant.sh --variant V1-V8|A1-A7 --clone-dir /path/to/clone --issue-url https://... --output-file /path/to/transcript.txt

VARIANT=""
CLONE_DIR=""
ISSUE_URL=""
OUTPUT_FILE=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_ROOT="$(dirname "${SCRIPT_DIR}")"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --variant)
      VARIANT="$2"
      shift 2
      ;;
    --clone-dir)
      CLONE_DIR="$2"
      shift 2
      ;;
    --issue-url)
      ISSUE_URL="$2"
      shift 2
      ;;
    --output-file)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Validate arguments
if [[ -z "${VARIANT}" || -z "${CLONE_DIR}" || -z "${ISSUE_URL}" || -z "${OUTPUT_FILE}" ]]; then
  echo "Usage: invoke-variant.sh --variant V1-V8|A1-A7 --clone-dir /path/to/clone --issue-url https://... --output-file /path/to/transcript.txt" >&2
  exit 1
fi

if [[ ! "${VARIANT}" =~ ^(V[1-8]|A[1-7])$ ]]; then
  echo "Error: Variant must be V1-V8 or A1-A7" >&2
  exit 1
fi

if [[ ! -d "${CLONE_DIR}" ]]; then
  echo "Error: Clone directory does not exist: ${CLONE_DIR}" >&2
  exit 1
fi

# Change to clone directory
cd "${CLONE_DIR}"

# Safety timeout: kill runaway agents after 10 minutes
AGENT_TIMEOUT="${AGENT_TIMEOUT:-600}"

# Helper: create symlinks from top-level dirs to .claude/ artifacts.
# Uses a loop because `ln -sf ../.claude/dir/*` doesn't glob-expand correctly
# (the shell resolves the glob relative to CWD, not the symlink target).
setup_symlinks() {
  mkdir -p agents skills scripts
  for f in .claude/agents/*; do [ -e "$f" ] && ln -sf "../$f" agents/; done
  for f in .claude/skills/*; do [ -e "$f" ] && ln -sf "../$f" skills/; done
  for f in .claude/scripts/*; do [ -e "$f" ] && ln -sf "../$f" scripts/; done
}

# Provision variant artifacts and invoke agent
case "${VARIANT}" in
  V1)
    # V1: fullsend single-skill
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V1-fullsend-single-skill"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V2)
    # V2: fullsend multi-skill
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V2-fullsend-multi-skill"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V3)
    # V3: vanilla Claude with prompt.txt
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V3-vanilla-claude"

    # No artifacts to copy

    # Read and process prompt template
    if [[ ! -f "${VARIANT_DIR}/prompt.txt" ]]; then
      echo "Error: V3 prompt.txt not found at ${VARIANT_DIR}/prompt.txt" >&2
      exit 1
    fi

    PROMPT="$(sed "s|{{ISSUE_URL}}|${ISSUE_URL}|g" "${VARIANT_DIR}/prompt.txt")"
    # Extract issue number for {{ISSUE_NUMBER}} placeholder
    ISSUE_NUMBER="$(echo "${ISSUE_URL}" | grep -o '[0-9]*$')"
    PROMPT="$(echo "${PROMPT}" | sed "s|{{ISSUE_NUMBER}}|${ISSUE_NUMBER}|g")"

    # Invoke Claude with prompt
    timeout "${AGENT_TIMEOUT}" claude -p --max-turns 20 --dangerously-skip-permissions \
      "${PROMPT}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V4)
    # V4: CLAUDE.md only — infrastructure exists but V4 was excluded from
    # scored results (started then stopped; see EXPERIMENT.md section 2)
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V4-claudemd-only"

    # Copy CLAUDE.md to repo root
    if [[ ! -f "${VARIANT_DIR}/CLAUDE.md" ]]; then
      echo "Error: V4 CLAUDE.md not found at ${VARIANT_DIR}/CLAUDE.md" >&2
      exit 1
    fi
    cp "${VARIANT_DIR}/CLAUDE.md" ./CLAUDE.md

    # Set ISSUE_NUMBER environment variable for the CLAUDE.md instructions
    ISSUE_NUMBER="$(echo "${ISSUE_URL}" | grep -o '[0-9]*$')"
    export ISSUE_NUMBER

    # Invoke Claude with basic prompt
    timeout "${AGENT_TIMEOUT}" claude -p --max-turns 20 --dangerously-skip-permissions \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V5)
    # V5: apex — best-possible agent + skill design
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V5-apex"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V6)
    # V6: apex-github — GitHub-specialized best agent + skill
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V6-apex-github"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V7)
    # V7: ultimate — fused best-of-all-variants agent + skill
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V7-ultimate"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  V8)
    # V8: hybrid — cleaned V1 + V5 minimal-diff + V7 reproduction/task-type
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/V8-hybrid"

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  A[1-5])
    # A1-A5: Modified V1 variants (agent + skill + scripts)
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/${VARIANT}-"
    case "${VARIANT}" in
      A1) VARIANT_DIR+="no-secret-scan" ;;
      A2) VARIANT_DIR+="no-disallowedtools" ;;
      A3) VARIANT_DIR+="no-explicit-staging" ;;
      A4) VARIANT_DIR+="no-protected-paths" ;;
      A5) VARIANT_DIR+="no-retry-limit" ;;
    esac

    # Copy variant artifacts
    mkdir -p .claude/{agents,skills,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/skills/"* .claude/skills/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    setup_symlinks

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  A6)
    # A6: Agent only (no skills)
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/A6-no-skill"

    # Copy variant artifacts (agents and scripts only)
    mkdir -p .claude/{agents,scripts}
    cp -r "${VARIANT_DIR}/agents/"* .claude/agents/
    cp -r "${VARIANT_DIR}/scripts/"* .claude/scripts/
    chmod +x .claude/scripts/*

    mkdir -p agents scripts
    for f in .claude/agents/*; do [ -e "$f" ] && ln -sf "../$f" agents/; done
    for f in .claude/scripts/*; do [ -e "$f" ] && ln -sf "../$f" scripts/; done

    # Invoke agent
    timeout "${AGENT_TIMEOUT}" claude --dangerously-skip-permissions --agent code \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  A7)
    # A7: Skill only (CLAUDE.md like V4)
    VARIANT_DIR="${EXPERIMENT_ROOT}/variants/A7-skill-only"

    # Copy CLAUDE.md to repo root
    if [[ ! -f "${VARIANT_DIR}/CLAUDE.md" ]]; then
      echo "Error: A7 CLAUDE.md not found at ${VARIANT_DIR}/CLAUDE.md" >&2
      exit 1
    fi
    cp "${VARIANT_DIR}/CLAUDE.md" ./CLAUDE.md

    # Set ISSUE_NUMBER environment variable for the CLAUDE.md instructions
    ISSUE_NUMBER="$(echo "${ISSUE_URL}" | grep -o '[0-9]*$')"
    export ISSUE_NUMBER

    # Invoke Claude with basic prompt
    timeout "${AGENT_TIMEOUT}" claude -p --max-turns 20 --dangerously-skip-permissions \
      "Implement the fix for ${ISSUE_URL}" \
      < /dev/null > "${OUTPUT_FILE}" 2>&1
    ;;

  *)
    echo "Error: Unknown variant: ${VARIANT}" >&2
    exit 1
    ;;
esac

# Return the exit code of the Claude invocation
exit $?
