# Plan: Use install.sh in action.yml

## Current State

`action.yml` has 90+ lines of inline bash for binary installation:
- Detect install method (vendored/release/source)
- Resolve `latest` to tag
- Download and extract tarball
- Source build fallback (broken for SHAs)

## Proposed Change

Replace inline installation logic with `scripts/install.sh`:
- Handles `latest`, version tags, and SHA resolution
- Errors on non-release SHAs (no broken source builds)
- Single source of truth for CLI users and CI

## Diff: action.yml

```yaml
# Replace lines 48-245 (detect + install steps) with:

    - name: Install fullsend
      if: steps.detect.outputs.install-method != 'vendored'
      shell: bash
      env:
        VERSION: ${{ inputs.version }}
        GH_TOKEN: ${{ inputs.github_token }}
      run: |
        set -euo pipefail
        export FULLSEND_VERSION="${VERSION}"
        export FULLSEND_INSTALL_DIR="${RUNNER_TEMP}/fullsend"
        "${GITHUB_ACTION_PATH}/scripts/install.sh"
        echo "${FULLSEND_INSTALL_DIR}" >> "${GITHUB_PATH}"
```

## Keep Separate

Vendored binary detection stays in action.yml — it checks workspace paths specific to GHA context.

```yaml
    - name: Detect vendored binary
      id: detect
      shell: bash
      run: |
        set -euo pipefail
        VENDORED=""
        if [[ -f "${GITHUB_WORKSPACE}/.fullsend/bin/fullsend" ]]; then
          VENDORED="${GITHUB_WORKSPACE}/.fullsend/bin/fullsend"
        elif [[ -f "${GITHUB_WORKSPACE}/bin/fullsend" ]]; then
          VENDORED="${GITHUB_WORKSPACE}/bin/fullsend"
        fi
        if [[ -n "${VENDORED}" ]]; then
          echo "Using vendored binary: ${VENDORED#"${GITHUB_WORKSPACE}/"}"
          echo "install-method=vendored" >> "${GITHUB_OUTPUT}"
          echo "vendored-path=${VENDORED}" >> "${GITHUB_OUTPUT}"
        else
          echo "install-method=download" >> "${GITHUB_OUTPUT}"
        fi

    - name: Install vendored binary
      if: steps.detect.outputs.install-method == 'vendored'
      shell: bash
      env:
        VENDORED: ${{ steps.detect.outputs.vendored-path }}
      run: |
        set -euo pipefail
        mkdir -p "${RUNNER_TEMP}/fullsend"
        cp "${VENDORED}" "${RUNNER_TEMP}/fullsend/fullsend"
        chmod +x "${RUNNER_TEMP}/fullsend/fullsend"
        echo "${RUNNER_TEMP}/fullsend" >> "${GITHUB_PATH}"
```

## Remove

- `retry_curl` function (install.sh handles retries if needed)
- Release API check logic
- Source build steps (setup-go, clone, make)
- All related conditionals

## Net Effect

- ~150 lines removed from action.yml
- SHA pinning works out of the box
- CLI users get same install path: `curl ... | bash`
- Source builds dropped — pin to releases only

## Migration

1. Merge install.sh to main
2. Update action.yml to use it
3. Update docs to recommend `curl .../install.sh | bash`
4. Remove source build documentation
