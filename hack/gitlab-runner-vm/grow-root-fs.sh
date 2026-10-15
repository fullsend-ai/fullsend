#!/usr/bin/env bash
#
# grow-root-fs.sh — Grow a runner VM's root partition and Btrfs filesystem
# to fill its disk, then verify disk, partition, and filesystem capacity.
#
# Fedora Cloud images on GCE were observed booting with an ~8 GiB root
# Btrfs partition on a 20 GiB disk (#8163): the image's own first-boot
# growth did not run, and nothing checked. create-gcp-vm.sh runs this script
# before registering the runner; it is also the repair path for existing
# runners (see README.md "GCE boot disk size and repair").
#
# Supported layout (Fedora Cloud): / is a Btrfs subvolume on a single-device
# filesystem that lives on a partition of a whole disk; /home and /var, when
# separately mounted, are subvolumes of that same filesystem, so growing it
# grows all three. Anything else fails before any device is modified.
#
# Idempotent: when the partition and filesystem already fill the disk,
# growpart reports NOCHANGE, `btrfs filesystem resize <devid>:max` is a no-op, and
# verification passes. Safe on a running runner — growth is online and does
# not touch registration, images, or workspace data.
#
# Usage (on the VM, as root):
#   sudo env MIN_DISK_GIB=30 bash grow-root-fs.sh
#
# From a workstation (script streamed over SSH):
#   gcloud compute ssh VM ... -- "sudo env MIN_DISK_GIB=30 bash -s" < grow-root-fs.sh
#
# Environment variables:
#   MIN_DISK_GIB — fail verification unless the root disk is at least this
#                  many GiB (default: 0, no disk-size check)
#
# Exit status: 0 when the root filesystem verifiably spans the disk;
# non-zero with an ERROR line otherwise.
set -euo pipefail

SYSFS_BLOCK="${GROW_ROOT_SYSFS_BLOCK:-/sys/class/block}"
MIN_DISK_GIB="${MIN_DISK_GIB:-0}"
# Tolerated gap between partition end and disk end, and between partition
# size and Btrfs device size. The GPT backup header and alignment take well
# under this; an unexpanded image leaves gigabytes.
SLACK_BYTES=$(( 64 * 1024 * 1024 ))

log() { echo "==> $*"; }
die() {
  echo "ERROR: $*" >&2
  exit 1
}

gib() {
  awk -v b="$1" 'BEGIN { printf "%.1f GiB", b / 1073741824 }'
}

# sysfs reports size/start in 512-byte sectors regardless of the device's
# logical block size.
sysfs_bytes() {
  local v
  v=$(cat "${SYSFS_BLOCK}/$1/$2" 2>/dev/null) || die "cannot read ${SYSFS_BLOCK}/$1/$2"
  [[ "${v}" =~ ^[0-9]+$ ]] || die "unexpected value in ${SYSFS_BLOCK}/$1/$2: ${v}"
  echo $(( v * 512 ))
}

ensure_tool() {
  local cmd="$1" pkg="$2"
  command -v "${cmd}" >/dev/null 2>&1 && return 0
  command -v dnf >/dev/null 2>&1 \
    || die "${cmd} not found and dnf is unavailable to install ${pkg}"
  log "installing ${pkg} (provides ${cmd})"
  dnf install -y "${pkg}" || die "failed to install ${pkg}"
  command -v "${cmd}" >/dev/null 2>&1 || die "${cmd} still not found after installing ${pkg}"
}

# Canonical block device backing the filesystem that contains PATH. Btrfs
# subvolume mounts report SOURCE as /dev/sda4[/root]; strip the suffix.
mount_device() {
  local src
  src=$(findmnt -n -o SOURCE --target "$1" | head -n 1) || die "findmnt failed for $1"
  src="${src%%\[*}"
  [ -n "${src}" ] || die "cannot determine the device backing $1"
  readlink -f "${src}"
}

btrfs_devid_field() {
  awk -v f="$1" '$1 == "devid" { for (i = 1; i < NF; i++) if ($i == f) { print $(i + 1); exit } }'
}

main() {
  [ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"
  [[ "${MIN_DISK_GIB}" =~ ^[0-9]+$ ]] \
    || die "MIN_DISK_GIB must be a non-negative integer (got: ${MIN_DISK_GIB})"

  ensure_tool growpart cloud-utils-growpart
  ensure_tool btrfs btrfs-progs

  # --- Validate the layout before touching any device ----------------------
  local fstype root_dev dev m
  fstype=$(findmnt -n -o FSTYPE --target / | head -n 1)
  [ "${fstype}" = "btrfs" ] \
    || die "unsupported root filesystem '${fstype}' — only the Fedora Cloud Btrfs layout is supported; no changes made"
  root_dev=$(mount_device /)
  for m in /home /var; do
    dev=$(mount_device "${m}")
    [ "${dev}" = "${root_dev}" ] \
      || die "${m} is on ${dev}, not the root filesystem device ${root_dev} — unsupported layout; no changes made"
  done

  local dev_type disk_name disk_type part_name partnum
  dev_type=$(lsblk -ndo TYPE "${root_dev}")
  [ "${dev_type}" = "part" ] \
    || die "root device ${root_dev} is not a partition (type: ${dev_type:-unknown}) — unsupported layout; no changes made"
  disk_name=$(lsblk -ndo PKNAME "${root_dev}")
  [ -n "${disk_name}" ] || die "cannot determine the disk holding ${root_dev}; no changes made"
  disk_type=$(lsblk -ndo TYPE "/dev/${disk_name}")
  [ "${disk_type}" = "disk" ] \
    || die "/dev/${disk_name} (parent of ${root_dev}) is not a whole disk (type: ${disk_type:-unknown}); no changes made"
  part_name="${root_dev##*/}"
  partnum=$(cat "${SYSFS_BLOCK}/${part_name}/partition" 2>/dev/null) || partnum=""
  [[ "${partnum}" =~ ^[0-9]+$ ]] \
    || die "cannot determine the partition number of ${root_dev}; no changes made"

  local show devid_count total_devices devid devid_path
  show=$(btrfs filesystem show --raw /) || die "btrfs filesystem show / failed; no changes made"
  devid_count=$(awk '$1 == "devid"' <<<"${show}" | wc -l)
  [ "${devid_count}" -eq 1 ] \
    || die "root Btrfs filesystem spans ${devid_count} devices — unsupported layout; no changes made"
  # A degraded multi-device filesystem can print one devid row while still
  # declaring more devices, so also require the declared count to be 1.
  total_devices=$(awk '$1 == "Total" && $2 == "devices" { print $3; exit }' <<<"${show}")
  [[ "${total_devices}" =~ ^[0-9]+$ ]] \
    || die "cannot determine the root Btrfs device count — unsupported layout; no changes made"
  [ "${total_devices}" -eq 1 ] \
    || die "root Btrfs filesystem declares ${total_devices} devices — unsupported layout; no changes made"
  ! grep -qi 'missing' <<<"${show}" \
    || die "root Btrfs filesystem reports missing devices — unsupported layout; no changes made"
  devid=$(awk '$1 == "devid" { print $2; exit }' <<<"${show}")
  [[ "${devid}" =~ ^[0-9]+$ ]] \
    || die "cannot determine the root Btrfs device ID; no changes made"
  devid_path=$(btrfs_devid_field path <<<"${show}")
  [ "$(readlink -f "${devid_path}")" = "${root_dev}" ] \
    || die "root Btrfs device ${devid_path} does not match mounted device ${root_dev}; no changes made"

  # --- Grow ----------------------------------------------------------------
  # Pick up an online disk resize (gcloud compute disks resize) if the
  # kernel has not noticed it yet.
  if [ -w "${SYSFS_BLOCK}/${disk_name}/device/rescan" ]; then
    echo 1 > "${SYSFS_BLOCK}/${disk_name}/device/rescan" || true
  fi

  log "growing partition ${partnum} of /dev/${disk_name} (${root_dev})"
  local out rc=0
  out=$(growpart "/dev/${disk_name}" "${partnum}" 2>&1) || rc=$?
  [ -z "${out}" ] || printf '%s\n' "${out}"
  if [ "${rc}" -eq 1 ] && grep -q 'NOCHANGE' <<<"${out}"; then
    log "partition already fills the available space"
  elif [ "${rc}" -ne 0 ]; then
    die "growpart /dev/${disk_name} ${partnum} failed (exit ${rc})"
  fi

  log "resizing the root Btrfs filesystem (devid ${devid}) to fill ${root_dev}"
  btrfs filesystem resize "${devid}:max" / || die "btrfs filesystem resize ${devid}:max / failed"

  # --- Verify --------------------------------------------------------------
  local disk_bytes part_bytes part_start fs_bytes unused
  disk_bytes=$(sysfs_bytes "${disk_name}" size)
  part_bytes=$(sysfs_bytes "${part_name}" size)
  part_start=$(sysfs_bytes "${part_name}" start)
  fs_bytes=$(btrfs filesystem show --raw / | btrfs_devid_field size) || fs_bytes=""
  [[ "${fs_bytes}" =~ ^[0-9]+$ ]] || die "cannot read the root Btrfs device size"

  log "disk /dev/${disk_name}: $(gib "${disk_bytes}"); partition ${root_dev}: $(gib "${part_bytes}"); Btrfs device size: $(gib "${fs_bytes}")"

  if [ "${MIN_DISK_GIB}" -gt 0 ] && [ "${disk_bytes}" -lt $(( MIN_DISK_GIB * 1073741824 )) ]; then
    die "disk /dev/${disk_name} is $(gib "${disk_bytes}"), expected at least ${MIN_DISK_GIB} GiB — resize the disk first"
  fi
  unused=$(( disk_bytes - part_start - part_bytes ))
  if [ "${unused}" -gt "${SLACK_BYTES}" ]; then
    die "root partition ${root_dev} ends $(gib "${unused}") before the end of /dev/${disk_name} — partition growth did not complete (is it the last partition?)"
  fi
  if [ $(( part_bytes - fs_bytes )) -gt "${SLACK_BYTES}" ]; then
    die "root Btrfs filesystem ($(gib "${fs_bytes}")) is smaller than its partition ($(gib "${part_bytes}")) — filesystem growth did not complete"
  fi

  df -h / /home /var 2>/dev/null || true
  log "OK: root filesystem spans the disk"
}

# Called last so a truncated copy (e.g. a dropped SSH stream into `bash -s`)
# defines nothing runnable instead of executing a prefix of the script. stdin
# is detached so no child (dnf, growpart) can consume the streamed script.
main "$@" </dev/null
