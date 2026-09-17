#!/usr/bin/env bash
# review-handoff.sh — Decide whether a ready-for-review labeled event is
# the automatic code-agent creation handoff (skip) or an explicit review
# request (dispatch).
#
# Provenance is the fullsend-auto-review-handoff label on the event
# snapshot, not the actor's username, a [bot] suffix, or a previous
# review of the same SHA. See docs/contributing/review-handoff-dedup.md.
#
# The GitHub Route job inlines equivalent logic via has_label after the
# maintainer patch is applied. That job sparse-checkouts only
# .fullsend/config.yaml, so it cannot source this file. Keep the
# inlined check and this helper in sync.

# shellcheck shell=bash

[[ -n "${REVIEW_HANDOFF_SH_LOADED:-}" ]] && return 0
REVIEW_HANDOFF_SH_LOADED=1

AUTO_REVIEW_HANDOFF_LABEL="fullsend-auto-review-handoff"

# should_skip_automatic_review_handoff <csv-labels>
# Returns 0 if the labeled event should not dispatch review.
# Returns 1 if review should dispatch.
# Matches reusable-dispatch.yml has_label: exact token match, no trim.
should_skip_automatic_review_handoff() {
  local csv="${1:-}"
  local needle="${AUTO_REVIEW_HANDOFF_LABEL}"
  local token
  local -a tokens
  IFS=',' read -ra tokens <<< "${csv}"
  for token in "${tokens[@]}"; do
    [[ "$token" == "$needle" ]] && return 0
  done
  return 1
}

# github_like_should_dispatch_review <action> <csv-labels>
# Mirrors per-repo GitHub Route after the maintainer patch:
# opened|synchronize|ready_for_review dispatch; labeled dispatches
# unless the automatic-handoff provenance label is present;
# slash-review dispatches. Authorization is out of scope.
github_like_should_dispatch_review() {
  local action="${1:-}"
  local csv_labels="${2:-}"
  case "${action}" in
    opened|synchronize|ready_for_review|slash-review)
      return 0
      ;;
    labeled)
      if should_skip_automatic_review_handoff "${csv_labels}"; then
        return 1
      fi
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}
