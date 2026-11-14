#!/usr/bin/env bash
# Helpers shared by the sandbox image publishing workflow and its local check.

sandbox_image_error() {
  # Keep workflow commands static. Render untrusted details as one shell-escaped
  # line on stderr so percent signs and control characters cannot add commands.
  printf '::error::Sandbox image validation failed\n'
  printf 'detail: %q\n' "$*" >&2
}

sandbox_image_is_digest() {
  [[ "${1-}" =~ ^sha256:[0-9a-f]{64}$ ]]
}

sandbox_image_require_digest() {
  local digest="${1-}"
  if sandbox_image_is_digest "$digest"; then
    return 0
  fi
  sandbox_image_error "invalid sha256 image digest: $digest"
  return 1
}
