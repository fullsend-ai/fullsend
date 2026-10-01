#!/usr/bin/env bash
# validate-output-schema.sh — Validate agent output against a JSON Schema.
#
# Generic script used by the harness validation_loop (ADR 0022).
# Works for any agent — the schema path is configured in the harness.
#
# Required env vars:
#   FULLSEND_OUTPUT_SCHEMA — path to the JSON Schema file
#
# Optional env vars:
#   FULLSEND_OUTPUT_FILE  — filename to validate (default: agent-result.json)
#
# The script looks for the output file in the iteration output directory.
# The working directory is the iteration dir (set by run.go).

set -euo pipefail

: "${FULLSEND_OUTPUT_SCHEMA:?FULLSEND_OUTPUT_SCHEMA must be set}"

# Find the output JSON file in this iteration's output directory.
OUTPUT_DIR="output"
if [[ ! -d "${OUTPUT_DIR}" ]]; then
  echo "FAIL: output directory not found"
  exit 1
fi

_output_file="${FULLSEND_OUTPUT_FILE:-agent-result.json}"
_output_file="$(basename "${_output_file}")"
RESULT_FILE="${OUTPUT_DIR}/${_output_file}"
if [[ ! -f "${RESULT_FILE}" ]]; then
  echo "FAIL: ${RESULT_FILE} not found"
  exit 1
fi
echo "Validating: ${RESULT_FILE} against ${FULLSEND_OUTPUT_SCHEMA}"

# Validate JSON is parseable.
if ! python3 -m json.tool "${RESULT_FILE}" > /dev/null 2>&1; then
  echo "FAIL: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

# Validate against schema using Python's jsonschema.
# jsonschema is required — fail hard if not installed.
if ! python3 -c "import jsonschema" 2>/dev/null; then
  echo "FAIL: python3 jsonschema package is not installed (required by ADR 0022)"
  exit 1
fi

if ! python3 -c "
import json, sys
from jsonschema import validate, ValidationError

def _trusted_property_names(node, found):
    # Collect every key declared under a 'properties' mapping anywhere in
    # the schema document. These are schema-authored literals, never
    # instance-controlled, so they are always safe to print.
    if isinstance(node, dict):
        props = node.get('properties')
        if isinstance(props, dict):
            found.update(k for k in props if isinstance(k, str))
        for v in node.values():
            _trusted_property_names(v, found)
    elif isinstance(node, list):
        for item in node:
            _trusted_property_names(item, found)

with open(sys.argv[1]) as f:
    instance = json.load(f)
with open(sys.argv[2]) as f:
    schema = json.load(f)

trusted_keys = set()
_trusted_property_names(schema, trusted_keys)

try:
    validate(instance=instance, schema=schema)
    print('PASS: output validated against schema')
except ValidationError as e:
    # e.message is built from the offending instance (e.g. an enum error
    # quotes the invalid value verbatim), and the instance is untrusted
    # agent/model output. Printing it would let a value like
    # '##[warning]forged' reach this step's log through the validation
    # loop. Report only trusted schema metadata, never the instance.
    #
    # e.path can itself carry instance-controlled strings: when a schema
    # uses additionalProperties/patternProperties instead of (or in
    # addition to) a fixed property list, the offending instance's own key
    # becomes a path component even though the schema never declared it.
    # Only print path components that are literal property names found
    # somewhere in the schema document (trusted_keys); array indices are
    # plain integers and always safe; anything else is masked.
    def _safe_component(p):
        if isinstance(p, bool):
            return str(p)
        if isinstance(p, int):
            return str(p)
        if isinstance(p, str) and p in trusted_keys:
            return p
        return '<dynamic-key>'
    path = '.'.join(_safe_component(p) for p in e.path) if e.path else '(root)'
    print(f'FAIL: schema validation failed: \"{path}\" failed its \"{e.validator}\" check')
    if e.validator in ('enum', 'const'):
        print(f'  allowed values: {e.validator_value!r}')
    if 'properties' in e.schema:
        allowed = ', '.join(sorted(e.schema['properties'].keys()))
        print(f'  allowed properties: {allowed}')
    sys.exit(1)
" "${RESULT_FILE}" "${FULLSEND_OUTPUT_SCHEMA}"; then
  exit 1
fi
