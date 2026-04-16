#!/bin/bash
set -euo pipefail

# Setup script for code agent evaluation experiment
# Clones the external scenarios repo and symlinks required directories.
#
# Usage: ./scripts/setup.sh
#
# Set SCENARIOS_REPO to override the default source repo.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "${SCRIPT_DIR}")"
SCENARIOS_REPO="${SCENARIOS_REPO:-https://github.com/ascerra/code-agent-eval-scenarios.git}"
CLONE_TARGET="${PROJECT_DIR}/.eval-scenarios"

log() {
    echo "[setup] $*"
}

if [[ -d "${CLONE_TARGET}" ]]; then
    log "Updating existing scenarios repo..."
    git -C "${CLONE_TARGET}" pull --ff-only 2>/dev/null || log "Warning: pull failed, using existing checkout"
else
    log "Cloning ${SCENARIOS_REPO}..."
    git clone "${SCENARIOS_REPO}" "${CLONE_TARGET}"
fi

# Symlink directories that scripts expect at the project root level
for dir in scenarios payloads prompts; do
    target="${PROJECT_DIR}/${dir}"
    source="${CLONE_TARGET}/${dir}"

    if [[ -L "${target}" ]]; then
        log "${dir}/ symlink already exists, updating..."
        rm "${target}"
    elif [[ -d "${target}" ]]; then
        log "WARNING: ${dir}/ is a real directory, skipping (remove it to use symlink)"
        continue
    fi

    if [[ -d "${source}" ]]; then
        ln -s "${source}" "${target}"
        log "Linked ${dir}/ -> ${source}"
    else
        log "WARNING: ${source} not found in scenarios repo, skipping"
    fi
done

# Symlink V1-V7 variant definitions (V8 is already in this PR)
for variant_dir in "${CLONE_TARGET}/variants"/V[1-7]-*; do
    [[ -d "${variant_dir}" ]] || continue
    variant_name="$(basename "${variant_dir}")"
    target="${PROJECT_DIR}/variants/${variant_name}"

    if [[ -L "${target}" ]]; then
        rm "${target}"
    elif [[ -d "${target}" ]]; then
        continue
    fi

    ln -s "${variant_dir}" "${target}"
    log "Linked variants/${variant_name}"
done

log "Setup complete. You can now run the experiment scripts."
