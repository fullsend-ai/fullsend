#!/usr/bin/env bash
# create-openshift-vm_test.sh — End-to-end tests for create-openshift-vm.sh
# against stubbed oc / virtctl / GitLab API (curl).
#
# Covers the self-hosted shared-token path (GITLAB_URL is the only
# registration target), recovery from a cloud-init package failure within
# one invocation, and --resume (idempotent, never duplicates a registration).
#
# The virtctl stub simulates the VM: cloud-init status, the base-package
# check, the cloud-init repair, VM-side runner config, and setup.sh (which
# "registers" by recording the token it received, as gitlab-runner register
# would). The curl stub simulates the GitLab runners API.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/create-openshift-vm_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CREATE_OCP="${SCRIPT_DIR}/create-openshift-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

SHIM_DIR=$(mktemp -d)
STATE=$(mktemp -d)
trap 'rm -rf "${SHIM_DIR}" "${STATE}"' EXIT

# --- oc: VM existence and creation -----------------------------------------
cat > "${SHIM_DIR}/oc" <<'EOF'
#!/usr/bin/env bash
echo "oc $*" >> "${STUB_STATE}/oc.log"
case " $* " in
  *" create "*) cat > "${STUB_STATE}/manifest"; touch "${STUB_STATE}/vm_exists" ;;
  *" get vm --no-headers "*) [ -f "${STUB_STATE}/vm_exists" ] && echo "fullsend-gitlab-runner-05"; exit 0 ;;
  *" get vm "*) [ -f "${STUB_STATE}/vm_exists" ] ;;
  *) exit 1 ;;
esac
EOF

# --- virtctl: the VM -------------------------------------------------------
cat > "${SHIM_DIR}/virtctl" <<'EOF'
#!/usr/bin/env bash
cmd=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-c" ]; then cmd="$2"; shift 2; else shift; fi
done
printf '%s\n' "${cmd}" >> "${STUB_STATE}/virtctl.log"
case "${cmd}" in
  "cloud-init status --wait")
    exit "$(cat "${STUB_STATE}/cloud_init_rc" 2>/dev/null || echo 0)" ;;
  "rpm -q "*)
    [ ! -f "${STUB_STATE}/packages_missing" ] ;;
  "sudo sh -es")
    # The repo repair, sent on stdin. Only commands that fix the placeholder
    # baseurl, switch it to HTTPS (HTTP may be blocked, #8169), and fix the
    # OpenH264 repo mend a repo broken by older user data.
    cat > "${STUB_STATE}/repair_input"
    [ -f "${STUB_STATE}/repair_fails" ] && exit 1
    if grep -Fq 'download[.]example' "${STUB_STATE}/repair_input" \
      && grep -Fq 'baseurl=https://dl.fedoraproject.org/' "${STUB_STATE}/repair_input" \
      && grep -Fq 'fedora-cisco-openh264' "${STUB_STATE}/repair_input"; then
      touch "${STUB_STATE}/repo_fixed"
    fi ;;
  *"cloud-init single --name bootcmd"*)
    # Re-runs the VM's own user data, which on an old VM is the broken repo
    # config: it never mends anything.
    exit 0 ;;
  *"cloud-init single --name package_update_upgrade_install"*)
    # dnf fails while the repo config is still broken (old user data).
    if [ -f "${STUB_STATE}/old_userdata" ] && [ ! -f "${STUB_STATE}/repo_fixed" ]; then
      exit 1
    fi
    rm -f "${STUB_STATE}/packages_missing" ;;
  *"sudo python3 -c"*"runners/verify"*)
    # The VM-side token check: run the script's real Python against a VM root
    # (config.toml + .runner_system_id under ${STUB_STATE}/vmroot), with
    # urllib's urlopen replaced by the fake GitLab in pyshim/sitecustomize.py.
    # The VM's token is glrt-new-<ID> (the stub's registration), or the
    # hand-written config.toml's own token. The VM has a runner system ID
    # unless 'no_system_id' says gitlab-runner never wrote one.
    root="${STUB_STATE}/vmroot"
    rm -rf "${root}"; mkdir -p "${root}"
    if [ -f "${STUB_STATE}/config.toml" ]; then
      cp "${STUB_STATE}/config.toml" "${root}/config.toml"
    elif [ -f "${STUB_STATE}/vm_config" ]; then
      printf '[[runners]]\n  id = %s\n  url = "%s"\n  token = "%s"\n' \
        "$(sed 's/.*-//' "${STUB_STATE}/vm_config")" "${GITLAB_URL}" "$(cat "${STUB_STATE}/vm_config")" \
        > "${root}/config.toml"
    else
      exit 1
    fi
    [ -f "${STUB_STATE}/no_system_id" ] || echo "r_vmsystem01" > "${root}/.runner_system_id"
    real="${cmd#sudo }"
    PYTHONPATH="$(dirname "$0")/pyshim" bash -c "${real//\/etc\/gitlab-runner/${root}}" ;;
  *"sudo python3 -c"*"/etc/gitlab-runner/config.toml"*)
    # The VM-side TOML probe: "<id> <url>" per [[runners]] entry. The stub's
    # registration records the token glrt-new-<ID> and the requested
    # GITLAB_URL; vm_runner_id and vm_runner_url override them.
    if [ -f "${STUB_STATE}/config.toml" ]; then
      # Run the script's real probe against a hand-written config.toml.
      real="${cmd#sudo }"
      bash -c "${real//\/etc\/gitlab-runner/${STUB_STATE}}"
      exit $?
    elif [ -f "${STUB_STATE}/vm_config" ]; then
      if [ -f "${STUB_STATE}/vm_runner_id" ]; then
        vm_id=$(cat "${STUB_STATE}/vm_runner_id")
      else
        vm_id=$(sed 's/.*-//' "${STUB_STATE}/vm_config")
      fi
      if [ -f "${STUB_STATE}/vm_runner_url" ]; then
        echo "${vm_id} $(cat "${STUB_STATE}/vm_runner_url")"
      else
        echo "${vm_id} ${GITLAB_URL}"
      fi
    fi
    [ ! -f "${STUB_STATE}/probe_fails" ] ;;
  "sudo systemctl stop gitlab-runner; ! systemctl is-active --quiet gitlab-runner")
    rm -f "${STUB_STATE}/service_active" ;;
  "sudo rm -f /etc/gitlab-runner/config.toml")
    rm -f "${STUB_STATE}/vm_config" ;;
  *"cat > ~/gitlab-runner-vm/.env"*)
    # setup.sh: register_runner records the token unless already registered.
    env_file="${STUB_STATE}/env.$(ls "${STUB_STATE}" | grep -c '^env\.')"
    cat > "${env_file}"
    token=$(sed -n "s/^REGISTRATION_TOKEN='\(.*\)'$/\1/p" "${env_file}")
    if [ ! -f "${STUB_STATE}/vm_config" ]; then
      [ -n "${token}" ] || exit 1
      echo "${token}" > "${STUB_STATE}/vm_config"
    fi
    exit "$(cat "${STUB_STATE}/setup_rc" 2>/dev/null || echo 0)" ;;
  *)
    cat > /dev/null ;;
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
path = urllib.parse.urlparse(url).path
if method == "POST" and path == "/api/v4/user/runners":
    n = 1 + max([r["id"] for r in runners] + [int(open(os.path.join(state, "last_id")).read()) if os.path.exists(os.path.join(state, "last_id")) else 0])
    open(os.path.join(state, "last_id"), "w").write(str(n))
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
    # Honor the tag_list filter like GitLab does, so a lookup that filters on
    # a mutable tag misses a runner whose tags were edited.
    want_tags = urllib.parse.parse_qs(urllib.parse.urlparse(url).query).get("tag_list")
    if want_tags:
        wanted = want_tags[0].split(",")
        runners = [r for r in runners if set(wanted) <= set(r.get("tag_list") or [])]
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
# Replaces urlopen for the VM-side verify script only (via PYTHONPATH). Like
# GitLab, it answers a glrt- token only when the request carries a system_id
# (HTTP 400 otherwise), logs every request, and returns the ID of the runner
# the token belongs to: glrt-new-<ID>, or the 'token_id' file for a token
# bound to another runner. 'verify_fails' simulates an unreachable GitLab.
mkdir -p "${SHIM_DIR}/pyshim"
cat > "${SHIM_DIR}/pyshim/sitecustomize.py" <<'EOF'
import io, json, os, urllib.error, urllib.parse, urllib.request

def urlopen(url, data=None, timeout=None, *args, **kwargs):
    state = os.environ["STUB_STATE"]
    form = {k: v[0] for k, v in urllib.parse.parse_qs((data or b"").decode()).items()}
    with open(os.path.join(state, "verify_requests.log"), "a") as f:
        f.write(json.dumps({"url": url, "form": form}) + "\n")
    if os.path.exists(os.path.join(state, "verify_fails")):
        raise urllib.error.URLError("unreachable")
    token = form.get("token", "")
    if token.startswith("glrt-") and not form.get("system_id"):
        raise urllib.error.HTTPError(url, 400, "system_id is missing", {}, io.BytesIO(b"{}"))
    path = os.path.join(state, "token_id")
    rid = int(open(path).read()) if os.path.exists(path) else int(token.rsplit("-", 1)[1])
    return io.BytesIO(json.dumps({"id": rid}).encode())

urllib.request.urlopen = urlopen
EOF
chmod +x "${SHIM_DIR}/oc" "${SHIM_DIR}/virtctl" "${SHIM_DIR}/curl"

# new_state — fresh simulated cluster/VM/GitLab for one scenario.
new_state() {
  rm -rf "${STATE}"
  STATE=$(mktemp -d)
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
  RUN_OUT=$(env -u GL_TOKEN -u RUNNER_TOKEN -u PROJECT_ID -u GROUP_ID -u GITHUB_ENV \
    PATH="${SHIM_DIR}:${PATH}" STUB_STATE="${STATE}" \
    NAMESPACE=runners RUNNER_IMAGE=ghcr.io/example/runner:v1 \
    SSH_PUBLIC_KEY="ssh-ed25519 AAAAtest test@example" \
    "${assigns[@]}" bash "${CREATE_OCP}" "$@" 2>&1 </dev/null)
  RUN_RC=$?
  set -e
}

runner_count() {
  python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "${STATE}/runners.json" 2>/dev/null || echo 0
}
# env_count — number of times setup.sh was run (one captured .env per run).
env_count() {
  local f n=0
  for f in "${STATE}"/env.*; do
    [ -e "${f}" ] && n=$((n + 1))
  done
  echo "${n}"
}
post_count() {
  grep -c '^POST ' "${STATE}/curl.log" 2>/dev/null || true
}

SELF_HOSTED="https://gitlab.internal.example"
SHARED=(RUNNER_TOKEN=glrt-shared "GITLAB_URL=${SELF_HOSTED}")
INDIVIDUAL=(GL_TOKEN=glpat-test GROUP_ID=42 "GITLAB_URL=${SELF_HOSTED}")

echo "== self-hosted GitLab, shared runner token =="
new_state
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -eq 0 ]; then
  pass "fresh create succeeds in one invocation"
else
  fail "fresh create failed (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
if [ -f "${STATE}/manifest" ] && grep -Fq 'name: fullsend-gitlab-runner-05' "${STATE}/manifest" \
  && grep -Fq 'download[.]example' "${STATE}/manifest"; then
  pass "VM 05 created from vm.yaml with the repo bootstrap"
else
  fail "VM manifest missing or lacks the vm.yaml bootstrap"
fi
if [ ! -s "${STATE}/curl.log" ]; then
  pass "no GitLab API calls — the shared token creates no registration"
else
  fail "shared-token create called the GitLab API: $(tr '\n' '|' < "${STATE}/curl.log")"
fi
if grep -qx "GITLAB_URL='${SELF_HOSTED}'" "${STATE}/env.0" \
  && grep -qx "REGISTRATION_TOKEN='glrt-shared'" "${STATE}/env.0"; then
  pass "setup.sh registers the shared token with the supplied GITLAB_URL"
else
  fail "setup.sh env does not target ${SELF_HOSTED} with the shared token: $(tr '\n' '|' < "${STATE}/env.0")"
fi
if grep -rFq 'gitlab.com' "${STATE}" || grep -Fq 'gitlab.com' <<< "${RUN_OUT}"; then
  fail "gitlab.com appears in a self-hosted-only run"
else
  pass "gitlab.com is never contacted or configured"
fi
if [ "$(env_count)" -eq 1 ] && ! grep -q 'cloud-init single' "${STATE}/virtctl.log"; then
  pass "setup.sh runs once; no cloud-init repair on a healthy boot"
else
  fail "unexpected setup/repair calls: $(tr '\n' '|' < "${STATE}/virtctl.log")"
fi

echo "== cloud-init package failure =="
new_state
echo 1 > "${STATE}/cloud_init_rc"
touch "${STATE}/packages_missing"
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -eq 0 ] && grep -Fxq 'sudo sh -es' "${STATE}/virtctl.log" \
  && grep -Fq 'sudo cloud-init single --name package_update_upgrade_install --frequency always' "${STATE}/virtctl.log" \
  && ! grep -q 'cloud-init single --name bootcmd' "${STATE}/virtctl.log"; then
  pass "missing base packages are repaired by pushing the repo repair + re-running the package module"
else
  fail "cloud-init package failure not repaired in one invocation (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

new_state
echo 1 > "${STATE}/cloud_init_rc"
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -eq 0 ] && ! grep -q 'cloud-init single' "${STATE}/virtctl.log" \
  && grep -Fq 'base packages are installed — continuing' <<< "${RUN_OUT}"; then
  pass "a non-package cloud-init error with packages present continues"
else
  fail "cloud-init error with packages present should continue (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

new_state
echo 1 > "${STATE}/cloud_init_rc"
touch "${STATE}/packages_missing" "${STATE}/repair_fails"
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume 05' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq 0 ]; then
  pass "unrepairable packages stop before setup and print the --resume recovery"
else
  fail "unrepairable packages should stop with a --resume hint (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# An older VM's user data still has the broken repo config and --resume does
# not re-apply the manifest: the repair must come from the current vm.yaml.
new_state
touch "${STATE}/vm_exists" "${STATE}/packages_missing" "${STATE}/old_userdata"
echo 1 > "${STATE}/cloud_init_rc"
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ -f "${STATE}/repo_fixed" ] && [ "$(env_count)" -eq 1 ]; then
  pass "resume repairs a VM with old user data using the current vm.yaml repair"
else
  fail "old-user-data VM not recovered on resume (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo "== existing VM =="
new_state
touch "${STATE}/vm_exists"
run_create "${SHARED[@]}" 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume 05' <<< "${RUN_OUT}" \
  && grep -Fq './delete-openshift-vm.sh fullsend-gitlab-runner-05 (which drains in-flight jobs)' <<< "${RUN_OUT}"; then
  pass "create refuses an existing VM and names --resume and drain-safe delete"
else
  fail "existing-VM error should name --resume and delete (rc=${RUN_RC}): ${RUN_OUT}"
fi

new_state
run_create "${SHARED[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'nothing to resume' <<< "${RUN_OUT}"; then
  pass "--resume refuses a VM that does not exist"
else
  fail "--resume on a missing VM should fail (rc=${RUN_RC}): ${RUN_OUT}"
fi

run_create "${SHARED[@]}" --resume
if [ "${RUN_RC}" -ne 0 ] && grep -Fq -- '--resume requires the NUMBER' <<< "${RUN_OUT}"; then
  pass "--resume requires a VM number"
else
  fail "--resume without NUMBER should fail (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== --resume, shared runner token =="
new_state
touch "${STATE}/vm_exists" "${STATE}/packages_missing"
echo 1 > "${STATE}/cloud_init_rc"
run_create "${SHARED[@]}" --resume 05
first_rc="${RUN_RC}"
run_create "${SHARED[@]}" --resume 05
if [ "${first_rc}" -eq 0 ] && [ "${RUN_RC}" -eq 0 ] && [ ! -f "${STATE}/manifest" ] \
  && [ ! -s "${STATE}/curl.log" ]; then
  pass "resume finishes an unconfigured VM, is repeatable, and creates nothing"
else
  fail "shared-token resume (rc=${first_rc}, ${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo "== --resume, GL_TOKEN registration =="
new_state
touch "${STATE}/vm_exists"
echo 1 > "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && [ "$(runner_count)" -eq 0 ] && [ -f "${STATE}/vm_config" ]; then
  pass "a failed run deregisters the runner it created (VM keeps a stale config)"
else
  fail "failed run should roll back its runner (rc=${RUN_RC}, runners=$(runner_count))"
fi

rm -f "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(runner_count)" -eq 1 ] \
  && grep -qx 'sudo rm -f /etc/gitlab-runner/config.toml' "${STATE}/virtctl.log" \
  && [ "$(cat "${STATE}/vm_config")" = "glrt-new-2" ]; then
  pass "resume replaces the stale config with exactly one new registration"
else
  fail "resume after rollback (rc=${RUN_RC}, runners=$(runner_count)): $(tail -5 <<< "${RUN_OUT}")"
fi

posts_before=$(post_count)
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && [ "$(runner_count)" -eq 1 ] && [ "$(post_count)" -eq "${posts_before}" ] \
  && grep -Fq 'reusing runner ID 2' <<< "${RUN_OUT}"; then
  pass "repeating resume reuses the VM's runner (no duplicate registration)"
else
  fail "repeat resume should reuse runner 2 (rc=${RUN_RC}, runners=$(runner_count)): $(tail -5 <<< "${RUN_OUT}")"
fi
if ! grep -v "^[A-Z]* ${SELF_HOSTED}/" "${STATE}/curl.log" | grep -q .; then
  pass "every GitLab API call targets the supplied GITLAB_URL"
else
  fail "GitLab API calls outside ${SELF_HOSTED}: $(tr '\n' '|' < "${STATE}/curl.log")"
fi

# The VM-side probe parses config.toml, so a reformatted header still counts
# as this VM's registration (the stub runs the script's real probe here).
printf '  [[ runners ]]\n  id = 2\n  url = "%s"\n  token = "glrt-new-2"\n' "${SELF_HOSTED}" > "${STATE}/config.toml"
rm -f "${STATE}/verify_requests.log"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && grep -Fq 'reusing runner ID 2' <<< "${RUN_OUT}"; then
  pass "resume recognises a runner config with an indented, spaced [[ runners ]] header"
else
  fail "indented [[ runners ]] header should count as registered (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
# The verify request the VM sends must carry the configured token and the
# runner manager's system ID from /etc/gitlab-runner/.runner_system_id
# (GitLab rejects a glrt- token without it).
if [ "$(wc -l < "${STATE}/verify_requests.log")" -eq 1 ] \
  && python3 - "${STATE}/verify_requests.log" "${SELF_HOSTED}" <<'PY'
import json, sys
req = json.loads(open(sys.argv[1]).readline())
assert req["url"] == sys.argv[2] + "/api/v4/runners/verify", req
assert req["form"] == {"token": "glrt-new-2", "system_id": "r_vmsystem01"}, req
PY
then
  pass "VM-side verify posts the configured token and the VM's runner system ID"
else
  fail "verify request lacks token/system_id: $(cat "${STATE}/verify_requests.log" 2>/dev/null)"
fi
rm -f "${STATE}/config.toml"

# No system ID on the VM (gitlab-runner never wrote one): the token check
# cannot run, so resume fails explicitly and stops the running service.
printf '[[runners]]\n  id = 2\n  url = "%s"\n  token = "glrt-new-2"\n' "${SELF_HOSTED}" > "${STATE}/config.toml"
touch "${STATE}/no_system_id" "${STATE}/service_active"
env_before=$(env_count)
rm -f "${STATE}/verify_requests.log"
run_create "${INDIVIDUAL[@]}" --resume 05
rm -f "${STATE}/no_system_id"
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'could not be confirmed as belonging to runner ID 2' <<< "${RUN_OUT}" \
  && grep -Fq '.runner_system_id' <<< "${RUN_OUT}" && [ ! -e "${STATE}/verify_requests.log" ] \
  && [ "$(env_count)" -eq "${env_before}" ] && [ ! -f "${STATE}/service_active" ]; then
  pass "resume fails explicitly and stops the service when the VM has no runner system ID"
else
  fail "missing system ID should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

posts_refused=$(post_count)
deletes_refused=$(grep -c '^DELETE ' "${STATE}/curl.log")
printf '[[runners]]\n  id = 99\n  url = "%s"\n' "${SELF_HOSTED}" > "${STATE}/config.toml"
env_before=$(env_count)
touch "${STATE}/service_active"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'is configured with runner ID' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq "${env_before}" ] && [ ! -f "${STATE}/service_active" ]; then
  pass "resume refuses a VM configured with a different runner than GitLab's and stops the running service"
else
  fail "runner ID mismatch should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
rm -f "${STATE}/config.toml"

# Runner IDs are instance-local: a config registered on another GitLab
# instance must be refused before any ID lookup, not judged stale because its
# ID is unknown here (which would register a duplicate and delete the config).
env_before=$(env_count)
posts_foreign=$(post_count)
curls_foreign=$(wc -l < "${STATE}/curl.log")
rms_foreign=$(grep -c 'sudo rm -f /etc/gitlab-runner/config.toml' "${STATE}/virtctl.log" || true)
printf '[[runners]]\n  id = 9999\n  url = "https://gitlab.other.example"\n' > "${STATE}/config.toml"
touch "${STATE}/service_active"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq "configured for GitLab instance 'https://gitlab.other.example'" <<< "${RUN_OUT}" \
  && [ "$(post_count)" -eq "${posts_foreign}" ] && [ "$(wc -l < "${STATE}/curl.log")" -eq "${curls_foreign}" ] \
  && [ "$(env_count)" -eq "${env_before}" ] && [ ! -f "${STATE}/service_active" ] \
  && [ -f "${STATE}/config.toml" ] \
  && [ "$(grep -c 'sudo rm -f /etc/gitlab-runner/config.toml' "${STATE}/virtctl.log" || true)" -eq "${rms_foreign}" ]; then
  pass "resume refuses a config registered on another GitLab instance before any ID lookup, keeping the config"
else
  fail "foreign-URL config should be refused before lookups (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
rm -f "${STATE}/config.toml"

# A config whose ID matches GitLab's runner but whose token authenticates a
# different runner (or cannot be verified) is not this registration.
for token_case in other-runner unverifiable; do
  env_before=$(env_count)
  touch "${STATE}/service_active"
  if [ "${token_case}" = "other-runner" ]; then
    echo 77 > "${STATE}/token_id"
  else
    touch "${STATE}/verify_fails"
  fi
  run_create "${INDIVIDUAL[@]}" --resume 05
  rm -f "${STATE}/token_id" "${STATE}/verify_fails"
  if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'could not be confirmed as belonging to runner ID 2' <<< "${RUN_OUT}" \
    && [ "$(env_count)" -eq "${env_before}" ] && [ ! -f "${STATE}/service_active" ]; then
    pass "resume refuses a config whose token is ${token_case} and stops the running service"
  else
    fail "token bound to another runner (${token_case}) should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
  fi
done

env_before=$(env_count)
touch "${STATE}/service_active"
run_create "${INDIVIDUAL[@]}" RUNNER_ACCESS_LEVEL=ref_protected --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no longer matches' <<< "${RUN_OUT}" \
  && grep -Fq 'access_level' <<< "${RUN_OUT}" && [ "$(env_count)" -eq "${env_before}" ] \
  && [ ! -f "${STATE}/service_active" ]; then
  pass "resume refuses a runner registered with a different access level and stops the running service"
else
  fail "access-level mismatch should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

env_before=$(env_count)
touch "${STATE}/service_active"
run_create GL_TOKEN=glpat-test GROUP_ID=43 "GITLAB_URL=${SELF_HOSTED}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no longer matches' <<< "${RUN_OUT}" \
  && grep -Fq 'does not belong to group 43' <<< "${RUN_OUT}" && [ "$(env_count)" -eq "${env_before}" ] \
  && [ ! -f "${STATE}/service_active" ]; then
  pass "resume refuses a runner registered for a different group and stops the running service"
else
  fail "scope mismatch should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
# Tags are mutable in GitLab: a runner whose tags were edited is still found
# by its description (no duplicate registration), and reuse is refused because
# the requested tag is no longer among its tags.
runners_tagged=$(cat "${STATE}/runners.json")
python3 - "${STATE}/runners.json" <<'PY'
import json, sys
runners = json.load(open(sys.argv[1]))
for r in runners:
    r["tag_list"] = ["edited-tag"]
json.dump(runners, open(sys.argv[1], "w"))
PY
env_before=$(env_count)
posts_tags=$(post_count)
touch "${STATE}/service_active"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no longer matches' <<< "${RUN_OUT}" \
  && grep -Fq 'is not among the runner tags' <<< "${RUN_OUT}" \
  && [ "$(post_count)" -eq "${posts_tags}" ] && [ "$(runner_count)" -eq 1 ] \
  && [ "$(env_count)" -eq "${env_before}" ] && [ ! -f "${STATE}/service_active" ]; then
  pass "resume finds a runner whose tags were edited, registers no duplicate, and refuses reuse"
else
  fail "edited-tag runner should be found and refused, not duplicated (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
printf '%s\n' "${runners_tagged}" > "${STATE}/runners.json"

# Runner details that omit (or empty) the membership list give no positive
# evidence of scope, so reuse is refused for both group and project runners.
runners_backup=$(cat "${STATE}/runners.json")
for membership in missing empty; do
  python3 - "${STATE}/runners.json" "${membership}" <<'PY'
import json, sys
path, mode = sys.argv[1:]
runners = json.load(open(path))
for r in runners:
    if mode == "missing":
        r.pop("groups", None)
    else:
        r["groups"] = []
json.dump(runners, open(path, "w"))
PY
  env_before=$(env_count)
  run_create "${INDIVIDUAL[@]}" --resume 05
  if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no longer matches' <<< "${RUN_OUT}" \
    && grep -Fq 'cannot confirm it belongs to group 42' <<< "${RUN_OUT}" \
    && [ "$(env_count)" -eq "${env_before}" ]; then
    pass "resume refuses a group runner whose details have ${membership} group membership"
  else
    fail "${membership} group membership should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
  fi
done
python3 - "${STATE}/runners.json" <<'PY'
import json, sys
runners = json.load(open(sys.argv[1]))
for r in runners:
    r["runner_type"] = "project_type"
    r.pop("groups", None)
json.dump(runners, open(sys.argv[1], "w"))
PY
env_before=$(env_count)
run_create GL_TOKEN=glpat-test PROJECT_ID=5 "GITLAB_URL=${SELF_HOSTED}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'cannot confirm it belongs to project 5' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq "${env_before}" ]; then
  pass "resume refuses a project runner whose details omit project membership"
else
  fail "missing project membership should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
printf '%s\n' "${runners_backup}" > "${STATE}/runners.json"

# patch_runners JSON — merge JSON into every runner in the stub's GitLab
# (a null value deletes the key).
patch_runners() {
  python3 - "${STATE}/runners.json" "$1" <<'PY'
import json, sys
path, patch = sys.argv[1], json.loads(sys.argv[2])
runners = json.load(open(path))
for r in runners:
    for k, v in patch.items():
        if v is None:
            r.pop(k, None)
        else:
            r[k] = v
json.dump(runners, open(path, "w"))
PY
}
# expect_resume_refused DESC EXPECTED_REASON [create args] — resume must be
# refused for the reason, without running setup.sh, and stop the service.
expect_resume_refused() {
  local desc="$1" reason="$2" env_before
  shift 2
  env_before=$(env_count)
  touch "${STATE}/service_active"
  run_create "$@" --resume 05
  if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'no longer matches' <<< "${RUN_OUT}" \
    && grep -Fq -- "${reason}" <<< "${RUN_OUT}" && [ "$(env_count)" -eq "${env_before}" ] \
    && [ ! -f "${STATE}/service_active" ]; then
    pass "resume refuses ${desc} and stops the running service"
  else
    fail "${desc} should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
  fi
}

# A group runner that now runs untagged jobs (or has no run_untagged value at
# all) is not the registration fresh provisioning created.
patch_runners '{"run_untagged": true}'
expect_resume_refused "a group runner that runs untagged jobs" "run_untagged is True" "${INDIVIDUAL[@]}"
patch_runners '{"run_untagged": null}'
expect_resume_refused "a group runner with no run_untagged value" "run_untagged is None" "${INDIVIDUAL[@]}"
patch_runners '{"run_untagged": "false"}'
expect_resume_refused "a group runner with a malformed run_untagged value" "run_untagged is 'false'" "${INDIVIDUAL[@]}"
printf '%s\n' "${runners_backup}" > "${STATE}/runners.json"

# Project runners must also stay locked to exactly the requested project.
PROJECT_RUNNER=(GL_TOKEN=glpat-test PROJECT_ID=5 "GITLAB_URL=${SELF_HOSTED}")
patch_runners '{"runner_type": "project_type", "groups": null, "projects": [{"id": 5}], "locked": true}'
env_before=$(env_count)
run_create "${PROJECT_RUNNER[@]}" --resume 05
if [ "${RUN_RC}" -eq 0 ] && grep -Fq 'reusing runner ID 2' <<< "${RUN_OUT}" \
  && [ "$(env_count)" -eq $((env_before + 1)) ]; then
  pass "resume reuses a locked project runner assigned to exactly the requested project"
else
  fail "a matching locked project runner should be reused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi
patch_runners '{"projects": [{"id": 5}, {"id": 6}]}'
expect_resume_refused "a project runner assigned to an additional project" "requested only 5" "${PROJECT_RUNNER[@]}"
patch_runners '{"projects": [{"id": 5}], "locked": false}'
expect_resume_refused "an unlocked project runner" "locked is False" "${PROJECT_RUNNER[@]}"
patch_runners '{"locked": null}'
expect_resume_refused "a project runner with no locked value" "locked is None" "${PROJECT_RUNNER[@]}"
patch_runners '{"locked": "true"}'
expect_resume_refused "a project runner with a malformed locked value" "locked is 'true'" "${PROJECT_RUNNER[@]}"
printf '%s\n' "${runners_backup}" > "${STATE}/runners.json"

if [ "$(runner_count)" -eq 1 ] && [ "$(post_count)" -eq "${posts_refused}" ] \
  && [ "$(grep -c '^DELETE ' "${STATE}/curl.log")" -eq "${deletes_refused}" ]; then
  pass "refused reuse neither registers nor deregisters anything"
else
  fail "refused reuse changed registrations: $(tr '\n' '|' < "${STATE}/curl.log")"
fi

touch "${STATE}/probe_fails" "${STATE}/service_active"
run_create "${INDIVIDUAL[@]}" --resume 05
rm -f "${STATE}/probe_fails"
if [ "${RUN_RC}" -ne 0 ] && grep -Fq 'could not read the runner config' <<< "${RUN_OUT}" \
  && [ "$(runner_count)" -eq 1 ] && [ ! -f "${STATE}/service_active" ]; then
  pass "resume fails closed and stops the service when the VM's runner config cannot be read"
else
  fail "unreadable VM config should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

deletes_before=$(grep -c '^DELETE ' "${STATE}/curl.log" || true)
echo 1 > "${STATE}/setup_rc"
run_create "${INDIVIDUAL[@]}" --resume 05
deletes_after=$(grep -c '^DELETE ' "${STATE}/curl.log" || true)
if [ "${RUN_RC}" -ne 0 ] && [ "$(runner_count)" -eq 1 ] && [ "${deletes_after}" -eq "${deletes_before}" ]; then
  pass "a failed resume never deregisters a runner it did not create"
else
  fail "failed reuse-resume touched the existing runner (rc=${RUN_RC}): $(tr '\n' '|' < "${STATE}/curl.log")"
fi

# A full page on every page (the runners stub ignores paging) means the scan
# hit its page cap: "not found" cannot be trusted, so resume must refuse.
new_state
touch "${STATE}/vm_exists"
python3 -c 'import json; print(json.dumps([{"id": i, "description": "runners/other"} for i in range(100, 200)]))' > "${STATE}/runners.json"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && [ "$(post_count)" -eq 0 ] && [ "$(env_count)" -eq 0 ] \
  && grep -Fq 'GitLab API lookup failed' <<< "${RUN_OUT}"; then
  pass "resume fails closed when the runner lookup hits its page cap"
else
  fail "incomplete runner scan should fail closed (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

# A 2xx body that is not an array of runner objects with integer IDs is not
# "no runners": resume must fail closed instead of registering a duplicate.
for bad_body in '{}' '""' '[{"description": "runners/fullsend-gitlab-runner-05"}]'; do
  new_state
  touch "${STATE}/vm_exists" "${STATE}/service_active"
  printf '%s\n' "${bad_body}" > "${STATE}/runners.json"
  run_create "${INDIVIDUAL[@]}" --resume 05
  if [ "${RUN_RC}" -ne 0 ] && [ "$(post_count)" -eq 0 ] && [ "$(env_count)" -eq 0 ] \
    && grep -Fq 'GitLab API lookup failed' <<< "${RUN_OUT}" && [ ! -f "${STATE}/service_active" ]; then
    pass "resume fails closed and stops the service on a malformed runner list (${bad_body})"
  else
    fail "malformed runner list ${bad_body} should fail closed (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
  fi
done

# A VM config whose runner ID still exists in GitLab (under another
# description) is not stale: replacing it would orphan that registration.
new_state
touch "${STATE}/vm_exists" "${STATE}/service_active"
printf '[[runners]]\n  id = 2\n  url = "%s"\n' "${SELF_HOSTED}" > "${STATE}/config.toml"
echo '[{"id": 2, "description": "someone/else", "tag_list": ["fullsend-gitlab-runner"]}]' > "${STATE}/runners.json"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && [ "$(post_count)" -eq 0 ] && [ "$(env_count)" -eq 0 ] \
  && grep -Fq 'could not be confirmed deleted' <<< "${RUN_OUT}" \
  && ! grep -Fq 'sudo rm -f /etc/gitlab-runner/config.toml' "${STATE}/virtctl.log" \
  && [ ! -f "${STATE}/service_active" ]; then
  pass "resume does not replace a config whose runner still exists in GitLab"
else
  fail "config with a live runner must not be treated as stale (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

new_state
touch "${STATE}/vm_exists"
echo '[{"id": 7, "description": "runners/fullsend-gitlab-runner-05"}]' > "${STATE}/runners.json"
run_create "${INDIVIDUAL[@]}" --resume 05
if [ "${RUN_RC}" -ne 0 ] && [ "$(post_count)" -eq 0 ] \
  && grep -Fq 'its token cannot be retrieved' <<< "${RUN_OUT}"; then
  pass "resume refuses when the VM never received its runner's token"
else
  fail "registered-but-unconfigured VM should be refused (rc=${RUN_RC}): $(tail -5 <<< "${RUN_OUT}")"
fi

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed" >&2
  exit 1
fi
echo "All create-openshift-vm.sh tests passed"
