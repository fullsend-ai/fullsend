#!/usr/bin/env bash
# grow-root-fs_test.sh — Tests for grow-root-fs.sh (root partition and Btrfs
# growth plus capacity verification) and for create-gcp-vm.sh wiring it in.
#
# Block-device tools (findmnt, lsblk, growpart, btrfs, df, id) are stubbed
# and sysfs is faked, so no real device is touched.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/grow-root-fs_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GROW="${SCRIPT_DIR}/grow-root-fs.sh"
CREATE_GCP="${SCRIPT_DIR}/create-gcp-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

STATE=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "${STATE}" "${SHIM_DIR}"' EXIT
export STATE
FAKE_SYSFS="${STATE}/sysfs"
CALL_LOG="${STATE}/calls.log"

GIB_SECTORS=2097152         # 1 GiB in 512-byte sectors
ROOT_START=4401152          # ~2.1 GiB of EFI/boot partitions before root
UNEXPANDED=16568320         # ~7.9 GiB root partition from the cloud image

# --- stubs ------------------------------------------------------------------
cat > "${SHIM_DIR}/id" <<'STUB'
#!/bin/sh
cat "${STATE}/uid"
STUB

cat > "${SHIM_DIR}/findmnt" <<'STUB'
#!/bin/sh
col="" target=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) col="$2"; shift ;;
    --target) target="$2"; shift ;;
  esac
  shift
done
case "${col}" in
  FSTYPE) cat "${STATE}/fstype" ;;
  SOURCE)
    case "${target}" in
      /) cat "${STATE}/source-root" ;;
      /home) cat "${STATE}/source-home" ;;
      /var) cat "${STATE}/source-var" ;;
    esac
    ;;
esac
STUB

cat > "${SHIM_DIR}/lsblk" <<'STUB'
#!/bin/sh
col="" dev=""
while [ $# -gt 0 ]; do
  case "$1" in
    -ndo) col="$2"; shift ;;
    *) dev="$1" ;;
  esac
  shift
done
cat "${STATE}/lsblk-${col}-$(basename "${dev}")" 2>/dev/null || true
STUB

cat > "${SHIM_DIR}/growpart" <<'STUB'
#!/bin/sh
echo "growpart $*" >> "${STATE}/calls.log"
sysfs="${STATE}/sysfs"
case "$(cat "${STATE}/growpart-mode")" in
  grow)
    disk=$(cat "${sysfs}/sda/size")
    start=$(cat "${sysfs}/sda4/start")
    echo $(( disk - start - 34 )) > "${sysfs}/sda4/size"
    echo "CHANGED: partition=4 start=${start}"
    exit 0
    ;;
  nochange)
    echo "NOCHANGE: partition 4 is size $(cat "${sysfs}/sda4/size"). it cannot be grown"
    exit 1
    ;;
  silent)
    exit 0
    ;;
  *)
    echo "FAILED: failed to resize"
    exit 2
    ;;
esac
STUB

cat > "${SHIM_DIR}/btrfs" <<'STUB'
#!/bin/sh
case "$*" in
  "filesystem show --raw /")
    echo "Label: 'fedora'  uuid: 00000000-0000-0000-0000-000000000000"
    echo "	Total devices $(cat "${STATE}/btrfs-total" 2>/dev/null || wc -l < "${STATE}/btrfs-devs") FS bytes used 3459923968"
    [ -f "${STATE}/btrfs-extra-line" ] && cat "${STATE}/btrfs-extra-line"
    while read -r id size path; do
      echo "	devid    ${id} size ${size} used 5402263552 path ${path}"
    done < "${STATE}/btrfs-devs"
    ;;
  "filesystem resize "*":max /")
    echo "btrfs $*" >> "${STATE}/calls.log"
    id="${3%%:*}"
    [ "${id}" = "$(awk '{ print $1; exit }' "${STATE}/btrfs-devs")" ] \
      || { echo "ERROR: invalid device id ${id}" >&2; exit 1; }
    [ "$(cat "${STATE}/btrfs-resize-mode")" = "ok" ] || { echo "ERROR: unable to resize" >&2; exit 1; }
    part=$(cat "${STATE}/sysfs/sda4/size")
    path=$(awk '{ print $3; exit }' "${STATE}/btrfs-devs")
    echo "${id} $(( part * 512 )) ${path}" > "${STATE}/btrfs-devs"
    echo "Resize device id ${id} (${path}) from old to max"
    ;;
  *) exit 1 ;;
esac
STUB

printf '#!/bin/sh\necho "df $*"\n' > "${SHIM_DIR}/df"
printf '#!/bin/sh\necho "dnf $*" >> "${STATE}/calls.log"\nexit 1\n' > "${SHIM_DIR}/dnf"
chmod +x "${SHIM_DIR}"/*

# Fedora Cloud layout: root, home, and var subvolumes on /dev/sda4.
reset_fixture() {
  local disk_gib="$1" root_sectors="$2"
  rm -rf "${FAKE_SYSFS}"
  mkdir -p "${FAKE_SYSFS}/sda" "${FAKE_SYSFS}/sda4"
  echo $(( disk_gib * GIB_SECTORS )) > "${FAKE_SYSFS}/sda/size"
  echo "${ROOT_START}" > "${FAKE_SYSFS}/sda4/start"
  echo "${root_sectors}" > "${FAKE_SYSFS}/sda4/size"
  echo 4 > "${FAKE_SYSFS}/sda4/partition"
  echo 0 > "${STATE}/uid"
  echo btrfs > "${STATE}/fstype"
  echo '/dev/sda4[/root]' > "${STATE}/source-root"
  echo '/dev/sda4[/home]' > "${STATE}/source-home"
  echo '/dev/sda4[/var]' > "${STATE}/source-var"
  echo part > "${STATE}/lsblk-TYPE-sda4"
  echo sda > "${STATE}/lsblk-PKNAME-sda4"
  echo disk > "${STATE}/lsblk-TYPE-sda"
  echo "1 $(( root_sectors * 512 )) /dev/sda4" > "${STATE}/btrfs-devs"
  rm -f "${STATE}/btrfs-total" "${STATE}/btrfs-extra-line"
  echo grow > "${STATE}/growpart-mode"
  echo ok > "${STATE}/btrfs-resize-mode"
  : > "${CALL_LOG}"
}

run_grow() {
  RUN_RC=0
  RUN_OUT=$(
    PATH="${SHIM_DIR}:${PATH}" GROW_ROOT_SYSFS_BLOCK="${FAKE_SYSFS}" \
      MIN_DISK_GIB="${1:-0}" bash "${GROW}" 2>&1
  ) && RUN_RC=0 || RUN_RC=$?
}

called() { grep -Fq "$1" "${CALL_LOG}"; }
out_has() { printf '%s' "${RUN_OUT}" | grep -Fq "$1"; }
calls() { tr '\n' '|' < "${CALL_LOG}"; }

echo "== static contracts =="
if grep -Fq -- '--boot-disk-size="${BOOT_DISK_GB}GB"' "${CREATE_GCP}" \
  && grep -Eq '^BOOT_DISK_GB=30$' "${CREATE_GCP}"; then
  pass "create-gcp-vm.sh requests a 30 GiB boot disk"
else
  fail "create-gcp-vm.sh does not request a 30 GiB boot disk"
fi
if grep -Fq 'MIN_DISK_GIB=${BOOT_DISK_GB} bash -s' "${CREATE_GCP}" \
  && grep -Fq 'grow-root-fs.sh' "${CREATE_GCP}"; then
  pass "create-gcp-vm.sh runs grow-root-fs.sh with the boot disk size as the minimum"
else
  fail "create-gcp-vm.sh does not run grow-root-fs.sh with MIN_DISK_GIB"
fi
if [ "$(tail -n 1 "${GROW}")" = 'main "$@" </dev/null' ]; then
  pass "grow-root-fs.sh only runs once fully received (main call is last)"
else
  fail "grow-root-fs.sh must end with the main call so a truncated stream runs nothing"
fi

echo "== larger disk, unexpanded image partition =="
reset_fixture 30 "${UNEXPANDED}"
run_grow 30
if [ "${RUN_RC}" -ne 0 ]; then
  fail "unexpanded 30 GiB disk should grow and verify (rc=${RUN_RC}): ${RUN_OUT}"
elif ! called 'growpart /dev/sda 4'; then
  fail "growpart was not run on /dev/sda partition 4: $(calls)"
elif ! called 'btrfs filesystem resize 1:max /'; then
  fail "btrfs resize was not run: $(calls)"
elif ! out_has 'OK: root filesystem spans the disk'; then
  fail "missing success line: ${RUN_OUT}"
elif called 'dnf'; then
  fail "dnf ran although growpart and btrfs were present: $(calls)"
else
  pass "unexpanded root on a 30 GiB disk grows partition and Btrfs, then verifies"
fi

echo "== current 20 GiB disk, unexpanded (in-place repair) =="
reset_fixture 20 "${UNEXPANDED}"
run_grow
if [ "${RUN_RC}" -eq 0 ] && called 'growpart /dev/sda 4' && out_has 'OK: root filesystem spans the disk'; then
  pass "unused space on a 20 GiB disk is reclaimed without a disk-size minimum"
else
  fail "20 GiB repair failed (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== already expanded layout (idempotent) =="
reset_fixture 30 $(( 30 * GIB_SECTORS - ROOT_START - 34 ))
echo nochange > "${STATE}/growpart-mode"
run_grow 30
if [ "${RUN_RC}" -ne 0 ]; then
  fail "already-expanded layout should succeed (rc=${RUN_RC}): ${RUN_OUT}"
elif ! out_has 'partition already fills the available space'; then
  fail "NOCHANGE was not treated as already expanded: ${RUN_OUT}"
elif ! out_has 'OK: root filesystem spans the disk'; then
  fail "already-expanded layout did not verify: ${RUN_OUT}"
else
  pass "already-expanded layout is a verified no-op"
fi

echo "== expansion failures =="
reset_fixture 30 "${UNEXPANDED}"
echo fail > "${STATE}/growpart-mode"
run_grow 30
if [ "${RUN_RC}" -eq 0 ]; then
  fail "growpart failure should fail the run: ${RUN_OUT}"
elif called 'btrfs filesystem resize'; then
  fail "btrfs resize ran after growpart failed: $(calls)"
elif out_has 'ERROR: growpart /dev/sda 4 failed (exit 2)'; then
  pass "growpart failure stops with a clear error"
else
  fail "growpart failure message mismatch: ${RUN_OUT}"
fi

reset_fixture 30 "${UNEXPANDED}"
echo fail > "${STATE}/btrfs-resize-mode"
run_grow 30
if [ "${RUN_RC}" -ne 0 ] && out_has 'ERROR: btrfs filesystem resize 1:max / failed'; then
  pass "btrfs resize failure stops with a clear error"
else
  fail "btrfs resize failure not reported (rc=${RUN_RC}): ${RUN_OUT}"
fi

reset_fixture 30 "${UNEXPANDED}"
echo silent > "${STATE}/growpart-mode"
run_grow 30
if [ "${RUN_RC}" -ne 0 ] && out_has 'partition growth did not complete'; then
  pass "root still at the image's ~8 GiB after growth fails verification"
else
  fail "unchanged ~8 GiB root passed verification (rc=${RUN_RC}): ${RUN_OUT}"
fi

reset_fixture 20 $(( 20 * GIB_SECTORS - ROOT_START - 34 ))
echo nochange > "${STATE}/growpart-mode"
run_grow 30
if [ "${RUN_RC}" -ne 0 ] && out_has 'expected at least 30 GiB'; then
  pass "disk smaller than MIN_DISK_GIB fails verification"
else
  fail "20 GiB disk passed a 30 GiB minimum (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== single device with a non-1 devid (e.g. after a device replace) =="
reset_fixture 30 "${UNEXPANDED}"
echo "3 $(( UNEXPANDED * 512 )) /dev/sda4" > "${STATE}/btrfs-devs"
run_grow 30
if [ "${RUN_RC}" -ne 0 ]; then
  fail "non-1 devid should grow and verify (rc=${RUN_RC}): ${RUN_OUT}"
elif ! called 'btrfs filesystem resize 3:max /'; then
  fail "btrfs resize did not target devid 3: $(calls)"
else
  pass "btrfs resize targets the filesystem's sole devid"
fi

echo "== create-gcp-vm.sh requires the completion marker =="
GROW_FN=$(sed -n '/^GROW_ROOT_FS_OK_MARKER=/p;/^grow_root_fs() {/,/^}/p' "${CREATE_GCP}")
GCLOUD_STUB_DIR=$(mktemp -d)
trap 'rm -rf "${STATE}" "${SHIM_DIR}" "${GCLOUD_STUB_DIR}"' EXIT
# gcloud stub: runs the streamed script (stdin) the way `bash -s` would, or a
# truncated prefix of it (all but the final `main` call) when TRUNCATE=1.
cat > "${GCLOUD_STUB_DIR}/gcloud" <<'STUB'
#!/bin/sh
if [ "${TRUNCATE:-0}" = 1 ]; then
  sed '$d' | bash -s
else
  cat >/dev/null
  echo "==> OK: root filesystem spans the disk"
fi
exit "${STUB_RC:-0}"
STUB
chmod +x "${GCLOUD_STUB_DIR}/gcloud"
run_grow_fn() {
  RUN_RC=0
  RUN_OUT=$(
    PATH="${GCLOUD_STUB_DIR}:${PATH}" TRUNCATE="${1:-0}" STUB_RC="${2:-0}" \
      SCRIPT_DIR="${SCRIPT_DIR}" vm_name=vm GCP_PROJECT=p GCP_ZONE=z GCE_SSH_FLAGS=() \
      BOOT_DISK_GB=30 bash -c "${GROW_FN}"$'\n''grow_root_fs' 2>&1
  ) && RUN_RC=0 || RUN_RC=$?
}
run_grow_fn 0 0
if [ "${RUN_RC}" -eq 0 ]; then
  pass "complete run with the marker succeeds"
else
  fail "run with the marker should succeed (rc=${RUN_RC}): ${RUN_OUT}"
fi
run_grow_fn 1 0
if [ "${RUN_RC}" -ne 0 ] && printf '%s' "${RUN_OUT}" | grep -Fq 'without its completion marker'; then
  pass "truncated-but-valid stream (exit 0, no marker) fails"
else
  fail "truncated stream passed as success (rc=${RUN_RC}): ${RUN_OUT}"
fi
run_grow_fn 0 7
if [ "${RUN_RC}" -eq 7 ]; then
  pass "ssh failure status is propagated"
else
  fail "ssh failure not propagated (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== unsupported layouts fail before modifying any device =="
check_unsupported() {
  local label="$1" expect="$2"
  run_grow 30
  if [ "${RUN_RC}" -eq 0 ]; then
    fail "${label}: should fail: ${RUN_OUT}"
  elif called 'growpart' || called 'btrfs filesystem resize'; then
    fail "${label}: modified a device: $(calls)"
  elif ! out_has "${expect}"; then
    fail "${label}: message mismatch (want '${expect}'): ${RUN_OUT}"
  else
    pass "${label}"
  fi
}

reset_fixture 30 "${UNEXPANDED}"
echo xfs > "${STATE}/fstype"
check_unsupported "non-Btrfs root is rejected" "unsupported root filesystem 'xfs'"

reset_fixture 30 "${UNEXPANDED}"
echo '/dev/sdb1' > "${STATE}/source-var"
check_unsupported "/var on another device is rejected" "/var is on /dev/sdb1"

reset_fixture 30 "${UNEXPANDED}"
echo lvm > "${STATE}/lsblk-TYPE-sda4"
check_unsupported "root that is not a partition is rejected" "is not a partition (type: lvm)"

reset_fixture 30 "${UNEXPANDED}"
echo '2 8482979840 /dev/sdb' >> "${STATE}/btrfs-devs"
check_unsupported "multi-device Btrfs is rejected" "spans 2 devices"

reset_fixture 30 "${UNEXPANDED}"
echo 2 > "${STATE}/btrfs-total"
check_unsupported "degraded multi-device Btrfs (one visible devid row) is rejected" "declares 2 devices"

reset_fixture 30 "${UNEXPANDED}"
echo 2 > "${STATE}/btrfs-total"
echo "	*** Some devices missing" > "${STATE}/btrfs-extra-line"
check_unsupported "Btrfs reporting missing devices is rejected" "declares 2 devices"

reset_fixture 30 "${UNEXPANDED}"
echo unknown > "${STATE}/btrfs-total"
check_unsupported "unparseable Btrfs device count fails closed" "cannot determine the root Btrfs device count"

reset_fixture 30 "${UNEXPANDED}"
echo "	*** Some devices missing" > "${STATE}/btrfs-extra-line"
check_unsupported "Btrfs reporting missing devices with a count of 1 is rejected" "reports missing devices"

reset_fixture 30 "${UNEXPANDED}"
echo 1000 > "${STATE}/uid"
check_unsupported "non-root invocation is rejected" "must run as root"

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
