#!/usr/bin/env bash
# redact-behaviour-artifacts.sh — Strip job secrets from behaviour debug artifacts
# before upload.
#
# Invoked from .github/workflows/e2e.yml after a behaviour job failure. The workflow
# checks out this script from the base branch (not PR head) and runs it in a clean
# environment so PR-head code cannot tamper with the redaction toolchain.
#
# Handles plain text (logs, JSON, JSONL), nested archives (zip, tar.gz, gzip), and
# replaces opaque or encrypted blobs that cannot be scanned safely.
#
# Prior art: fullsend-ai/agents scripts/lib/post-failure-report.lib.sh

set -euo pipefail
shopt -s inherit_errexit

export PATH=/usr/bin:/bin
export LC_ALL=C
unset LD_PRELOAD BASH_ENV

ARTIFACT_DIR="${ARTIFACT_DIR:?ARTIFACT_DIR is required}"

# SYNC-WITH: perFileLimit/totalExtractLimit in pkg/behaviourtest/drivers/ci/githubactions/githubactions.go
readonly ARCHIVE_PER_FILE_LIMIT=$((10 << 20))
readonly ARCHIVE_TOTAL_LIMIT=$((100 << 20))
readonly LITERAL_SECRET_MIN_LINE_LEN=8

if [ ! -d "${ARTIFACT_DIR}" ]; then
  echo "::notice::No behaviour artifact dir at ${ARTIFACT_DIR} — skipping redaction"
  exit 0
fi

ARTIFACT_DIR="$(cd "${ARTIFACT_DIR}" && pwd)"

_read_text_file() {
  local file="$1"
  /usr/bin/python3 -c "
import pathlib
import sys

try:
    sys.stdout.write(pathlib.Path(sys.argv[1]).read_text())
except OSError:
    sys.exit(1)
" "${file}"
}

_redact_multiline_pem() {
  /usr/bin/awk '
    function is_pem_begin(line) {
      return tolower(line) ~ /-----begin .*private key.*-----/
    }
    function is_pem_end(line) {
      return tolower(line) ~ /-----end .*private key.*-----/
    }
    is_pem_begin($0) {
      print "[REDACTED PRIVATE KEY]"
      if (!is_pem_end($0)) {
        in_pem = 1
      }
      next
    }
    is_pem_end($0) {
      in_pem = 0
      next
    }
    in_pem { next }
    { print }
  '
}

_redact_literal_token() {
  local detail="$1"
  local token="$2"

  if [ -z "${token}" ]; then
    printf '%s' "${detail}"
    return 0
  fi

  REDACT_LITERAL_TOKEN="${token}" REDACT_LITERAL_REPL="[REDACTED]" /usr/bin/awk '
    {
      token = ENVIRON["REDACT_LITERAL_TOKEN"]
      repl = ENVIRON["REDACT_LITERAL_REPL"]
      s = $0
      while ((i = index(s, token)) > 0) {
        s = substr(s, 1, i - 1) repl substr(s, i + length(token))
      }
      print s
    }
  ' <<< "${detail}"
}

_redact_patterns() {
  /usr/bin/sed -E \
    -e 's/gh[a-z]_[A-Za-z0-9_]{20,}/[REDACTED]/g' \
    -e 's/github_pat_[A-Za-z0-9_]+/[REDACTED]/g' \
    -e 's/x-access-token:[^@[:space:]]+/x-access-token:[REDACTED]/g' \
    -e 's/(Bearer|token)[[:space:]]+[^[:space:]]+/\1 [REDACTED]/gI' \
    -e 's/ya29\.[A-Za-z0-9._-]+/[REDACTED]/g'
}

_redact_literal_secrets() {
  local detail="$1"
  local name value line

  local secret_names=(
    TEST_FULLSEND_PEM
    TEST_TRIAGE_PEM
    TEST_CODER_PEM
    TEST_REVIEW_PEM
    TEST_RETRO_PEM
    TEST_PRIORITIZE_PEM
    CLOUDFLARE_ACCOUNT_ID
    CLOUDFLARE_API_TOKEN
    TEST_ACTOR_WRITE_PAT
    TEST_ACTOR_TRIAGE_PAT
    TEST_ACTOR_OUTSIDER_PAT
    E2E_GCP_PROJECT_ID
    E2E_GCP_WIF_PROVIDER
    E2E_GCP_SERVICE_ACCOUNT
  )

  for name in "${secret_names[@]}"; do
    value="${!name:-}"
    if [ -z "${value}" ]; then
      continue
    fi
    detail="$(_redact_literal_token "${detail}" "${value}")"
    if [[ "${value}" == *$'\n'* ]]; then
      while IFS= read -r line || [ -n "${line}" ]; do
        if [ "${#line}" -ge "${LITERAL_SECRET_MIN_LINE_LEN}" ]; then
          detail="$(_redact_literal_token "${detail}" "${line}")"
        fi
      done <<< "${value}"
    fi
  done

  printf '%s' "${detail}"
}

_redact_text_content() {
  local content="$1"
  local redacted
  redacted="$(printf '%s\n' "${content}" | _redact_multiline_pem | _redact_patterns)"
  _redact_literal_secrets "${redacted}"
}

_contains_nul_bytes() {
  local file="$1"
  /usr/bin/python3 -c "import sys
try:
    data = open(sys.argv[1], 'rb').read()
except OSError:
    sys.exit(0)
sys.exit(0 if b'\\x00' in data else 1)" "${file}" 2>/dev/null
}

_sanitize_log_path() {
  local value="$1"
  value="${value//$'\n'/}"
  value="${value//$'\r'/}"
  value="${value//::/}"
  value="${value//%0A/}"
  value="${value//%0a/}"
  value="${value//%0D/}"
  value="${value//%0d/}"
  value="${value//%25/}"
  value="$(printf '%s' "${value}" | /usr/bin/sed $'s/\x1b\\[[0-9;]*[A-Za-z]//g')"
  printf '%s' "${value}"
}

_stub_opaque_file() {
  local file="$1"
  local reason="${2:-could not be scanned for job secrets}"
  local tmp safe_file
  tmp="$(mktemp)"
  printf '%s\n' "[REDACTED OPAQUE CONTENT]" "This file was removed from behaviour debug artifacts because it ${reason}." >"${tmp}"
  mv "${tmp}" "${file}"
  safe_file="$(_sanitize_log_path "${file}")"
  echo "::warning::Replaced opaque artifact file: ${safe_file}"
}

_replace_symlink_with_stub() {
  local file="$1"
  local reason="${2:-was a symlink and could not be scanned for job secrets}"
  local tmp safe_file
  rm -f "${file}"
  tmp="$(mktemp)"
  printf '%s\n' "[REDACTED OPAQUE CONTENT]" "This file was removed from behaviour debug artifacts because it ${reason}." >"${tmp}"
  mv "${tmp}" "${file}"
  safe_file="$(_sanitize_log_path "${file}")"
  echo "::warning::Replaced symlink artifact: ${safe_file}"
}

_remove_symlinks() {
  local dir="$1"
  local link

  while IFS= read -r -d '' link; do
    _replace_symlink_with_stub "${link}"
  done < <(/usr/bin/find "${dir}" -type l -print0)
}

_safe_extract_archive() {
  local archive="$1"
  local dest="$2"
  local format="$3"

  /usr/bin/python3 - "${archive}" "${dest}" "${format}" "${ARCHIVE_PER_FILE_LIMIT}" "${ARCHIVE_TOTAL_LIMIT}" <<'PY'
import pathlib
import sys
import tarfile
import zipfile

archive, dest, fmt = sys.argv[1:4]
per_file = int(sys.argv[4])
total_limit = int(sys.argv[5])
root = pathlib.Path(dest)
root.mkdir(parents=True, exist_ok=True)
total = 0


def safe_child(name: str) -> pathlib.Path:
    candidate = (root / name).resolve()
    root_resolved = root.resolve()
    if candidate != root_resolved and root_resolved not in candidate.parents:
        raise ValueError(f"path traversal entry: {name}")
    return candidate


if fmt == "zip":
    with zipfile.ZipFile(archive) as zf:
        for info in zf.infolist():
            mode = (info.external_attr >> 16) & 0o170000
            if mode == 0o120000:
                raise ValueError(f"symlink entry: {info.filename}")
            if info.is_dir():
                safe_child(info.filename).mkdir(parents=True, exist_ok=True)
                continue
            if info.file_size > per_file:
                raise ValueError(f"entry exceeds per-file limit: {info.filename}")
            target = safe_child(info.filename)
            target.parent.mkdir(parents=True, exist_ok=True)
            with zf.open(info) as src, open(target, "wb") as dst:
                chunk = src.read(per_file + 1)
                if len(chunk) > per_file:
                    raise ValueError(f"entry exceeds per-file limit: {info.filename}")
                total += len(chunk)
                if total > total_limit:
                    raise ValueError("archive exceeds total extraction limit")
                dst.write(chunk)
elif fmt == "tar.gz":
    with tarfile.open(archive, "r:gz") as tf:
        for member in tf.getmembers():
            if member.issym() or member.islnk():
                raise ValueError(f"link entry: {member.name}")
            if member.isdir():
                safe_child(member.name).mkdir(parents=True, exist_ok=True)
                continue
            if member.size > per_file:
                raise ValueError(f"entry exceeds per-file limit: {member.name}")
            target = safe_child(member.name)
            target.parent.mkdir(parents=True, exist_ok=True)
            extracted = tf.extractfile(member)
            if extracted is None:
                continue
            data = extracted.read(per_file + 1)
            if len(data) > per_file:
                raise ValueError(f"entry exceeds per-file limit: {member.name}")
            total += len(data)
            if total > total_limit:
                raise ValueError("archive exceeds total extraction limit")
            target.write_bytes(data)
else:
    raise ValueError(f"unsupported archive format: {fmt}")
PY
}

_decompress_gzip_capped() {
  local src="$1"
  local dst="$2"
  local limit="$3"

  /usr/bin/python3 - "${src}" "${dst}" "${limit}" <<'PY'
import gzip
import sys

src, dst, limit = sys.argv[1:4]
limit = int(limit)
with gzip.open(src, "rb") as handle:
    data = handle.read(limit + 1)
if len(data) > limit:
    raise ValueError("gzip exceeds safe extraction limit")
open(dst, "wb").write(data)
PY
}

_file_kind() {
  local file="$1"
  local base lower mime

  base="$(basename "${file}")"
  lower="${base,,}"

  case "${lower}" in
    *.tar.gz | *.tgz)
      echo archive-tar-gz
      return 0
      ;;
    *.gz)
      echo archive-gzip
      return 0
      ;;
    *.zip)
      echo archive-zip
      return 0
      ;;
    *.gpg | *.age | *.enc)
      echo encrypted
      return 0
      ;;
  esac

  if _contains_nul_bytes "${file}"; then
    echo binary
    return 0
  fi

  case "${lower}" in
    *.json | *.jsonl | *.log | *.txt | *.md | *.yaml | *.yml | *.xml | *.feature | *.out | *.err)
      echo text
      return 0
      ;;
  esac

  mime="$(/usr/bin/file --brief --mime-type "${file}" 2>/dev/null || true)"
  case "${mime}" in
    text/* | application/json | application/xml | application/x-empty | inode/x-empty)
      echo text
      return 0
      ;;
    application/gzip | application/x-gzip)
      echo archive-gzip
      return 0
      ;;
    application/zip)
      echo archive-zip
      return 0
      ;;
    application/x-tar*)
      echo archive-tar-gz
      return 0
      ;;
    application/pgp-encrypted)
      echo encrypted
      return 0
      ;;
    application/pgp-keys)
      echo text
      return 0
      ;;
    image/* | video/* | audio/* | application/pdf)
      echo media
      return 0
      ;;
  esac

  echo text
}

_redact_zip_file() {
  local file="$1"
  local tmpdir workdir abs_file

  abs_file="$(cd "$(dirname "${file}")" && pwd)/$(basename "${file}")"
  tmpdir="$(mktemp -d)"
  workdir="${tmpdir}/contents"
  mkdir -p "${workdir}"

  if ! _safe_extract_archive "${abs_file}" "${workdir}" zip; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${abs_file}" "could not be safely extracted as zip"
    return 0
  fi

  _remove_symlinks "${workdir}"
  _redact_tree "${workdir}"
  rm -f "${abs_file}"
  (cd "${workdir}" && /usr/bin/zip -qr "${abs_file}" .)
  rm -rf "${tmpdir}"
}

_redact_tar_gz_file() {
  local file="$1"
  local tmpdir workdir

  tmpdir="$(mktemp -d)"
  workdir="${tmpdir}/contents"
  mkdir -p "${workdir}"

  if ! _safe_extract_archive "${file}" "${workdir}" tar.gz; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${file}" "could not be safely extracted as tar.gz"
    return 0
  fi

  _remove_symlinks "${workdir}"
  _redact_tree "${workdir}"
  rm -f "${file}"
  /usr/bin/tar -czf "${file}" -C "${workdir}" .
  rm -rf "${tmpdir}"
}

_redact_gzip_file() {
  local file="$1"
  local tmpdir content redacted

  tmpdir="$(mktemp -d)"
  if ! _decompress_gzip_capped "${file}" "${tmpdir}/content" "${ARCHIVE_PER_FILE_LIMIT}" 2>/dev/null; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${file}" "could not be safely decompressed as gzip"
    return 0
  fi

  if _contains_nul_bytes "${tmpdir}/content"; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${file}" "contained binary content that could not be scanned for job secrets"
    return 0
  fi

  if ! content="$(_read_text_file "${tmpdir}/content")"; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${file}" "could not be read after gzip decompression"
    return 0
  fi

  if ! redacted="$(_redact_text_content "${content}")"; then
    rm -rf "${tmpdir}"
    _stub_opaque_file "${file}" "could not be redacted after gzip decompression"
    return 0
  fi

  printf '%s' "${redacted}" | /usr/bin/gzip -c >"${file}"
  rm -rf "${tmpdir}"
}

_redact_text_file() {
  local file="$1"
  local content redacted tmp

  if ! content="$(_read_text_file "${file}")"; then
    _stub_opaque_file "${file}" "could not be read"
    return 0
  fi

  if ! redacted="$(_redact_text_content "${content}")"; then
    _stub_opaque_file "${file}" "could not be redacted"
    return 0
  fi

  tmp="$(mktemp)"
  printf '%s' "${redacted}" >"${tmp}"
  mv "${tmp}" "${file}"
}

_redact_tree() {
  local dir="$1"
  local file

  _remove_symlinks "${dir}"
  while IFS= read -r -d '' file; do
    _redact_path "${file}"
  done < <(/usr/bin/find "${dir}" -type f -print0)
}

_redact_opaque_file() {
  local file="$1"
  local reason="$2"
  _stub_opaque_file "${file}" "${reason}"
}

_redact_path() {
  local file="$1"
  local kind

  kind="$(_file_kind "${file}")"
  case "${kind}" in
    text)
      _redact_text_file "${file}"
      ;;
    archive-zip)
      _redact_zip_file "${file}"
      ;;
    archive-tar-gz)
      _redact_tar_gz_file "${file}"
      ;;
    archive-gzip)
      _redact_gzip_file "${file}"
      ;;
    media | binary)
      _redact_opaque_file "${file}" "is binary or media and could not be scanned for job secrets"
      ;;
    encrypted)
      _redact_opaque_file "${file}" "is encrypted and could not be scanned for job secrets"
      ;;
  esac
}

_remove_symlinks "${ARTIFACT_DIR}"

file_count=0
while IFS= read -r -d '' file; do
  _redact_path "${file}"
  file_count=$((file_count + 1))
done < <(/usr/bin/find "${ARTIFACT_DIR}" -type f -print0)

echo "::notice::Redacted secrets in ${file_count} behaviour artifact file(s)"
