#!/usr/bin/env bash
# run-experiment.sh — Build fullsend, sync experiment files to the .fullsend repo,
# upload the binary to a GitHub release, and trigger the workflow.
#
# Usage: ./experiments/runner-hello-world/experiment/run-experiment.sh
#
# Prerequisites:
#   - gh CLI authenticated
#   - Go toolchain installed
#   - podman (for building and pushing container images)
#   - /tmp/dot-fullsend is a clone of test-fullsend/.fullsend

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
RUNNER_DIR="${REPO_ROOT}/experiments/runner-hello-world"
EXPERIMENT_DIR="${RUNNER_DIR}/experiment"
FULLSEND_DIR="${RUNNER_DIR}/.fullsend"
FULLSEND_REPO="/tmp/dot-fullsend"
RELEASE_REPO="maruiz93/fullsend"
RELEASE_TAG="runner-hello-world-dev"
WORKFLOW_REPO="test-fullsend/.fullsend"
WORKFLOW_FILE="hello-world.yml"
TARGET_REPO="test-fullsend/test-repo"
IMAGE_REPO="quay.io/manonru/fullsend-exp"
IMAGE_TAG="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
BASE_IMAGE="fullsend-sandbox:${IMAGE_TAG}"

echo "==> Building fullsend (linux/amd64)..."
GOOS=linux GOARCH=amd64 go build -o /tmp/fullsend_build/fullsend "${REPO_ROOT}/cmd/fullsend/"
echo "    Built: /tmp/fullsend_build/fullsend"

echo "==> Creating tarball..."
tar czf /tmp/fullsend_build/fullsend_dev_linux_amd64.tar.gz -C /tmp/fullsend_build fullsend
echo "    Created: /tmp/fullsend_build/fullsend_dev_linux_amd64.tar.gz"

echo "==> Building base sandbox image..."
podman build -t "${BASE_IMAGE}" \
  -f "${REPO_ROOT}/images/sandbox/Containerfile" "${REPO_ROOT}/images/sandbox/"
echo "    Built: ${BASE_IMAGE}"

echo "==> Building experiment container image..."
podman build -t "${IMAGE_REPO}:${IMAGE_TAG}" \
  --build-arg "BASE_IMAGE=${BASE_IMAGE}" \
  -f "${EXPERIMENT_DIR}/Containerfile" "${EXPERIMENT_DIR}/"
echo "    Built: ${IMAGE_REPO}:${IMAGE_TAG}"

echo "==> Pushing container image..."
podman tag "${IMAGE_REPO}:${IMAGE_TAG}" "${IMAGE_REPO}:latest"
podman push "${IMAGE_REPO}:${IMAGE_TAG}"
podman push "${IMAGE_REPO}:latest"
echo "    Pushed: ${IMAGE_REPO}:${IMAGE_TAG} and :latest"

echo "==> Syncing .fullsend files to ${FULLSEND_REPO}..."
rsync -av --delete \
  --exclude='.git' \
  --exclude='.github' \
  "${FULLSEND_DIR}/" "${FULLSEND_REPO}/"

# Sync workflow file to .github/workflows/
mkdir -p "${FULLSEND_REPO}/.github/workflows"
cp "${EXPERIMENT_DIR}/workflow/hello-world.yml" "${FULLSEND_REPO}/.github/workflows/hello-world.yml"
echo "    Synced .fullsend files and workflow"

echo "==> Pushing experiment changes to .fullsend repo..."
cd "${FULLSEND_REPO}"

# Safety: verify the clone's remote matches the expected workflow repo.
ACTUAL_REMOTE=$(git remote get-url origin 2>/dev/null || true)
if [[ "$ACTUAL_REMOTE" != *"${WORKFLOW_REPO}"* ]]; then
  echo "ERROR: ${FULLSEND_REPO} remote (${ACTUAL_REMOTE}) does not match expected repo (${WORKFLOW_REPO})"
  exit 1
fi

git add -A
if git diff --cached --quiet; then
  echo "    No changes to push"
else
  git commit -m "Update hello-world experiment files"
  git push
  echo "    Pushed"
fi

echo "==> Uploading binary to release ${RELEASE_TAG}..."
gh release upload "${RELEASE_TAG}" \
  /tmp/fullsend_build/fullsend_dev_linux_amd64.tar.gz \
  --clobber --repo "${RELEASE_REPO}"
echo "    Uploaded"

echo "==> Triggering workflow ${WORKFLOW_FILE} (target: ${TARGET_REPO})..."
RUN_URL=$(gh workflow run "${WORKFLOW_FILE}" --repo "${WORKFLOW_REPO}" \
  -f target-repo="${TARGET_REPO}" 2>&1)
echo "    ${RUN_URL}"

# Give GitHub a moment to register the run, then fetch the URL.
sleep 3
RUN_ID=$(gh run list --repo "${WORKFLOW_REPO}" --workflow "${WORKFLOW_FILE}" --limit 1 --json databaseId --jq '.[0].databaseId')
echo ""
echo "==> Workflow run: https://github.com/${WORKFLOW_REPO}/actions/runs/${RUN_ID}"
echo "    Watch with: gh run watch ${RUN_ID} --repo ${WORKFLOW_REPO}"
