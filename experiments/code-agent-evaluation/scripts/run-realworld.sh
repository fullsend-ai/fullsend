#!/bin/bash
set -euo pipefail

# Run real-world evaluation: V5 vs V7 against real forked repo issues
# Usage: run-realworld.sh [--results-dir <path>] [--trials N]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "${SCRIPT_DIR}")"

TRIALS="${1:-3}"
RESULTS_DIR="${2:-}"

if [[ -z "${RESULTS_DIR}" ]]; then
    RESULTS_DIR="${PROJECT_DIR}/results/realworld-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "${RESULTS_DIR}"

SCENARIOS="R01 R02"
VARIANTS="V5 V7"

log() {
    echo "[$(date -Iseconds)] $*" | tee -a "${RESULTS_DIR}/run.log"
}

total=0
completed=0
failed=0

for s in ${SCENARIOS}; do
    for v in ${VARIANTS}; do
        for t in $(seq 1 "${TRIALS}"); do
            total=$((total + 1))
        done
    done
done

log "Starting real-world evaluation: ${total} trials (${TRIALS} trials × 2 scenarios × 2 variants)"
log "Results: ${RESULTS_DIR}"

for s in ${SCENARIOS}; do
    for v in ${VARIANTS}; do
        for t in $(seq 1 "${TRIALS}"); do
            trial_dir="${RESULTS_DIR}/${s}/${v}/trial-${t}"

            if [[ -f "${trial_dir}/composite-score.json" ]]; then
                log "SKIP ${s}/${v}/trial-${t} (already completed)"
                completed=$((completed + 1))
                continue
            fi

            mkdir -p "${trial_dir}"
            log "RUN  ${s}/${v}/trial-${t} [${completed}/${total} done, ${failed} failed]"

            if "${SCRIPT_DIR}/run-single-trial.sh" \
                --scenario "${s}" \
                --variant "${v}" \
                --trial "${t}" \
                --output-dir "${trial_dir}"; then
                completed=$((completed + 1))
                log "PASS ${s}/${v}/trial-${t}"
            else
                failed=$((failed + 1))
                log "FAIL ${s}/${v}/trial-${t} (exit $?)"
            fi
        done
    done
done

log "Complete: ${completed}/${total} succeeded, ${failed} failed"
log "Results in: ${RESULTS_DIR}"
