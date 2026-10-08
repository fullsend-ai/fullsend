#!/usr/bin/env bash
# create-gcp-vm_test.sh — End-to-end tests for create-gcp-vm.sh against a
# stubbed gcloud / GitLab API (curl).
#
# Covers fresh provisioning and --resume: a healthy runner converges without
# corrective changes, a small boot disk is grown (never shrunk), interrupted
# provisioning is finished, repeated resume never duplicates a registration
# (shared-token and GL_TOKEN modes), setup runs as the VM's existing service
# user rather than the operator's SSH login, and conflicting or unsafe state
# is refused without touching registrations.
#
# The gcloud stub simulates the GCE instance, its boot disk, and the VM behind
# `gcloud compute ssh`: the login user, the service-user probe, sudo -u
# wrapping, VM-side runner config, and setup.sh (which "registers" by
# recording the token it received, as gitlab-runner register would, and
# makes the user it ran as the service user, as setup_runner_user does). The
# curl stub simulates the GitLab runners API.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/create-gcp-vm_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CREATE_GCP="${SCRIPT_DIR}/create-gcp-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

SHIM_DIR=$(mktemp -d)
STATE=$(mktemp -d)
trap 'rm -rf "${SHIM_DIR}" "${STATE}"' EXIT

# --- sleep: with_backoff and the boot wait must not slow the tests ---------
printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/sleep"

# --- gcloud: the instance, its boot disk, and the VM behind compute ssh ----
cat > "${SHIM_DIR}/gcloud" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "gcloud $*" >> "${S}/gcloud.log"
vm_status() { cat "${S}/vm_status" 2>/dev/null || echo RUNNING; }
case "$1 $2 $3" in
  "compute instances list")
    [ -f "${S}/vm_exists" ] && echo "fullsend-gitlab-runner-05"
    exit 0 ;;
  "compute instances create")
    touch "${S}/vm_exists"; echo 30 > "${S}/disk_gb"; exit 0 ;;
  "compute instances describe")
    [ -f "${S}/vm_exists" ] || exit 1
    case " $* " in
      *" --format=value(status) "*) vm_status ;;
      *" --format=json "*)
        printf '{"disks": [{"boot": true, "source": "https://www.googleapis.com/compute/v1/projects/%s/zones/%s/disks/%s"}]}\n' \
          test-project "${DISK_ZONE:-us-east1-b}" "$4" ;;
    esac
    exit 0 ;;
  "compute disks describe")
    cat "${S}/disk_gb" 2>/dev/null || echo 20; exit 0 ;;
  "compute disks resize")
    for a in "$@"; do
      case "${a}" in --size=*) n="${a#--size=}"; echo "${n%GB}" > "${S}/disk_gb" ;; esac
    done
    exit 0 ;;
esac
[ "$1 $2" = "compute ssh" ] || exit 1

cmd=""
while [ $# -gt 0 ]; do
  case "$1" in
    --command=*) cmd="${1#--command=}" ;;
    --) cmd="$2"; shift ;;
  esac
  shift
done
printf '%s\n' "${cmd}" >> "${S}/ssh_raw.log"

# Unwrap `sudo -n -u USER -H env ... bash -c '<cmd>'` (create's as_runner).
user=$(cat "${S}/login_user" 2>/dev/null || echo alice)
case "${cmd}" in
  "sudo -n -u "*" bash -c "*)
    parsed=$(python3 -c 'import shlex, sys
a = shlex.split(sys.argv[1])
print(a[3]); print(a[a.index("bash") + 2])' "${cmd}")
    user=$(head -n 1 <<< "${parsed}")
    cmd=$(tail -n +2 <<< "${parsed}")
    cmd="${cmd#cd || exit; }" ;;
esac
printf '[%s] %s\n' "${user}" "${cmd}" >> "${S}/ssh.log"

uid_of() { case "$1" in alice) echo 1001 ;; bob) echo 1002 ;; carol) echo 1003 ;; *) return 1 ;; esac; }

case "${cmd}" in
  "true") exit 0 ;;
  "sudo dnf install "*) exit 0 ;;
  "sudo env MIN_DISK_GIB="*" bash -s")
    cat > /dev/null
    echo "${cmd}" > "${S}/grow_cmd"
    echo "==> OK: root filesystem spans the disk" ;;
  *'login=%s'*)
    # The service-user probe: login user, drop-in User=, /etc/gitlab-runner owner.
    [ -f "${S}/no_login_sudo" ] && exit 1
    echo "login=${user}"
    [ -f "${S}/unit_user" ] && echo "unit=$(cat "${S}/unit_user")"
    [ -f "${S}/owner_user" ] && echo "owner=$(cat "${S}/owner_user")"
    exit 0 ;;
  "getent passwd "*)
    u="${cmd#getent passwd }"
    uid=$(uid_of "${u}") || exit 2
    echo "${u}:x:${uid}:${uid}::/home/${u}:/bin/bash" ;;
  "sudo -n -u "*" sudo -n true")
    u=$(cut -d' ' -f4 <<< "${cmd}")
    [ ! -f "${S}/no_sudo_${u}" ] ;;
  "sudo loginctl enable-linger "*)
    touch "${S}/linger_$(cut -d' ' -f4 <<< "${cmd}")" ;;
  *"sudo python3 -c"*"runners/verify"*)
    # The VM-side token check: run the script's real Python against a VM root,
    # with urllib's urlopen replaced by the fake GitLab in pyshim.
    root="${S}/vmroot"
    rm -rf "${root}"; mkdir -p "${root}"
    if [ -f "${S}/vm_config" ]; then
      printf '[[runners]]\n  id = %s\n  url = "%s"\n  token = "%s"\n' \
        "$(sed 's/.*-//' "${S}/vm_config")" "${GITLAB_URL}" "$(cat "${S}/vm_config")" \
        > "${root}/config.toml"
    else
      exit 1
    fi
    echo "r_vmsystem01" > "${root}/.runner_system_id"
    real="${cmd#sudo }"
    PYTHONPATH="$(dirname "$0")/pyshim" bash -c "${real//\/etc\/gitlab-runner/${root}}" ;;
  *"sudo python3 -c"*"/etc/gitlab-runner/config.toml"*)
    # The VM-side TOML probe: "<id> <url>" per [[runners]] entry.
    if [ -f "${S}/vm_config_noid" ]; then
      # A hand-edited config whose [[runners]] entry records no id: run the
      # script's real Python against a VM root holding it.
      root="${S}/vmroot"
      rm -rf "${root}"; mkdir -p "${root}"
      printf '[[runners]]\n  url = "%s"\n  token = "glrt-noid"\n' "${GITLAB_URL}" > "${root}/config.toml"
      real="${cmd#sudo }"
      bash -c "${real//\/etc\/gitlab-runner/${root}}"
    elif [ -f "${S}/vm_config" ]; then
      echo "$(sed 's/.*-//' "${S}/vm_config") $(cat "${S}/vm_runner_url" 2>/dev/null || echo "${GITLAB_URL}")"
    fi ;;
  "sudo systemctl stop gitlab-runner; ! systemctl is-active --quiet gitlab-runner")
    rm -f "${S}/service_active" ;;
  "sudo mv /etc/gitlab-runner/config.toml /etc/gitlab-runner/config.toml.stale-"*" && sudo chmod 600 /etc/gitlab-runner/config.toml.stale-"*)
    # The stale config is moved aside, not deleted: keep it as the backup.
    mv "${S}/vm_config" "${S}/vm_config_backup" ;;
  "mkdir -p ~/gitlab-runner-vm" | "chmod +x "*) exit 0 ;;
  "tar -C ~/gitlab-runner-vm -xf -")
    cat > /dev/null; echo "${user}" >> "${S}/files_users" ;;
  "cd ~/gitlab-runner-vm && sha256sum -c --quiet -")
    cat > /dev/null ;;
  *"cat > ~/gitlab-runner-vm/.env"*)
    # setup.sh: register_runner records the token unless already registered;
    # setup_runner_user makes the user it runs as the service user.
    env_file="${S}/env.$(find "${S}" -maxdepth 1 -name 'env.*' | wc -l)"
    cat > "${env_file}"
    echo "${user}" >> "${S}/setup_users"
    token=$(sed -n "s/^REGISTRATION_TOKEN='\(.*\)'$/\1/p" "${env_file}")
    if [ ! -f "${S}/vm_config" ]; then
      [ -n "${token}" ] || exit 1
      echo "${token}" > "${S}/vm_config"
    fi
    echo "${user}" > "${S}/unit_user"
    echo "${user}" > "${S}/owner_user"
    exit "$(cat "${S}/setup_rc" 2>/dev/null || echo 0)" ;;
  *)
    echo "unexpected ssh command: ${cmd}" >&2
    exit 1 ;;
esac
EOF

# --- curl: the GitLab runners API ------------------------------------------
cat > "${SHIM_DIR}/curl" <<'EOF'
#!/usr/bin/env python3
import json, os, sys, urllib.parse
state = os.environ["STUB_STATE"]
args = sys.argv[1:]
method, url, desc, write_out = "GET", "", "", ""
form = {}
i = 0
while i < len(args):
    a = args[i]
    if a == "-X":
        method = args[i + 1]; i += 1
    elif a == "-w":
        write_out = args[i + 1]; i += 1
    elif a == "-o":
        i += 1
    elif a == "--data-urlencode":
        k, _, v = args[i + 1].partition("=")
        form[k] = v
        if k == "description":
            desc = v
        i += 1
    elif a.startswith("https://"):
        url = a
    i += 1
with open(os.path.join(state, "curl.log"), "a") as f:
    f.write(f"{method} {url}\n")
db = os.path.join(state, "runners.json")
runners = json.load(open(db)) if os.path.exists(db) else []
last = os.path.join(state, "last_id")
path = urllib.parse.urlparse(url).path
if method == "POST" and path == "/api/v4/user/runners":
    n = 1 + max([r["id"] for r in runners] + [int(open(last).read()) if os.path.exists(last) else 0])
    open(last, "w").write(str(n))
    entry = {"id": n, "description": desc,
             "tag_list": form.get("tag_list", "").split(","),
             "access_level": form.get("access_level"),
             "runner_type": form.get("runner_type"),
             "run_untagged": form.get("run_untagged") == "true",
             "locked": form.get("locked") == "true"}
    if "group_id" in form:
        entry["groups"] = [{"id": int(form["group_id"])}]
    if "project_id" in form:
        entry["projects"] = [{"id": int(form["project_id"])}]
    runners.append(entry)
    print(json.dumps({"id": n, "token": f"glrt-new-{n}"}))
elif method == "GET" and path == "/api/v4/runners":
    print(json.dumps(runners))
    sys.exit(0)
elif method == "GET" and path.startswith("/api/v4/runners/"):
    rid = int(path.rsplit("/", 1)[1])
    match = [r for r in runners if r["id"] == rid]
    if write_out:
        print("200" if match else "404", end="")
        sys.exit(0 if match else 22)
    if not match:
        sys.exit(22)
    print(json.dumps(match[0]))
    sys.exit(0)
elif method == "DELETE" and path.startswith("/api/v4/runners/"):
    rid = int(path.rsplit("/", 1)[1])
    runners = [r for r in runners if r["id"] != rid]
else:
    sys.exit(22)
json.dump(runners, open(db, "w"))
EOF
# --- urllib: the fake GitLab behind the VM-side POST /runners/verify --------
mkdir -p "${SHIM_DIR}/pyshim"
cat > "${SHIM_DIR}/pyshim/sitecustomize.py" <<'EOF'
import io, json, os, urllib.error, urllib.parse, urllib.request

def urlopen(url, data=None, timeout=None, *args, **kwargs):
    form = {k: v[0] for k, v in urllib.parse.parse_qs((data or b"").decode()).items()}
    token = form.get("token", "")
    if token.startswith("glrt-") and not form.get("system_id"):
        raise urllib.error.HTTPError(url, 400, "system_id is missing", {}, io.BytesIO(b"{}"))
    return io.BytesIO(json.dumps({"id": int(token.rsplit("-", 1)[1])}).encode())

urllib.request.urlopen = urlopen
EOF
chmod +x "${SHIM_DIR}/gcloud" "${SHIM_DIR}/curl" "${SHIM_DIR}/sleep"

# new_state — fresh simulated project/VM/GitLab for one scenario.
new_state() {
  rm -rf "${STATE}"
  STATE=$(mktemp -d)
}

# existing_vm <service_user> <disk_gb> — a VM some earlier run provisioned.
existing_vm() {
  touch "${STATE}/vm_exists"
  echo "$2" > "${STATE}/disk_gb"
  if [ -n "$1" ]; then
    echo "$1" > "${STATE}/unit_user"
    echo "$1" > "${STATE}/owner_user"
  fi
}

# run_create [KEY=val ...] [args] — run the create script against the stubs.
# Sets RUN_RC, RUN_OUT.
run_create() {
  local assigns=()
  while [ $# -gt 0 ] && [[ "$1" == *=* ]]; do
    assigns+=("$1")
    shift
  done
  set +e
  RUN_OUT=$(env -u GL_TOKEN -u RUNNER_TOKEN -u PROJECT_ID -u GROUP_ID -u RUNNER_USER -u GITHUB_ENV \
    -u GCP_ZONE -u GCP_USE_IAP \
    PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" \
    GCP_PROJECT=test-project RUNNER_IMAGE=ghcr.io/example/runner:v1 \
    "${assigns[@]}" bash "${CREATE_GCP}" "$@" 2>&1 </dev/null)
  RUN_RC=$?
  set -e
}

runner_count() {
  python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "${STATE}/runners.json" 2>/dev/null || echo 0
}
# env_count — number of times setup.sh was run (one captured .env per run).
env_count() {
  find "${STATE}" -maxdepth 1 -name 'env.*' | wc -l
}
count_in() {
  grep -c -e "$1" "$2" 2>/dev/null || true
}
last_env() {
  local n
  n=$(env_count)
  n=$((n - 1))
  cat "${STATE}/env.${n}"
}

SELF_HOSTED="https://gitlab.internal.example"
SHARED=(RUNNER_TOKEN=glrt-shared "GITLAB_URL=${SELF_HOSTED}")
INDIVIDUAL=(GL_TOKEN=glpat-test GROUP_ID=42 "GITLAB_URL=${SELF_HOSTED}")

echo "== fresh create, shared runner token =="
new_state
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -eq 0 ] && grep -Fq 'compute instances create fullsend-gitlab-runner-05' "${STATE}/gcloud.log" \
  && grep -Fq -- '--boot-disk-size=30GB' "${STATE}/gcloud.log"; then
  pass "fresh create makes VM 05 with a 30 GiB boot disk"
else
  fail "fresh create failed (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
if [ ! -s "${STATE}/curl.log" ] && grep -qx "REGISTRATION_TOKEN='glrt-shared'" "${STATE}/env.0"; then
  pass "shared-token create passes the token to setup.sh and calls no GitLab API"
else
  fail "shared-token create called the GitLab API or lost the token"
fi
if ! grep -q 'sudo -n -u' "${STATE}/ssh_raw.log" && [ "$(cat "${STATE}/setup_users")" = "alice" ] \
  && ! grep -Fq 'disks resize' "${STATE}/gcloud.log"; then
  pass "fresh create runs setup as the SSH login user and resizes no disk"
else
  fail "fresh create should run setup directly as the login user: $(cat "${STATE}/ssh_raw.log")"
fi

echo "== validation =="
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume 05' <<< "${RUN_OUT}" \
  && grep -Fq './delete-gcp-vm.sh fullsend-gitlab-runner-05 (which drains in-flight jobs)' <<< "${RUN_OUT}" \
  && [ "$(count_in 'instances create' "${STATE}/gcloud.log")" -eq 1 ]; then
  pass "create refuses an existing VM and names --resume and the drain-safe delete"
else
  fail "create over an existing VM (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

new_state
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'nothing to resume' <<< "${RUN_OUT}" \
  && ! grep -Fq 'instances create' "${STATE}/gcloud.log"; then
  pass "resume refuses a missing VM and never creates one"
else
  fail "resume of a missing VM (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

run_create "${SHARED[@]}" --resume
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume requires the NUMBER' <<< "${RUN_OUT}"; then
  pass "--resume without NUMBER is rejected"
else
  fail "--resume without NUMBER (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

run_create "${SHARED[@]}" --resume five
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'NUMBER must be numeric' <<< "${RUN_OUT}"; then
  pass "--resume with a non-numeric NUMBER is rejected"
else
  fail "--resume five (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

run_create "${SHARED[@]}" RUNNER_USER='bad;user' --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'RUNNER_USER must be a plain, non-root Unix user name' <<< "${RUN_OUT}"; then
  pass "an invalid RUNNER_USER is rejected before touching GCP"
else
  fail "invalid RUNNER_USER (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

new_state
existing_vm alice 30
echo TERMINATED > "${STATE}/vm_status"
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'is TERMINATED, not RUNNING' <<< "${RUN_OUT}" \
  && grep -Fq 'gcloud compute instances start fullsend-gitlab-runner-05' <<< "${RUN_OUT}" \
  && [ ! -e "${STATE}/ssh.log" ]; then
  pass "resume refuses a stopped VM with a start hint and does not start it"
else
  fail "resume of a stopped VM (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

echo "== --resume, shared runner token =="
new_state
run_create "${SHARED[@]}" 05
cp "${STATE}/vm_config" "${STATE}/vm_config.before"
creates=$(count_in 'instances create' "${STATE}/gcloud.log")
run_create "${SHARED[@]}" --resume 05
first_rc="${RUN_RC}"
run_create "${SHARED[@]}" --resume 05
if [ "${first_rc}" -eq 0 ] && [ "${RUN_RC}" -eq 0 ] \
  && [ "$(count_in 'instances create' "${STATE}/gcloud.log")" -eq "${creates}" ] \
  && [ ! -s "${STATE}/curl.log" ] && cmp -s "${STATE}/vm_config" "${STATE}/vm_config.before" \
  && ! grep -Fq 'disks resize' "${STATE}/gcloud.log" \
  && ! grep -Fq 'config.toml.stale-' "${STATE}/ssh.log"; then
  pass "resuming a healthy runner is repeatable and changes no VM, disk, config, or registration"
else
  fail "healthy shared-token resume (rc=${first_rc}, ${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
if grep -qx "REGISTRATION_TOKEN='glrt-shared'" <(last_env) \
  && grep -Fq 'service user alice (existing' <<< "${RUN_OUT}" \
  && grep -Fq 'sudo env MIN_DISK_GIB=30 bash -s' "${STATE}/grow_cmd"; then
  pass "resume reruns the same setup.sh, package and disk-growth steps as create"
else
  fail "resume should rerun fresh provisioning's steps: $(tail -5 <<< "${RUN_OUT}")"
fi

# Interrupted before setup.sh ever ran: no service user, no config.
new_state
existing_vm "" 30
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(cat "${STATE}/vm_config")" = "glrt-shared" ] \
  && grep -Fq 'VM has none yet' <<< "${RUN_OUT}" && [ ! -s "${STATE}/curl.log" ]; then
  pass "resume finishes a VM interrupted before setup, as the login user"
else
  fail "resume of an unprovisioned VM (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo 1 > "${STATE}/setup_rc"
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume 05' <<< "${RUN_OUT}" \
  && grep -Fq 'Or delete it:' <<< "${RUN_OUT}" && [ ! -s "${STATE}/curl.log" ]; then
  pass "a failed shared-token resume points at --resume and never touches GitLab"
else
  fail "failed shared-token resume (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo "== --resume, boot disk =="
new_state
existing_vm alice 20
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(cat "${STATE}/disk_gb")" = "30" ] \
  && grep -Fq 'compute disks resize fullsend-gitlab-runner-05 --project=test-project --zone=us-east1-b --size=30GB' "${STATE}/gcloud.log"; then
  pass "resume grows a 20 GiB boot disk to 30 GiB"
else
  fail "small disk should be resized (rc=${RUN_RC}, disk=$(cat "${STATE}/disk_gb")): $(tail -5 <<< "${RUN_OUT}")"
fi

new_state
existing_vm alice 50
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(cat "${STATE}/disk_gb")" = "50" ] \
  && ! grep -Fq 'disks resize' "${STATE}/gcloud.log"; then
  pass "resume never shrinks a larger boot disk"
else
  fail "larger disk must be left alone (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

new_state
existing_vm alice 20
run_create "${SHARED[@]}" DISK_ZONE=us-west1-a --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'is not a zonal disk in us-east1-b' <<< "${RUN_OUT}" \
  && ! grep -Fq 'disks resize' "${STATE}/gcloud.log"; then
  pass "resume refuses to resize a boot disk outside GCP_ZONE"
else
  fail "foreign-zone disk (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo "== --resume, service user =="
# Provisioned by alice; bob runs the resume.
new_state
existing_vm alice 30
echo glrt-shared > "${STATE}/vm_config"
echo bob > "${STATE}/login_user"
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(sort -u "${STATE}/setup_users")" = "alice" ] \
  && [ "$(sort -u "${STATE}/files_users")" = "alice" ] \
  && [ "$(cat "${STATE}/unit_user")" = "alice" ] && [ -f "${STATE}/linger_alice" ]; then
  pass "another operator's resume copies files and runs setup as the existing service user"
else
  fail "service user should stay alice (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
if grep -F 'setup.sh' "${STATE}/ssh_raw.log" | grep -Fq 'sudo -n -u alice -H env USER=alice LOGNAME=alice XDG_RUNTIME_DIR=/run/user/1001 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus bash -c'; then
  pass "setup runs with the service user's home, login name, and rootless runtime directory"
else
  fail "setup wrapper lacks the service user's identity: $(grep -F 'setup.sh' "${STATE}/ssh_raw.log")"
fi

env_before=$(env_count)
run_create "${SHARED[@]}" RUNNER_USER=bob --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq "service user is alice" <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq "${env_before}" ]; then
  pass "resume refuses a RUNNER_USER that differs from the VM's service user"
else
  fail "RUNNER_USER mismatch (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

run_create "${SHARED[@]}" RUNNER_USER=alice --resume 05
if [ "${RUN_RC}" -eq 0 ]; then
  pass "resume accepts a RUNNER_USER that names the VM's service user"
else
  fail "matching RUNNER_USER (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

echo carol > "${STATE}/owner_user"
env_before=$(env_count)
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'conflicting gitlab-runner service users' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq "${env_before}" ]; then
  pass "resume refuses ambiguous service-user state"
else
  fail "ambiguous service user (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi
echo alice > "${STATE}/owner_user"

touch "${STATE}/no_sudo_alice"
run_create "${SHARED[@]}" --resume 05
rm -f "${STATE}/no_sudo_alice"
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'alice has no passwordless sudo' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq "${env_before}" ]; then
  pass "resume explains a service user that cannot run setup.sh's sudo"
else
  fail "service user without sudo (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

# A root-owned /etc/gitlab-runner (created, not yet chowned) records no user.
new_state
existing_vm "" 30
echo root > "${STATE}/owner_user"
echo bob > "${STATE}/login_user"
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(cat "${STATE}/setup_users")" = "bob" ] \
  && ! grep -q 'sudo -n -u' "${STATE}/ssh_raw.log"; then
  pass "a VM without a service user is finished as the login user"
else
  fail "no-service-user resume (rc=${RUN_RC}): $(tail -3 <<< "${RUN_OUT}")"
fi

echo "== --resume, GL_TOKEN registration =="
new_state
existing_vm "" 30
echo 1 > "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && [ "$(runner_count)" -eq 0 ] && [ -f "${STATE}/vm_config" ] \
  && grep -Fq 'deregistering runner 1' <<< "${RUN_OUT}" && grep -Fq -- '--resume 05' <<< "${RUN_OUT}"; then
  pass "a failed run deregisters the runner it created and points at --resume"
else
  fail "failed run should roll back its runner (rc=${RUN_RC}, runners=$(runner_count)): $(tail -5 <<< "${RUN_OUT}")"
fi

rm -f "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(runner_count)" -eq 1 ] \
  && grep -Fq 'sudo mv /etc/gitlab-runner/config.toml /etc/gitlab-runner/config.toml.stale-' "${STATE}/ssh.log" \
  && ! grep -Fq 'sudo rm -f /etc/gitlab-runner/config.toml' "${STATE}/ssh.log" \
  && [ "$(cat "${STATE}/vm_config_backup")" = "glrt-new-1" ] \
  && [ "$(cat "${STATE}/vm_config")" = "glrt-new-2" ] \
  && grep -Fq '"description": "test-project/fullsend-gitlab-runner-05"' "${STATE}/runners.json"; then
  pass "resume moves the stale config aside and replaces it with exactly one new registration"
else
  fail "resume after rollback (rc=${RUN_RC}, runners=$(runner_count)): $(tail -5 <<< "${RUN_OUT}")"
fi

posts_before=$(count_in '^POST ' "${STATE}/curl.log")
run_create "${INDIVIDUAL[@]}" --resume 05
first_rc="${RUN_RC}"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${first_rc}" -eq 0 ] && [ "${RUN_RC}" -eq 0 ] && [ "$(runner_count)" -eq 1 ] \
  && [ "$(count_in '^POST ' "${STATE}/curl.log")" -eq "${posts_before}" ] \
  && grep -Fq 'reusing runner ID 2' <<< "${RUN_OUT}" \
  && grep -qx "REGISTRATION_TOKEN=''" <(last_env); then
  pass "repeating resume reuses the VM's runner (no duplicate registration)"
else
  fail "repeat resume should reuse runner 2 (rc=${first_rc}, ${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
if ! grep -v "^[A-Z]* ${SELF_HOSTED}/" "${STATE}/curl.log" | grep -q .; then
  pass "every GitLab API call targets the supplied GITLAB_URL"
else
  fail "GitLab API calls outside ${SELF_HOSTED}: $(tr '\n' '|' < "${STATE}/curl.log")"
fi

deletes_before=$(count_in '^DELETE ' "${STATE}/curl.log")
echo 1 > "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
rm -f "${STATE}/setup_rc"
if [ "${RUN_RC}" -ne 0 ] && [ "$(runner_count)" -eq 1 ] \
  && [ "$(count_in '^DELETE ' "${STATE}/curl.log")" -eq "${deletes_before}" ]; then
  pass "a failed resume never deregisters a previously healthy runner"
else
  fail "failed reuse-resume touched the existing runner (rc=${RUN_RC}): $(tr '\n' '|' < "${STATE}/curl.log")"
fi

echo https://gitlab.other.example > "${STATE}/vm_runner_url"
touch "${STATE}/service_active"
env_before=$(env_count)
run_create "${INDIVIDUAL[@]}" --resume 05
rm -f "${STATE}/vm_runner_url"
if [ "${RUN_RC}" -ne 0 ] && grep -Fq "configured for GitLab instance 'https://gitlab.other.example'" <<< "${RUN_OUT}" \
  && [ ! -f "${STATE}/service_active" ] && [ "$(env_count)" -eq "${env_before}" ] \
  && [ "$(cat "${STATE}/vm_config")" = "glrt-new-2" ] && [ "$(runner_count)" -eq 1 ]; then
  pass "resume refuses a wrong-instance config, stops the service, and keeps the config"
else
  fail "wrong-instance config (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# A second runner registered under this VM's description is ambiguous.
python3 - "${STATE}/runners.json" <<'PY'
import json, sys
p = sys.argv[1]
r = json.load(open(p))
r.append(dict(r[0], id=9))
json.dump(r, open(p, "w"))
PY
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq '2 runners are registered as test-project/fullsend-gitlab-runner-05' <<< "${RUN_OUT}" \
  && [ "$(runner_count)" -eq 2 ] && [ "$(env_count)" -eq "${env_before}" ]; then
  pass "resume refuses ambiguous registrations without deregistering either"
else
  fail "ambiguous registrations (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# Registered, but the VM never received the token: resume cannot finish it.
new_state
existing_vm "" 30
echo '[{"id": 7, "description": "test-project/fullsend-gitlab-runner-05"}]' > "${STATE}/runners.json"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'its token cannot be retrieved' <<< "${RUN_OUT}" \
  && ! grep -Fq -- '--resume 05' <<< "${RUN_OUT}" && [ "$(runner_count)" -eq 1 ] \
  && [ "$(count_in '^POST ' "${STATE}/curl.log")" -eq 0 ]; then
  pass "resume explains a registered-but-unconfigured VM it cannot recover"
else
  fail "registered-but-unconfigured VM (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# A runner under another description is unrelated: never replaced or removed.
new_state
existing_vm alice 30
echo glrt-new-3 > "${STATE}/vm_config"
echo '[{"id": 3, "description": "someone/else", "tag_list": ["fullsend-gitlab-runner"]}]' > "${STATE}/runners.json"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'could not be confirmed deleted' <<< "${RUN_OUT}" \
  && [ "$(cat "${STATE}/vm_config")" = "glrt-new-3" ] && [ "$(runner_count)" -eq 1 ] \
  && [ "$(count_in '^POST ' "${STATE}/curl.log")" -eq 0 ] \
  && [ "$(count_in '^DELETE ' "${STATE}/curl.log")" -eq 0 ]; then
  pass "resume never replaces a config whose runner still exists elsewhere"
else
  fail "unrelated live runner (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# A [[runners]] entry with no id must not default to a runner ID that GitLab
# then reports missing: resume refuses instead of replacing the config.
new_state
existing_vm alice 30
echo glrt-new-3 > "${STATE}/vm_config"
touch "${STATE}/vm_config_noid"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no positive integer id' <<< "${RUN_OUT}" \
  && [ "$(cat "${STATE}/vm_config")" = "glrt-new-3" ] && [ ! -e "${STATE}/vm_config_backup" ] \
  && ! grep -q '^\(POST\|DELETE\) ' "${STATE}/curl.log" 2>/dev/null \
  && ! grep -Fq 'config.toml.stale-' "${STATE}/ssh.log"; then
  pass "resume refuses a config whose runner entry has no id"
else
  fail "config without a runner id (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed" >&2
  exit 1
fi
echo "All create-gcp-vm.sh tests passed"
