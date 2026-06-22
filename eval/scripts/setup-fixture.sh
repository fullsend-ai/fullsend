#!/usr/bin/env bash
# before_each hook: create ephemeral repo and fixture for a test case.
#
# Reads input.yaml from CASE_SOURCE_DIR, creates an ephemeral GitHub repo,
# pushes test content, creates the fixture (issue or PR), and writes
# .hook-outputs.yaml so the harness passes dynamic URLs to the runner.
#
# Required env (set by harness + eval.yaml execution.env):
#   CASE_SOURCE_DIR  — path to the original case directory in the dataset
#   CASE_WORKSPACE   — path to the case workspace (cwd)
#   EVAL_ORG         — GitHub org/user for ephemeral repos
#   GH_TOKEN         — GitHub token with repo and delete_repo scope
#
# Writes:
#   $CASE_WORKSPACE/.hook-outputs.yaml — env vars for the runner
set -euo pipefail

CASE_WORKSPACE="${CASE_WORKSPACE:?CASE_WORKSPACE is required}"
EVAL_ORG="${EVAL_ORG:?EVAL_ORG is required}"

# CASE_SOURCE_DIR is set by the harness but may resolve incorrectly when
# dataset.path is relative. Fall back to locating input.yaml via the
# eval config's directory.
CASE_SOURCE_DIR="${CASE_SOURCE_DIR:?CASE_SOURCE_DIR is required}"
if [[ ! -d "$CASE_SOURCE_DIR" ]]; then
  config="${AGENT_EVAL_CONFIG:?}"
  config_dir="$(dirname "$config")"
  case_id="${CASE_ID:?}"
  dataset_path="$(yq -r '.dataset.path // "cases"' "$config")"
  CASE_SOURCE_DIR="$(cd "$config_dir" && cd "$dataset_path" && cd "$case_id" && pwd)"
fi

for cmd in gh yq jq git uuidgen; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is required but not found in PATH" >&2
    exit 1
  fi
done

INPUT="${CASE_SOURCE_DIR}/input.yaml"
if [[ ! -f "$INPUT" ]]; then
  echo "ERROR: ${INPUT} not found" >&2
  exit 1
fi

FORGE=$(yq -r '.forge // "github"' "$INPUT")
FIXTURE_TYPE=$(yq -r '.fixture.type // "issue"' "$INPUT")
FIXTURE_TITLE=$(yq -r '.fixture.title' "$INPUT")
FIXTURE_BODY=$(yq -r '.fixture.body' "$INPUT")
FIXTURE_BASE=$(yq -r '.fixture.base // "main"' "$INPUT")
FIXTURE_HEAD=$(yq -r '.fixture.head_branch // ""' "$INPUT")
FIXTURE_FILES=$(yq -r '.fixture.files // "[]"' "$INPUT")

# --- Create ephemeral repo ---
uuid=$(uuidgen | tr '[:upper:]' '[:lower:]' | cut -c1-8)
CASE_ID_SAFE=$(basename "$CASE_SOURCE_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g')
repo_name="eval-${CASE_ID_SAFE}-${uuid}"
EPHEMERAL_REPO="${EVAL_ORG}/${repo_name}"

gh repo create "$EPHEMERAL_REPO" --public --description "Ephemeral eval repo (auto-deleted)"
echo "Created repo: $EPHEMERAL_REPO"

TARGET_DIR=$(mktemp -d)
GH_CRED_HELPER='!f(){ echo "password=${GH_TOKEN}"; };f'
git -c "credential.helper=${GH_CRED_HELPER}" \
  clone "https://x-access-token@github.com/${EPHEMERAL_REPO}.git" "$TARGET_DIR"
git -C "$TARGET_DIR" config credential.helper "${GH_CRED_HELPER}"

if [[ -d "${CASE_SOURCE_DIR}/repo" ]]; then
  cp -a "${CASE_SOURCE_DIR}/repo/." "$TARGET_DIR/"
else
  echo "# Eval test repo" > "$TARGET_DIR/README.md"
fi

git -C "$TARGET_DIR" add -A
if ! git -C "$TARGET_DIR" diff --cached --quiet; then
  git -C "$TARGET_DIR" commit -m "eval: initial content"
  git -C "$TARGET_DIR" push origin HEAD
fi

# --- Create seed issues (if any) ---
SEED_COUNT=$(yq -r '.seed_issues // [] | length' "$INPUT")
if [[ "$SEED_COUNT" -gt 0 ]]; then
  for i in $(seq 0 $((SEED_COUNT - 1))); do
    seed_title=$(yq -r ".seed_issues[$i].title" "$INPUT")
    seed_body=$(yq -r ".seed_issues[$i].body" "$INPUT")
    seed_url=$(gh issue create \
      --repo "$EPHEMERAL_REPO" \
      --title "$seed_title" \
      --body "$seed_body")
    echo "Created seed issue: $seed_url"
  done
fi

# --- Create fixture ---
FIXTURE_URL=""
FIXTURE_NUMBER=""

case "${FORGE}:${FIXTURE_TYPE}" in
  github:issue)
    FIXTURE_URL=$(gh issue create \
      --repo "$EPHEMERAL_REPO" \
      --title "$FIXTURE_TITLE" \
      --body "$FIXTURE_BODY")
    FIXTURE_NUMBER="${FIXTURE_URL##*/}"
    echo "Created issue: $FIXTURE_URL"
    ;;
  github:pull_request)
    PR_BRANCH="${FIXTURE_HEAD:-eval-pr-$(date +%s)-$$}"
    git -C "$TARGET_DIR" checkout -b "$PR_BRANCH"
    file_count=$(echo "$FIXTURE_FILES" | yq -r 'length')
    for i in $(seq 0 $((file_count - 1))); do
      path=$(echo "$FIXTURE_FILES" | yq -r ".[$i].path")
      mkdir -p "$TARGET_DIR/$(dirname "$path")"
      echo "$FIXTURE_FILES" | yq -r ".[$i].content" > "$TARGET_DIR/$path"
    done
    git -C "$TARGET_DIR" add -A
    git -C "$TARGET_DIR" commit -m "eval: fixture changes"
    git -C "$TARGET_DIR" push origin "$PR_BRANCH"
    FIXTURE_URL=$(gh pr create \
      --repo "$EPHEMERAL_REPO" \
      --base "$FIXTURE_BASE" \
      --head "$PR_BRANCH" \
      --title "$FIXTURE_TITLE" \
      --body "$FIXTURE_BODY")
    FIXTURE_NUMBER="${FIXTURE_URL##*/}"
    echo "Created PR: $FIXTURE_URL"
    ;;
  *)
    echo "ERROR: unsupported forge:fixture_type = ${FORGE}:${FIXTURE_TYPE}" >&2
    exit 1
    ;;
esac

# Clean up the local clone
rm -rf "$TARGET_DIR"

# --- Write hook outputs ---
# The harness reads this file and injects env vars into the CLI runner
# and forward-propagates them to after_each hooks.
cat > "$CASE_WORKSPACE/.hook-outputs.yaml" <<YAML
env:
  EPHEMERAL_REPO: "${EPHEMERAL_REPO}"
  FIXTURE_URL: "${FIXTURE_URL}"
  FIXTURE_NUMBER: "${FIXTURE_NUMBER}"
  FIXTURE_TYPE: "${FIXTURE_TYPE}"
  FORGE: "${FORGE}"
data:
  ephemeral_repo: "${EPHEMERAL_REPO}"
  fixture_url: "${FIXTURE_URL}"
  fixture_type: "${FIXTURE_TYPE}"
YAML

echo "Hook outputs written to $CASE_WORKSPACE/.hook-outputs.yaml"
