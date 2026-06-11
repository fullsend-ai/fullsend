#!/bin/bash

# get-adr-status.sh - Extract and validate status from ADR markdown files
#
# Adapted from https://github.com/konflux-ci/architecture/blob/main/hack/util/get-adr-status.sh
#
# Usage: ./get-adr-status.sh <adr-file>
#
# Exit codes:
#   0 - Success, valid status found and printed
#   1 - Error: file not found or not readable
#   2 - Error: no ## Status section found
#   3 - Error: status section is empty
#   4 - Error: invalid status value

set -euo pipefail

VALID_STATUSES=("Accepted" "Deprecated" "Superseded")

error() {
    echo "ERROR: $1" >&2
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "Usage: $0 <adr-file>"
    echo ""
    echo "Extract and validate the status from an ADR markdown file."
    echo "Valid statuses: ${VALID_STATUSES[*]}"
    exit 0
fi

if [[ $# -ne 1 ]]; then
    error "Exactly one argument (ADR file path) is required"
    exit 1
fi

adr_file="$1"

if [[ ! -f "$adr_file" ]]; then
    error "File '$adr_file' not found"
    exit 1
fi

if [[ ! -r "$adr_file" ]]; then
    error "File '$adr_file' is not readable"
    exit 1
fi

# Extract the first non-empty line from the ## Status section
status_line=$(awk '
    /^## Status$/ {
        in_status = 1
        next
    }
    in_status && /^##/ {
        in_status = 0
        next
    }
    in_status && /^[[:space:]]*$/ {
        next
    }
    in_status {
        print $0
        exit
    }
' "$adr_file")

if [[ -z "$status_line" ]]; then
    error "No ## Status section found in '$adr_file'"
    exit 2
fi

# Extract the first word, stripping any markdown formatting
status=$(echo "$status_line" | sed 's/[*_`]//g' | awk '{print $1}')

if [[ -z "$status" ]]; then
    error "Status section is empty or contains only formatting in '$adr_file'"
    exit 3
fi

# Validate against known statuses
valid=false
for s in "${VALID_STATUSES[@]}"; do
    if [[ "$status" == "$s" ]]; then
        valid=true
        break
    fi
done

if [[ "$valid" != "true" ]]; then
    error "Invalid status '$status' in '$adr_file'. Valid statuses: ${VALID_STATUSES[*]}"
    exit 4
fi

echo "$status"
