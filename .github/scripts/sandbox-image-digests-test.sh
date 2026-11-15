#!/usr/bin/env bash
# Focused check for digest validation and safe workflow diagnostics.
set -euo pipefail

source .github/scripts/sandbox-image-digests.sh

valid_digest="sha256:$(printf '%064d' 0 | tr '0' 'a')"
if ! sandbox_image_require_digest "$valid_digest" >/dev/null; then
  echo "valid digest was rejected" >&2
  exit 1
fi

for invalid_digest in \
  "" \
  "sha256:abc" \
  "sha256:$(printf '%064d' 0 | tr '0' 'A')" \
  "sha256:$(printf '%064d' 0 | tr '0' 'a')/extra"; do
  if sandbox_image_require_digest "$invalid_digest" >/dev/null 2>&1; then
    echo "invalid digest was accepted: $(printf '%q' "$invalid_digest")" >&2
    exit 1
  fi
done

injected_value=$'bad%value\r\n::error::injected-command'
if diagnostic=$(sandbox_image_require_digest "$injected_value" 2>&1); then
  echo "malformed injection value was accepted" >&2
  exit 1
fi
if [[ "$diagnostic" != $'::error::Sandbox image validation failed\n'* ]]; then
  echo "diagnostic did not use the static workflow annotation" >&2
  exit 1
fi
if [[ "$diagnostic" == *$'\r'* || "$diagnostic" == *$'\n::error::injected-command'* ]]; then
  echo "diagnostic contains raw control characters or an injected command" >&2
  exit 1
fi
if [[ "$diagnostic" != *'bad%value\r\n::error::injected-command'* ]]; then
  echo "diagnostic did not safely render the malformed value" >&2
  exit 1
fi

echo "sandbox image digest checks passed"
