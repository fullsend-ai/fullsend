#!/usr/bin/env bash
# vm_test.sh — Tests for the vm.yaml cloud-init bootstrap (bootcmd).
#
# bootcmd runs before cloud-init's packages module, so it alone decides
# whether the first `dnf install` on a fresh VM can reach a repository.
# These tests extract the bootcmd entries from vm.yaml and run them against
# representative Fedora 44 cloud-image repo files (placeholder baseurl=,
# metalink-only OpenH264 repo) in a temp dir instead of /etc/yum.repos.d.
# Enabled repos must end up on HTTPS: some egress-restricted clusters block
# HTTP (port 80) to dl.fedoraproject.org while HTTPS works (#8169).
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/vm_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="${SCRIPT_DIR}/vm.yaml"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

WORK_DIR=$(mktemp -d)
trap 'rm -rf "${WORK_DIR}"' EXIT

# Print each bootcmd entry of vm.yaml's cloud-config, one per line. Entries
# are plain double-quoted YAML strings; a backslash would need YAML
# unescaping, so refuse it rather than run something other than what
# cloud-init runs.
extract_bootcmd() {
  python3 - "${TEMPLATE}" <<'PY'
import re, sys
lines = open(sys.argv[1]).read().splitlines()
start = next(i for i, l in enumerate(lines) if l.strip() == "bootcmd:")
indent = len(lines[start]) - len(lines[start].lstrip())
for line in lines[start + 1:]:
    m = re.match(r'^(\s*)- "(.*)"\s*$', line)
    if not m or len(m.group(1)) <= indent:
        break
    if "\\" in m.group(2):
        sys.exit("bootcmd entry contains a backslash; extend vm_test.sh to unescape it")
    print(m.group(2))
PY
}

# write_fedora44_repos <dir> — stock Fedora 44 cloud-image repo files
# (trimmed to the keys that matter for repo selection).
write_fedora44_repos() {
  local dir="$1"
  mkdir -p "${dir}"
  cat > "${dir}/fedora.repo" <<'EOF'
[fedora]
name=Fedora $releasever - $basearch
#baseurl=http://download.example/pub/fedora/linux/releases/$releasever/Everything/$basearch/os/
metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch
enabled=1
countme=1
gpgcheck=1
skip_if_unavailable=False

[fedora-debuginfo]
name=Fedora $releasever - $basearch - Debug
#baseurl=http://download.example/pub/fedora/linux/releases/$releasever/Everything/$basearch/debug/tree/
metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-debug-$releasever&arch=$basearch
enabled=0
gpgcheck=1
skip_if_unavailable=False
EOF
  cat > "${dir}/fedora-updates.repo" <<'EOF'
[updates]
name=Fedora $releasever - $basearch - Updates
#baseurl=http://download.example/pub/fedora/linux/updates/$releasever/Everything/$basearch/
metalink=https://mirrors.fedoraproject.org/metalink?repo=updates-released-f$releasever&arch=$basearch
enabled=1
countme=1
gpgcheck=1
skip_if_unavailable=False
EOF
  cat > "${dir}/fedora-updates-testing.repo" <<'EOF'
[updates-testing]
name=Fedora $releasever - $basearch - Test Updates
#baseurl=http://download.example/pub/fedora/linux/updates/testing/$releasever/Everything/$basearch/
metalink=https://mirrors.fedoraproject.org/metalink?repo=updates-testing-f$releasever&arch=$basearch
enabled=0
gpgcheck=1
skip_if_unavailable=False
EOF
  cat > "${dir}/fedora-cisco-openh264.repo" <<'EOF'
[fedora-cisco-openh264]
name=Fedora $releasever openh264 (From Cisco) - $basearch
metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-cisco-openh264-$releasever&arch=$basearch
type=rpm
enabled=1
gpgcheck=1
skip_if_unavailable=True

[fedora-cisco-openh264-debuginfo]
name=Fedora $releasever openh264 (From Cisco) - $basearch - Debug
metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-cisco-openh264-debug-$releasever&arch=$basearch
type=rpm
enabled=0
gpgcheck=1
skip_if_unavailable=True
EOF
}

# run_bootcmd <dir> — run every bootcmd entry with /etc/yum.repos.d
# redirected to <dir>. Returns the first failing entry's exit status.
run_bootcmd() {
  local dir="$1" cmd
  while IFS= read -r cmd; do
    sh -c "${cmd//\/etc\/yum.repos.d/${dir}}" || return $?
  done <<< "${BOOTCMD}"
}

# write_http_baseurl_repos <dir> — repo files a previous bootstrap left with
# metalink= commented out and a plain-HTTP dl.fedoraproject.org baseurl=
# enabled (the repair case: no active metalink= to trigger a switch).
write_http_baseurl_repos() {
  local dir="$1"
  mkdir -p "${dir}"
  cat > "${dir}/fedora.repo" <<'EOF'
[fedora]
name=Fedora $releasever - $basearch
baseurl=http://dl.fedoraproject.org/pub/fedora/linux/releases/$releasever/Everything/$basearch/os/
#metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch
enabled=1
gpgcheck=1
EOF
  cat > "${dir}/fedora-updates.repo" <<'EOF'
[updates]
name=Fedora $releasever - $basearch - Updates
baseurl=http://dl.fedoraproject.org/pub/fedora/linux/updates/$releasever/Everything/$basearch/
#metalink=https://mirrors.fedoraproject.org/metalink?repo=updates-released-f$releasever&arch=$basearch
enabled=1
gpgcheck=1
EOF
}

# check_enabled_repos <dir> — print one line per enabled repo section that
# dnf could not load from the egress-allowlisted mirror over HTTPS, and exit
# non-zero if there are any.
check_enabled_repos() {
  python3 - "$1" <<'PY'
import configparser, glob, sys
bad = []
for path in sorted(glob.glob(sys.argv[1] + "/fedora*.repo")):
    cp = configparser.RawConfigParser()
    cp.read(path)
    for section in cp.sections():
        if cp.get(section, "enabled", fallback="1") != "1":
            continue
        baseurl = cp.get(section, "baseurl", fallback="")
        if cp.has_option(section, "metalink"):
            bad.append(f"{section}: metalink= still active")
        elif not baseurl.startswith("https://dl.fedoraproject.org/"):
            bad.append(f"{section}: unusable baseurl={baseurl!r}")
for b in bad:
    print(b)
sys.exit(1 if bad else 0)
PY
}

echo "== vm.yaml bootcmd extraction =="
if ! BOOTCMD=$(extract_bootcmd) || [ -z "${BOOTCMD}" ]; then
  fail "could not extract bootcmd entries from vm.yaml"
  exit 1
fi
if grep -Fq '/etc/yum.repos.d' <<< "${BOOTCMD}"; then
  pass "bootcmd targets /etc/yum.repos.d"
else
  fail "bootcmd no longer targets /etc/yum.repos.d — update vm_test.sh's path redirect"
fi

# bootcmd must precede packages: in the cloud-config, so a reader editing
# the template sees which config the package install depends on.
if python3 - "${TEMPLATE}" <<'PY'
import sys
text = open(sys.argv[1]).read()
sys.exit(0 if 0 <= text.find("bootcmd:") < text.find("packages:") else 1)
PY
then
  pass "bootcmd precedes the packages list"
else
  fail "bootcmd missing or after packages: in vm.yaml"
fi

echo "== Fedora 44 cloud image repos =="
REPOS="${WORK_DIR}/fedora44"
write_fedora44_repos "${REPOS}"
if run_bootcmd "${REPOS}"; then
  pass "bootcmd exits 0 on Fedora 44 repo files"
else
  fail "bootcmd failed on Fedora 44 repo files"
fi

if problems=$(check_enabled_repos "${REPOS}"); then
  pass "every enabled repo uses an HTTPS dl.fedoraproject.org baseurl"
else
  fail "enabled repos dnf cannot load: ${problems//$'\n'/; }"
fi

if grep -h '^[^#]' "${REPOS}"/fedora*.repo | grep -Fq 'download.example'; then
  fail "an active line still points at the download.example placeholder"
else
  pass "no active line points at download.example"
fi

if grep -A6 '^\[fedora-cisco-openh264\]' "${REPOS}/fedora-cisco-openh264.repo" | grep -qx 'enabled=0'; then
  pass "fedora-cisco-openh264 is disabled"
else
  fail "fedora-cisco-openh264 is still enabled"
fi

if grep -A6 '^\[fedora-debuginfo\]' "${REPOS}/fedora.repo" | grep -qx 'enabled=0' \
  && grep -A6 '^\[updates-testing\]' "${REPOS}/fedora-updates-testing.repo" | grep -qx 'enabled=0'; then
  pass "disabled repos stay disabled"
else
  fail "bootcmd enabled a repo that was disabled"
fi

echo "== idempotence (bootcmd runs on every boot) =="
before=$(cat "${REPOS}"/*.repo)
if run_bootcmd "${REPOS}" && [ "$(cat "${REPOS}"/*.repo)" = "${before}" ]; then
  pass "second boot leaves repo files unchanged"
else
  fail "second bootcmd run changed repo files or failed"
fi

echo "== images without the OpenH264 repo =="
NO_CISCO="${WORK_DIR}/no-cisco"
write_fedora44_repos "${NO_CISCO}"
rm -f "${NO_CISCO}/fedora-cisco-openh264.repo"
if run_bootcmd "${NO_CISCO}" && check_enabled_repos "${NO_CISCO}" >/dev/null; then
  pass "bootcmd succeeds when fedora-cisco-openh264.repo is absent"
else
  fail "bootcmd fails or leaves unusable repos when fedora-cisco-openh264.repo is absent"
fi

echo "== enabled plain-HTTP baseurl, no metalink (repair) =="
HTTP_REPOS="${WORK_DIR}/http-baseurl"
write_http_baseurl_repos "${HTTP_REPOS}"
problems=""
if run_bootcmd "${HTTP_REPOS}" && problems=$(check_enabled_repos "${HTTP_REPOS}"); then
  pass "bootcmd switches an enabled HTTP baseurl to HTTPS"
else
  problems="${problems:-bootcmd failed}"
  fail "bootcmd left an unusable enabled baseurl: ${problems//$'\n'/; }"
fi
before=$(cat "${HTTP_REPOS}"/*.repo)
if run_bootcmd "${HTTP_REPOS}" && [ "$(cat "${HTTP_REPOS}"/*.repo)" = "${before}" ]; then
  pass "second boot leaves repaired HTTPS repo files unchanged"
else
  fail "second bootcmd run changed repaired repo files or failed"
fi

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed" >&2
  exit 1
fi
echo "All vm.yaml bootstrap tests passed"
