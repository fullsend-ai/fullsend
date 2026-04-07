#!/usr/bin/env bash
set -euo pipefail

# Triage agent applies labels and posts comments via gh CLI
# during its run. Post-script just handles any final label cleanup.
#
# If agent set ready-to-code, strip triage-phase labels.
# (The label state machine guard from Story 2 may handle this instead.)
