#!/bin/bash
set -euo pipefail

# Main orchestrator for code agent evaluation experiment
# Usage: run-experiment.sh [options]

# Default options
TRIALS=3
SCENARIO=""
VARIANT=""
RESUME_DIR=""
DRY_RUN=false
JUDGE_MODEL="claude-sonnet-4-6"
SECURITY_ONLY=false
ABLATION_ONLY=false

# Script directory (relative to this script)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "${SCRIPT_DIR}")"

usage() {
    cat <<EOF
Usage: $0 [options]

Options:
  --trials N            Trials per cell (default: 3, security: 5)
  --scenario S01        Run only this scenario
  --variant V1          Run only this variant
  --resume <dir>        Resume a partial run
  --dry-run             Print commands without executing
  --judge-model MODEL   Model for LLM judge (default: claude-sonnet-4-6)
  --security-only       Run security payloads against V1,V3 only
  --ablation-only       Run ablation study against A1-A3 variants
  --help                Show this help

Examples:
  $0                                    # Run full experiment
  $0 --trials 3 --scenario S01         # Run only S01 with 3 trials
  $0 --resume results/20260410T123456Z  # Resume partial run
  $0 --security-only                    # Run security red team experiment
  $0 --ablation-only                    # Run ablation study (A1-A7 variants)
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --trials)
            TRIALS="$2"
            shift 2
            ;;
        --scenario)
            SCENARIO="$2"
            shift 2
            ;;
        --variant)
            VARIANT="$2"
            shift 2
            ;;
        --resume)
            RESUME_DIR="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --judge-model)
            JUDGE_MODEL="$2"
            shift 2
            ;;
        --security-only)
            SECURITY_ONLY=true
            shift
            ;;
        --ablation-only)
            ABLATION_ONLY=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

# Set security-only defaults (if trials wasn't explicitly set)
if [[ "${SECURITY_ONLY}" == "true" ]]; then
    # Default to 5 trials for security experiments (unless explicitly set)
    if [[ "$*" != *"--trials"* ]]; then
        TRIALS=5
    fi
fi

# Set ablation-only defaults (if trials wasn't explicitly set)
if [[ "${ABLATION_ONLY}" == "true" ]]; then
    # Default to 3 trials for ablation experiments (unless explicitly set)
    if [[ "$*" != *"--trials"* ]]; then
        TRIALS=3
    fi
fi

# Validation
if ! [[ "${TRIALS}" =~ ^[0-9]+$ ]] || [[ "${TRIALS}" -lt 1 ]]; then
    echo "Error: --trials must be a positive integer" >&2
    exit 1
fi

# Check for conflicting flags
if [[ "${SECURITY_ONLY}" == "true" && "${ABLATION_ONLY}" == "true" ]]; then
    echo "Error: --security-only and --ablation-only cannot be used together" >&2
    exit 1
fi

log() {
    echo "[$(date -Iseconds)] $*" >&2
}

# Get security payloads (p01-p06 — highest-value attack vectors)
get_security_payloads() {
    for i in 01 02 03 04 05 06; do
        local match
        match=$(ls "${PROJECT_DIR}/payloads/p${i}"*.md 2>/dev/null | head -1)
        if [[ -n "${match}" ]]; then
            basename "${match}" .md
        fi
    done
}

# Validate prerequisites (Phase P steps)
validate_prerequisites() {
    log "Validating prerequisites..."

    # Check CLI tools
    for cmd in claude gh git jq go python3 node npm; do
        if ! command -v "${cmd}" >/dev/null 2>&1; then
            echo "FAIL: ${cmd} CLI not found" >&2
            exit 1
        fi
    done

    # Check GitHub auth
    if ! gh auth status 2>&1 | grep -q "Logged in"; then
        echo "FAIL: gh not authenticated" >&2
        exit 1
    fi

    AUTHED_USER="$(gh api user -q .login)"
    if [[ "${AUTHED_USER}" != "ascerra" ]]; then
        echo "FAIL: authenticated as ${AUTHED_USER}, expected ascerra" >&2
        exit 1
    fi

    # Check Claude connectivity (non-fatal — piped check can fail in some environments)
    if ! echo "What is 2+2?" | timeout 30 claude -p --max-turns 1 2>/dev/null | grep -qi "4"; then
        log "Warning: claude connectivity check failed (may work fine in non-piped mode)"
    fi

    # Check fullsend repo state
    local fullsend_root="/home/ascerra/development/devProd/ai-sdlc/fresh-dev/fullsend"
    if [[ ! -d "${fullsend_root}" ]]; then
        echo "FAIL: fullsend repo not found at ${fullsend_root}" >&2
        exit 1
    fi

    cd "${fullsend_root}"
    git fetch origin story-4-code-agent 2>/dev/null || true
    if [[ -z "$(git show origin/story-4-code-agent:agents/code.md 2>/dev/null)" ]]; then
        echo "FAIL: cannot read agents/code.md from PR #189 branch" >&2
        exit 1
    fi

    # Return to project directory
    cd "${PROJECT_DIR}"

    # Check security payloads if in security-only mode
    if [[ "${SECURITY_ONLY}" == "true" ]]; then
        local payload_count
        payload_count=$(ls payloads/p*.md 2>/dev/null | wc -l)
        if [[ "${payload_count}" -lt 6 ]]; then
            echo "FAIL: Expected at least 6 security payloads (p01-p06), found ${payload_count}" >&2
            exit 1
        fi
        log "Found ${payload_count} security payloads"
    fi

    log "Prerequisites validated successfully"
}

# Create or resume results directory
setup_results_dir() {
    if [[ -n "${RESUME_DIR}" ]]; then
        if [[ ! -d "${RESUME_DIR}" ]]; then
            echo "Error: Resume directory ${RESUME_DIR} does not exist" >&2
            exit 1
        fi
        RESULTS_DIR="$(cd "${RESUME_DIR}" && pwd)"
        log "Resuming experiment in ${RESULTS_DIR}"
    else
        # Create timestamped results directory
        local timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
        RESULTS_DIR="${PROJECT_DIR}/results/${timestamp}"

        if [[ "${DRY_RUN}" == "false" ]]; then
            mkdir -p "${RESULTS_DIR}"
            log "Created results directory: ${RESULTS_DIR}"

            if [[ "${SECURITY_ONLY}" == "true" ]]; then
                # Copy security payloads to results directory
                cp -r "${PROJECT_DIR}/payloads" "${RESULTS_DIR}/"

                # Create a security manifest
                cat > "${RESULTS_DIR}/security-manifest.json" <<EOF
{
  "experiment_type": "security",
  "payloads": [
$(ls "${PROJECT_DIR}/payloads/"p*.md | while read -r f; do
    payload_id="$(basename "${f}" .md)"
    echo "    {\"id\": \"${payload_id}\", \"file\": \"${f}\"}"
done | paste -sd ',' -)
  ]
}
EOF
            else
                # Copy manifest and scenarios to results directory
                cp "${PROJECT_DIR}/scenarios/manifest.json" "${RESULTS_DIR}/"
                cp -r "${PROJECT_DIR}/scenarios"/*.json "${RESULTS_DIR}/"
            fi
        else
            log "Would create results directory: ${RESULTS_DIR}"
        fi
    fi
}

# Get scenarios from manifest or security payloads
get_scenarios() {
    if [[ "${SECURITY_ONLY}" == "true" ]]; then
        if [[ -n "${SCENARIO}" ]]; then
            # Allow override for single payload testing
            echo "${SCENARIO}"
        else
            get_security_payloads
        fi
    elif [[ "${ABLATION_ONLY}" == "true" ]]; then
        if [[ -n "${SCENARIO}" ]]; then
            # Allow override for single scenario testing
            echo "${SCENARIO}"
        else
            # Ablation study: S01 (functional) + S11 (secret injection) + S15 (CODEOWNERS)
            echo "S01 S11 S15"
        fi
    else
        if [[ -n "${SCENARIO}" ]]; then
            echo "${SCENARIO}"
        else
            jq -r '.scenarios[].id' "${PROJECT_DIR}/scenarios/manifest.json"
        fi
    fi
}

# Get variants to test
get_variants() {
    if [[ "${SECURITY_ONLY}" == "true" ]]; then
        if [[ -n "${VARIANT}" ]]; then
            # Allow override for single variant testing
            echo "${VARIANT}"
        else
            echo "V1 V3"
        fi
    elif [[ "${ABLATION_ONLY}" == "true" ]]; then
        if [[ -n "${VARIANT}" ]]; then
            # Allow override for single variant testing
            echo "${VARIANT}"
        else
            echo "A1 A2 A3"
        fi
    else
        if [[ -n "${VARIANT}" ]]; then
            echo "${VARIANT}"
        else
            echo "V1 V2 V3 V5 V6"
        fi
    fi
}

# Check if trial is already completed
trial_completed() {
    local scenario="$1"
    local variant="$2"
    local trial="$3"

    # In dry-run mode, assume no trials are completed
    if [[ "${DRY_RUN}" == "true" ]]; then
        return 1
    fi

    local trial_dir="${RESULTS_DIR}/${scenario}/${variant}/trial-${trial}"
    [[ -f "${trial_dir}/judge-assessment.json" ]]
}

# Run a single trial
run_trial() {
    local scenario="$1"
    local variant="$2"
    local trial="$3"

    log "Running trial: ${scenario}/${variant}/trial-${trial}"

    local trial_dir="${RESULTS_DIR}/${scenario}/${variant}/trial-${trial}"

    if trial_completed "${scenario}" "${variant}" "${trial}"; then
        log "Trial already completed, skipping"
        return 0
    fi

    if [[ "${DRY_RUN}" == "false" ]]; then
        mkdir -p "${trial_dir}"

        # Call the single-trial runner
        if "${SCRIPT_DIR}/run-single-trial.sh" \
            --scenario "${scenario}" \
            --variant "${variant}" \
            --trial "${trial}" \
            --output-dir "${trial_dir}" \
            --judge-model "${JUDGE_MODEL}"; then
            log "Trial completed successfully: ${scenario}/${variant}/trial-${trial}"
        else
            local exit_code=$?
            log "Trial failed with exit code ${exit_code}: ${scenario}/${variant}/trial-${trial}"
            return "${exit_code}"
        fi
    else
        log "Would run: run-single-trial.sh --scenario ${scenario} --variant ${variant} --trial ${trial} --output-dir ${trial_dir}"
    fi
}

# Run all trials
run_trials() {
    local scenarios
    local variants

    scenarios=$(get_scenarios)
    variants=$(get_variants)

    local total_trials=0
    local completed_trials=0
    local failed_trials=0

    # Count total trials for progress reporting
    for scenario in ${scenarios}; do
        for variant in ${variants}; do
            for trial in $(seq 1 "${TRIALS}"); do
                total_trials=$((total_trials + 1))
                if trial_completed "${scenario}" "${variant}" "${trial}"; then
                    completed_trials=$((completed_trials + 1))
                fi
            done
        done
    done

    log "Starting experiment: ${completed_trials}/${total_trials} trials already completed"

    # Reset counter — it gets rebuilt accurately in the main loop
    completed_trials=0

    # Run trials
    for scenario in ${scenarios}; do
        for variant in ${variants}; do
            for trial in $(seq 1 "${TRIALS}"); do
                if ! run_trial "${scenario}" "${variant}" "${trial}"; then
                    failed_trials=$((failed_trials + 1))
                    log "Trial failed: ${scenario}/${variant}/trial-${trial}"
                    # Continue with next trial instead of failing entire experiment
                fi

                # Update progress
                if trial_completed "${scenario}" "${variant}" "${trial}"; then
                    completed_trials=$((completed_trials + 1))
                fi

                log "Progress: ${completed_trials}/${total_trials} trials completed, ${failed_trials} failed"
            done
        done
    done

    log "Experiment completed: ${completed_trials}/${total_trials} trials completed, ${failed_trials} failed"
}

# Run analysis (placeholder for now - will be implemented in Phase 7)
run_analysis() {
    log "Running cross-variant analysis..."

    if [[ "${DRY_RUN}" == "false" ]]; then
        # This will be implemented in Phase 7
        # "${SCRIPT_DIR}/summarize.sh" "${RESULTS_DIR}"
        # "${SCRIPT_DIR}/analyze-scenario.sh" "${RESULTS_DIR}"

        # For now, just create a placeholder summary
        cat > "${RESULTS_DIR}/summary.md" <<EOF
# Experiment Summary

Results directory: ${RESULTS_DIR}
Scenarios tested: $(get_scenarios | wc -w)
Variants tested: $(get_variants | wc -w)
Trials per cell: ${TRIALS}
Judge model: ${JUDGE_MODEL}

Analysis scripts will be implemented in Phase 7.
EOF

        log "Analysis completed - placeholder summary created"
    else
        log "Would run analysis scripts on ${RESULTS_DIR}"
    fi
}

# Main execution
main() {
    if [[ "${SECURITY_ONLY}" == "true" ]]; then
        log "Starting security red team experiment"
    elif [[ "${ABLATION_ONLY}" == "true" ]]; then
        log "Starting ablation study experiment"
    else
        log "Starting code agent evaluation experiment"
    fi
    log "Trials per cell: ${TRIALS}"
    log "Judge model: ${JUDGE_MODEL}"
    if [[ "${SECURITY_ONLY}" == "true" ]]; then
        log "Security-only mode: testing payloads against V1,V3 variants"
    elif [[ "${ABLATION_ONLY}" == "true" ]]; then
        log "Ablation-only mode: testing A1-A3 variants against S01, S11, S15"
    fi
    if [[ -n "${SCENARIO}" ]]; then
        log "Single scenario: ${SCENARIO}"
    fi
    if [[ -n "${VARIANT}" ]]; then
        log "Single variant: ${VARIANT}"
    fi
    if [[ "${DRY_RUN}" == "true" ]]; then
        log "DRY RUN MODE - no changes will be made"
    fi

    validate_prerequisites
    setup_results_dir
    run_trials
    run_analysis

    log "Experiment completed successfully"
    log "Results saved to: ${RESULTS_DIR}"
}

# Run main function
main "$@"
