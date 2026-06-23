#!/usr/bin/env bash
# after_each hook: delete the ephemeral GitHub repo.
#
# Required env (forward-propagated from setup-fixture.sh):
#   EPHEMERAL_REPO — org/name of the ephemeral repo
set -euo pipefail

EPHEMERAL_REPO="${EPHEMERAL_REPO:-}"
if [[ -z "$EPHEMERAL_REPO" ]]; then
  echo "WARNING: EPHEMERAL_REPO not set, skipping teardown" >&2
  exit 0
fi

if ! gh repo delete "$EPHEMERAL_REPO" --yes 2>&1; then
  echo "WARNING: failed to delete $EPHEMERAL_REPO — may need manual cleanup" >&2
fi
