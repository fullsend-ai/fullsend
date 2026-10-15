# GitLab Runner VM

Provisions GitLab Runner VMs on OpenShift Virtualization or GCE (Google
Compute Engine) with a Podman custom executor and OpenShell gateway for
fullsend agent jobs.

## Architecture

Each runner VM runs:
- **gitlab-runner** (custom executor) — receives CI jobs from GitLab.
  The runner is a systemd *system* service running as the VM user, so it
  does not go through `pam_systemd` and does not inherit a login session.
  `setup.sh` enables lingering for that user and writes `XDG_RUNTIME_DIR`
  / `DBUS_SESSION_BUS_ADDRESS` into the gitlab-runner drop-in with the
  runner user's numeric UID (resolved at setup time — systemd `%U` on a
  system-scope unit expands to 0, not `User=`). `systemctl --user`
  (OpenShell gateway start/stop) can then reach the user bus.
  `executor/gateway.sh` also pins those variables itself, so a job still
  works if the unit environment is missing (#7453, #7696).
- **Podman** (rootless) — creates per-job containers. A user systemd
  timer (`fullsend-podman-prune.timer`) plus a prepare/cleanup hook
  reclaim unused images and stopped leftovers so the ~30 GiB root disk
  cannot fill with superseded job layers (#7663). In-flight `runner-*` /
  `openshell-*` containers are never stopped; the warm-cache runner and
  supervisor images listed in `~/.config/fullsend-gitlab-runner/keep-images`
  are never removed.
- **OpenShell gateway** — started per job in `prepare.sh`, torn down in
  `cleanup.sh`. The VM does **not** keep a long-lived `systemd --user`
  gateway: that accumulated a stale profile registry, a baked-in OpenShell
  version, and leaked sandboxes (#7218). Each job recreates the gateway
  with an empty registry, matching GitHub Actions (fresh, version-matched
  install, throw the runner away). The ~6 GB image cache stays warm; only
  the CLI (~39 MB) and a version-skewed supervisor (~30 MB) are fetched
  when the job's pin differs from the host.

Job containers use `--network=host` to reach the gateway. An OCI
`createRuntime` hook injects the host CA trust bundle into every container
(Debian and RHEL-family layouts) so the OpenShell supervisor and sandboxed
processes can verify internal TLS endpoints. That hook is the **sandbox-host**
half of private-CA support; GitLab job containers separately consume
`CI_SERVER_TLS_CA_FILE` (see [Private CA (self-hosted GitLab)](../../docs/guides/getting-started/operations.md#private-ca-self-hosted-gitlab)).
The Kubernetes executor does not run this hook — do not treat a job-local
`CI_SERVER_TLS_CA_FILE` path as available on a remote sandbox host.
Gateway mTLS credentials are mounted read-only from the runner user's
OpenShell config. `prepare.sh` reaps leftover `openshell-*` / `openshell.managed`
containers from an abruptly-killed prior job before starting the new gateway.

This is a deployment variant of the container isolation model described in
ADR-0036. It uses a Podman custom executor instead of Docker/Kubernetes
executors but maintains equivalent container-level isolation for agent jobs.

See also:
- [ADR 0036: Agent Execution Sandbox](../../docs/ADRs/0036-agent-execution-sandbox.md)

## Quick start — OpenShift Virtualization

> **Requires:** `oc`, `virtctl` (client >= 1.5 — ships with OpenShift Virtualization >= 4.19),
> `python3`, and a local `ssh` binary (virtctl wraps it via ProxyCommand).

```bash
# 1. Create and provision a VM — group-scoped runner (recommended):
GL_TOKEN=glpat-xxx GROUP_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh

# Or project-scoped runner (for single-project deployments):
GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh

# Or join an existing runner pool (runner-hub — multiple VMs, one registration):
RUNNER_TOKEN=glrt-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh 05

# 2. Delete a VM (individual runner — drains, deregisters, deletes):
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  ./delete-openshift-vm.sh fullsend-gitlab-runner-01

# Or delete a fleet VM (no GL_TOKEN — drains and deletes, does not deregister):
RUNNER_TOKEN=glrt-xxx NAMESPACE=my-namespace \
  ./delete-openshift-vm.sh fullsend-gitlab-runner-05

# Or skip the drain step and delete immediately:
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  ./delete-openshift-vm.sh --no-drain fullsend-gitlab-runner-01

# 3. List VMs:
NAMESPACE=my-namespace ./delete-openshift-vm.sh --list

# 4. Finish a VM whose provisioning failed (same environment as the create):
RUNNER_TOKEN=glrt-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh --resume 05
```

### Recovering from a failed provisioning run

The VM's cloud-init `bootcmd` makes the Fedora repos usable before cloud-init
installs packages: it switches `metalink=` to `baseurl=`, replaces the
`download.example` placeholder that recent Fedora cloud images ship, rewrites
the `dl.fedoraproject.org` base URLs from HTTP to HTTPS (some clusters block
HTTP egress), and disables the metalink-only OpenH264 repo. `setup.sh` applies
the same repair, including to repos already switched to an HTTP `baseurl=`. If
the base packages are still
missing after cloud-init, `create-openshift-vm.sh` sends the current `vm.yaml`
repair commands to the VM (so a VM created from an older template is fixed too)
and re-runs the package module once before running `setup.sh`, which then
verifies the service and that the runner is registered with `GITLAB_URL` (and
nowhere else). A reused runner config for a different GitLab, or with more
than one runner, is rejected and leaves `gitlab-runner` stopped.

If a run still fails after the VM exists, fix the cause and re-run with
`--resume NUMBER` and the same environment. It never creates a second VM, and
never creates a duplicate registration:

- `RUNNER_TOKEN` (shared pool): no registration is created; `setup.sh` is
  re-run with the pool token.
- `GL_TOKEN`: the runner registered for `NAMESPACE/vm-name` is reused if the VM
  holds its config (the config's runner ID must be that runner's, the VM's
  token must verify as that runner using the system ID in
  `/etc/gitlab-runner/.runner_system_id`, and GitLab must still report the
  requested scope, `RUNNER_ACCESS_LEVEL` and `RUNNER_TAG` for it, with
  `run_untagged` false and, for a project runner, locked to only the requested
  project — otherwise `--resume` refuses and stops a running `gitlab-runner`,
  as it also does when it cannot read the VM's runner config or look up the
  registration).
  The runner is found by its `NAMESPACE/vm-name` description, not by tag, so
  edited tags never cause a duplicate. If none is registered (a failed run
  deregisters the runner it created), a new one is registered and any stale
  config on the VM is replaced — but only when GitLab confirms the config's
  runner ID is gone. If several are registered, or one is registered but the VM never
  received its token, `--resume` refuses — delete the VM with
  `./delete-openshift-vm.sh` (which deregisters it) and create it again.

`--resume` is safe to repeat. Recreating the VM (delete, then create) remains
the compliance path for a VM that was configured and served jobs.

## Quick start — GCE (Google Compute Engine)

> **Requires:** `gcloud` CLI (authenticated), `python3`, `curl`.
> By default, VMs have no public IP — SSH access is tunneled via [IAP](https://cloud.google.com/iap/docs/using-tcp-forwarding)
> (Cloud IAP API must be enabled; operator needs `roles/iap.tunnelResourceAccessor`).
> The VPC subnet must have [Cloud NAT](https://cloud.google.com/nat/docs/overview)
> configured — without it, VMs created with `--no-address` cannot reach
> package mirrors or container registries and `dnf install` will fail.
> Set `GCP_USE_IAP=false` to create the VM with an external IP and SSH directly.
> VMs get a 30 GiB boot disk whose root filesystem is grown and verified
> during provisioning — see [GCE boot disk size and repair](#gce-boot-disk-size-and-repair).

```bash
# 1. Create and provision a VM — group-scoped runner (recommended):
GL_TOKEN=glpat-xxx GROUP_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh

# Or project-scoped runner (for single-project deployments):
GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh

# Or join an existing runner pool (runner-hub — multiple VMs, one registration):
RUNNER_TOKEN=glrt-xxx \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh 05

# 2. Delete a VM (individual runner — drains, deregisters, deletes):
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  ./delete-gcp-vm.sh fullsend-gitlab-runner-01

# Or delete a fleet VM (no GL_TOKEN — drains and deletes, does not deregister):
RUNNER_TOKEN=glrt-xxx GCP_PROJECT=my-gcp-project \
  ./delete-gcp-vm.sh fullsend-gitlab-runner-05

# Or skip the drain step and delete immediately:
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  ./delete-gcp-vm.sh --no-drain fullsend-gitlab-runner-01

# 3. List VMs:
GCP_PROJECT=my-gcp-project ./delete-gcp-vm.sh --list

# 4. Finish or repair an existing VM (same environment as the create):
RUNNER_TOKEN=glrt-xxx \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh --resume 05
```

### Resuming or repairing a GCE runner

`create-gcp-vm.sh --resume NUMBER` re-runs provisioning on the existing VM
`fullsend-gitlab-runner-NUMBER` in `GCP_PROJECT` / `GCP_ZONE`. It finishes a
VM whose create failed part-way, or brings a working runner up to the
current provisioning. It reuses the steps and files of a fresh create:
package install, root filesystem growth, staging `hack/gitlab-runner-vm/`,
and `setup.sh`, followed by its verification.

**Inputs.** Pass the same environment as the create: `GITLAB_URL`,
`RUNNER_IMAGE`, `GCP_PROJECT`, `GCP_ZONE`, and either `RUNNER_TOKEN` or
`GL_TOKEN` with `PROJECT_ID` / `GROUP_ID`. `RUNNER_USER` is optional (see
below). `--resume` never creates or starts a VM: it refuses if the VM does
not exist or is not running.

**Service user.** `gcloud compute ssh` logs in as a per-operator account,
so the login user is not necessarily the account the runner runs as. The
script works out the existing service user from the gitlab-runner systemd
drop-in (`User=`) and the owner of `/etc/gitlab-runner`, then stages files
and runs `setup.sh` as that user. It uses `sudo -u` with that user's
`HOME`, `XDG_RUNTIME_DIR`, and user D-Bus, after enabling lingering. This
keeps rootless Podman storage, the executor paths and the workspace with
the same account. On a VM that was never configured, the service user is
`RUNNER_USER` if set, otherwise the login user. The script refuses if:

- `RUNNER_USER` names a different account from the existing service user,
  because it does not move a runner between accounts;
- the drop-in and `/etc/gitlab-runner` disagree about the service user;
- the service user is `root`;
- the service user has no passwordless `sudo`, which `setup.sh` needs.

**Disk.** If the boot disk is smaller than 30 GiB, it is resized to 30 GiB
online. A larger disk is never shrunk. Either way, `grow-root-fs.sh` then
grows the root filesystem to fill the disk and verifies it.

**Job interruption.** `setup.sh` stops gitlab-runner while it reconfigures
and restarts it at the end. A job running on the VM at that moment is
interrupted. Resume when the runner is idle, or pause it in GitLab and
wait for running jobs to finish first.

**Rerun semantics.** `--resume` is idempotent and safe to repeat. On a
healthy runner it changes nothing beyond restarting the service: setup
leaves an already-correct config untouched and the runner keeps its
registration, images and workspace data.

- With `RUNNER_TOKEN`, the shared token is re-applied. Nothing is
  registered or deregistered, so a shared fleet runner is never removed.
- With `GL_TOKEN`, the existing registration for this VM (described as
  `<GCP_PROJECT>/<vm-name>`) is reused when the VM's config already holds
  its token. A new runner is registered only if none exists for the VM. If
  that run fails, only the runner it just registered is deregistered. A
  stale config is replaced only when GitLab confirms its runner ID is gone;
  the old file is kept on the VM as
  `/etc/gitlab-runner/config.toml.stale-<timestamp>` (root-only) so settings
  you added by hand can be recovered.

`--resume` refuses rather than guessing when it finds:

- a config registered with a different GitLab instance;
- several runners registered for the VM;
- a config whose runner ID does not match this VM's registration;
- a config with a `[[runners]]` entry that has no positive integer `id`.

A runner token is verified from the VM over HTTPS before setup runs, so a
stale CA trust on the VM (for example after a GitLab CA rotation) also makes
`--resume` refuse; refresh the VM's CA certificates manually or recreate the
VM.

A runner that is registered in GitLab but whose token never reached the VM
cannot be recovered, because GitLab does not show the token again. For
that, and for any refusal, drain and delete the VM with
`./delete-gcp-vm.sh` (which deregisters an individual runner) and create it
again.

**Differences from OpenShift `--resume`.**

- The GCE script resizes an undersized boot disk and grows the root
  filesystem.
- It detects the service user instead of using a fixed `VM_USER`.
- It connects with `gcloud compute ssh` (through IAP unless
  `GCP_USE_IAP=false`) and installs packages with `dnf`, where OpenShift
  waits for and repairs cloud-init.
- It checks the GCE instance status first and refuses a VM that is not
  running (start it with `gcloud compute instances start`).

Recreating the VM (delete, then create) remains the compliance path for a
VM that was configured and served jobs.

## Environment variables

### Shared

| Variable | Required | Default | Description |
|---|---|---|---|
| `RUNNER_TOKEN` | yes (create)² | — | GitLab runner authentication token (`glrt-*`). In create mode, the VM joins an existing runner pool and `GL_TOKEN` / `PROJECT_ID` / `GROUP_ID` are not required. In delete mode, setting it selects fleet mode: the shared fleet runner is not deregistered and `GL_TOKEN` is not required |
| `GL_TOKEN` | yes (create²/delete³) | — | GitLab PAT (Owner role on the target group or project, scopes: `create_runner` + `manage_runner` + `api`). In create mode, required unless `RUNNER_TOKEN` is set. In delete mode, required unless `RUNNER_TOKEN` is set (fleet mode) — used to look up and deregister an individually-registered runner |
| `PROJECT_ID` | yes (create)¹ | — | GitLab project ID — registers a project-scoped runner (`locked=true`) |
| `GROUP_ID` | yes (create)¹ | — | GitLab group ID — registers a group-scoped runner (`locked=false`). Recommended for platform-service deployments |
| `GITLAB_URL` | yes (create/delete³) | — | GitLab instance URL. In delete mode, required only when `GL_TOKEN` is used to look up / deregister a runner (not required in fleet mode) |
| `RUNNER_IMAGE` | yes (create) | — | Image pre-pulled as a warm cache; jobs must still set `image:` in `.gitlab-ci.yml` |
| `RUNNER_TAG` | no | `fullsend-gitlab-runner` | Runner tag for job matching |
| `DRAIN_TIMEOUT_SEC` | no | `600` | Delete mode only: cap in seconds on draining in-flight jobs (SIGQUIT + wait for `runner-*` containers to exit) before deleting the VM. On cap overrun, the script warns and proceeds. See `--no-drain` to skip draining entirely |
| `RUNNER_ACCESS_LEVEL` | no | `not_protected` | `ref_protected` restricts the runner to protected branches and tags, so merge-request pipelines on unprotected source refs never match and sit `pending`. In project mode, `not_protected` means any job on any branch of the project can run; in group mode, any tag-matched job on any branch of any project invited into the group tree runs on this VM (see Security below) |
| `OPENSHELL_VERSION` | no | from `.github/scripts/openshell-version.sh` | OpenShell version used at **VM provision** (Renovate-tracked). Per-job `prepare.sh` re-reads the job image's `openshell --version` and upgrades the host CLI + supervisor when they differ, so a stale VM pin cannot stick. |
| `GITLAB_RUNNER_VERSION` | no | `19.2.1` | gitlab-runner version |
| `REGISTRATION_TOKEN` | setup only | — | GitLab runner registration token |

¹ Exactly one of `PROJECT_ID` or `GROUP_ID` must be set when registering a new runner with `GL_TOKEN` (mutually exclusive). Not required when `RUNNER_TOKEN` is set.
² Create mode: `RUNNER_TOKEN` joins an existing pool (and takes precedence if both are set). `GL_TOKEN` registers a new runner. One of the two is required.
³ Delete mode: `RUNNER_TOKEN` selects fleet mode (skip deregistration, `GL_TOKEN`/`GITLAB_URL` not required). Otherwise `GL_TOKEN` + `GITLAB_URL` are required so the script can look up and deregister an individually-registered runner — omitting them is not authorization to skip deregistration.

### OpenShift-specific

| Variable | Required | Default | Description |
|---|---|---|---|
| `NAMESPACE` | yes (create/delete) | — | OpenShift namespace |
| `VM_USER` | no | `fedora` | Cloud-image login user (`cloud-user` on RHEL/CentOS Stream images) |
| `SSH_PUBLIC_KEY` | no | contents of `~/.ssh/id_rsa.pub` or `id_ed25519.pub` | SSH public key contents (not a path) |
| `RUNNER_USER` | no | `VM_USER` | Delete mode only: Unix account gitlab-runner/podman run as on the VM (setup.sh's `RUNNER_USER`). Used to drain as the correct identity when it differs from `VM_USER` |

### GCE-specific

| Variable | Required | Default | Description |
|---|---|---|---|
| `GCP_PROJECT` | yes | — | GCP project ID |
| `GCP_ZONE` | no | `us-east1-b` | GCE zone |
| `GCP_MACHINE_TYPE` | no | `e2-standard-4` | GCE machine type (4 vCPU, 16 GB — closest to the 4 CPU / 14 GiB KubeVirt spec) |
| `GCP_NETWORK` | no | `gitlab-runners` | VPC network (must have IAP ingress and egress firewall rules) |
| `GCP_SUBNET` | no | — | VPC subnet (required for custom-mode VPCs; omit for auto-mode) |
| `GCP_USE_IAP` | no | `true` | Use IAP tunneling for SSH. Set to `false` to create the VM with an external IP and SSH directly. |
| `GCP_IMAGE_FAMILY` | no | `fedora-cloud-43-x86-64` | GCE image family |
| `GCP_IMAGE_PROJECT` | no | `fedora-cloud` | GCE image project |
| `RUNNER_USER` | no | unset | `create-gcp-vm.sh --resume`: service user for a VM that was never configured (default: the `gcloud compute ssh` login user). On a configured VM it must match the detected service user or resume refuses (see [Resuming or repairing a GCE runner](#resuming-or-repairing-a-gce-runner)). Delete mode: Unix account gitlab-runner/podman run as on the VM (setup.sh's `RUNNER_USER`, i.e. whichever identity ran `setup.sh`). Used to drain as the correct identity when `gcloud compute ssh` connects as someone else. GCE has no fixed login user equivalent to OpenShift's `VM_USER`, so unlike there this has no safe default — without it, the drain runs as the connecting identity and can under-report idle if that identity differs from the one gitlab-runner runs as |

## Files

- `create-openshift-vm.sh` — end-to-end VM creation on OpenShift + runner registration + setup
- `delete-openshift-vm.sh` — drain in-flight jobs, then OpenShift VM teardown + runner deregistration
- `create-gcp-vm.sh` — end-to-end VM creation on GCE + runner registration + setup
- `delete-gcp-vm.sh` — drain in-flight jobs, then GCE VM teardown + runner deregistration
- `setup.sh` — standalone VM configuration (called by create-openshift-vm.sh / create-gcp-vm.sh). Idempotent and safe to re-run in place as a debug convenience; recreation is the compliance path (see #7257). Re-running it on an already-provisioned VM also installs/refreshes the Podman prune timer.
- `setup_test.sh` — unit tests for setup.sh idempotency hygiene (backup, executor path reconciliation and verification, gateway seed skip, Fedora repo repair)
- `create-openshift-vm_test.sh` — end-to-end tests for create-openshift-vm.sh against stubbed `oc`/`virtctl`/GitLab API (shared-token path, cloud-init package repair, `--resume`)
- `create-gcp-vm_test.sh` — end-to-end tests for create-gcp-vm.sh against stubbed `gcloud`/GitLab API (fresh create, `--resume` disk growth, service-user detection, both registration modes)
- `podman-prune.sh` — reclaims unused rootless Podman containers and images; installed as a user systemd timer by setup.sh and invoked from prepare/cleanup
- `podman-prune_test.sh` — unit tests for the prune script and timer install
- `grow-root-fs.sh` — grows the root partition and Btrfs filesystem to fill the disk and verifies capacity; run by create-gcp-vm.sh and used to repair existing GCE runners (see [GCE boot disk size and repair](#gce-boot-disk-size-and-repair))
- `grow-root-fs_test.sh` — unit tests for grow-root-fs.sh (unexpanded, already-expanded, failed growth, unsupported layouts)
- `gitlab-runner-version.sh` — central pin for the gitlab-runner version
- `vm.yaml` — KubeVirt VirtualMachine template (OpenShift only)
- `vm_test.sh` — tests that vm.yaml's `bootcmd` leaves Fedora repo files usable (HTTPS `baseurl=`) before cloud-init installs packages
- `executor/job_id.sh` — shared helper resolving the trusted job ID
- `executor/prepare.sh` — custom executor prepare stage (reaps leftover OpenShell containers, prunes unused images, starts a per-job gateway matched to the job image's OpenShell version)
- `executor/run.sh` — custom executor run stage
- `executor/cleanup.sh` — custom executor cleanup stage (stops the gateway, wipes `~/.local/state/openshell/{gateway,tls}`, reaps sandboxes)
- `executor/gateway.sh` — shared per-job gateway helpers sourced by prepare/cleanup

## Executor script layout

`install_executor` in [`setup.sh`](setup.sh) does not glob `executor/*.sh`;
it copies an explicit five-file allowlist — `job_id.sh`, `prepare.sh`,
`run.sh`, `cleanup.sh`, `gateway.sh` — into a flat `EXECUTOR_DIR`
(`~/gitlab-runner-executor`) when the VM is provisioned. `create-gcp-vm.sh`
and `create-openshift-vm.sh` independently hard-code the same five-file list
to stage and `chmod +x` the scripts on the VM. A script under `executor/`
that is not on all three lists (e.g. `gateway_test.sh`,
`prepare_validation_test.sh`) is never installed at all, regardless of what
paths it references — a new script must be added to all three lists first.

Once a script is on those lists, parent directories from the source tree are
**not** preserved when it lands in `EXECUTOR_DIR`, except those explicitly
seeded by `install_executor`. Today that exception is only
`.github/scripts/openshell-version.sh`. Any such script that needs a file
outside its own directory must either:

1. Reference only same-directory siblings (the path still works after
   flattening), or
2. Have `install_executor` explicitly copy the needed file into
   `EXECUTOR_DIR`, mirroring the `openshell-version.sh` precedent.

Guessing a `BASH_SOURCE`-relative path across the flattening boundary
silently fails at per-job runtime: `prepare.sh`/`cleanup.sh` source the
flattened copy, not the source-tree file. [`gateway.sh`](executor/gateway.sh)
is the current example of (2); `job_id.sh`, `prepare.sh`, `run.sh`, and
`cleanup.sh` only reference same-directory siblings.

`podman-prune.sh` is **not** an executor script. `setup.sh` installs it
to `~/.local/lib/fullsend/podman-prune.sh` and `prepare.sh`/`cleanup.sh`
invoke that path via `prune_unused_podman_storage` in `gateway.sh`. Do
not add it to the five-file executor allowlist.

## Repairing stale executor paths

`setup.sh` derives the custom executor's paths from the `HOME` of the user
running it: `prepare_exec`, `run_exec` and `cleanup_exec` under
`~/gitlab-runner-executor/`, and `builds_dir`/`cache_dir` under `~/builds`
and `~/cache`. If `/etc/gitlab-runner/config.toml` points them anywhere
else (for example at another account's home), every job fails in prepare
with `fork/exec .../prepare.sh: no such file or directory`.

The supported repair is to re-run `setup.sh` as the runner service user,
the account the runner should run as (`systemctl show -p User gitlab-runner`).
`setup.sh` also switches the service to whichever user runs it. Copy the
current `hack/gitlab-runner-vm/` files onto the VM and re-run `setup.sh`
with the same `GITLAB_URL` / `RUNNER_IMAGE` used at provision time. Do not
edit the TOML by hand or switch the executor back to `shell`. On an
existing custom-executor config, `patch_config` then:

- rewrites only those five managed keys when their values differ, after
  saving the previous file as `config.toml.bak`. Registration (`name`,
  `url`, `id`, `token`) and every other setting are left unchanged;
- leaves an already-correct config untouched (no write, no backup);
- fails without modifying anything if `config.toml` has more than one
  `[[runners]]` block or a managed key is missing or duplicated. Fix
  those by hand.

`verify` then checks the paths `config.toml` actually configures, not just
the scripts under `~/gitlab-runner-executor/`. The configured scripts must
be executable and the build/cache directories writable by the runner user,
or setup exits non-zero.

## Disk / image prune

These VMs are long-lived. Without periodic reclaim, unused Podman images
accumulate until `podman pull` fails with `no space left on device`.
`setup.sh` therefore:

1. Writes `~/.config/fullsend-gitlab-runner/keep-images` with the
   pre-pulled `RUNNER_IMAGE` and OpenShell supervisor tag.
2. Installs `~/.local/lib/fullsend/podman-prune.sh` and a user systemd
   timer (`fullsend-podman-prune.timer`, hourly, Nice=19) that skips
   the run when a `runner-*` or `openshell-*` container is in-flight.
3. Invokes the same helper from `prepare.sh` (before the job image pull)
   and `cleanup.sh` (after the job container is gone) so a busy runner
   still reclaims space between jobs.

To apply this to an already-provisioned VM, copy the updated
`hack/gitlab-runner-vm/` files onto the VM and re-run `setup.sh` with
the same `GITLAB_URL` / `RUNNER_IMAGE` used at provision time. The
install is idempotent. For an immediate reclaim without waiting for the
first timer tick:

```bash
systemctl --user start fullsend-podman-prune.service
```

## GCE boot disk size and repair

`create-gcp-vm.sh` creates a **30 GiB** `pd-balanced` boot disk, matching
the ~30 GiB guest disks of the OpenShift runners. A bigger virtual disk is
not enough on its own: the Fedora Cloud image's own first-boot growth was
observed not to run on GCE, leaving an ~8 GiB root Btrfs partition on a
20 GiB disk (#8163). After installing packages and before registering the
runner, `create-gcp-vm.sh` therefore streams
[`grow-root-fs.sh`](grow-root-fs.sh) to the VM and runs it as root. The
script:

1. Checks the layout first: `/` must be Btrfs on a single-device filesystem
   on a partition of a whole disk, and `/home` and `/var` must be on the
   same filesystem (Fedora Cloud subvolumes). Any other layout fails with
   `ERROR: ... no changes made` before any device is modified.
2. Installs `cloud-utils-growpart` / `btrfs-progs` with `dnf` if they are
   missing, then runs `growpart` on the root partition (`NOCHANGE` counts
   as success) and `btrfs filesystem resize <devid>:max /` for the
   filesystem's sole device.
3. Verifies capacity: the disk is at least `MIN_DISK_GIB` (30 when run by
   `create-gcp-vm.sh`), the root partition reaches the end of the disk, and
   the Btrfs device size matches the partition. If any check fails,
   provisioning stops and prints the cleanup hint. `create-gcp-vm.sh` also
   requires the script's `OK: root filesystem spans the disk` line in the
   SSH output, so a truncated stream cannot pass as success.

Expected usable capacity: roughly **27–28 GiB** for `/`, `/home`, and
`/var` together (they share one Btrfs filesystem) on a 30 GiB disk, about
17–18 GiB on a 20 GiB disk. The difference goes to the EFI and `/boot`
partitions (~2 GiB) and filesystem overhead. Check with:

```bash
lsblk -o NAME,SIZE,FSTYPE,MOUNTPOINTS
df -h / /home /var
```

### Repairing existing runners

The script is idempotent and grows the disk online. It does not recreate
the VM and leaves the runner registration, images, and workspace data in
place. `./create-gcp-vm.sh --resume NUMBER` performs both steps below
(see [Resuming or repairing a GCE runner](#resuming-or-repairing-a-gce-runner))
and also re-runs `setup.sh`. To grow only the disk, run it from the repo
root on your workstation (drop
`--tunnel-through-iap` for VMs with an external IP):

```bash
vm=fullsend-gitlab-runner-01

# Optional: grow the GCE disk to 30 GiB first. The boot disk is named after
# the VM; the resize is online and only increases size. Skip this step to
# just reclaim the unused space on a current 20 GiB disk.
gcloud compute disks resize "${vm}" --size=30GB \
  --project="${GCP_PROJECT}" --zone="${GCP_ZONE}"

# Grow the root partition and Btrfs filesystem, then verify. Set
# MIN_DISK_GIB to the disk size you expect (30 after the resize, 20 without).
gcloud compute ssh "${vm}" --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" \
  --tunnel-through-iap \
  -- "sudo env MIN_DISK_GIB=30 bash -s" < hack/gitlab-runner-vm/grow-root-fs.sh
```

If you re-run the script on a runner that is already expanded, it changes
nothing and still runs the verification.

## Security notes

- By default (`GCP_USE_IAP=true`), GCE VMs are created with `--no-address`
  (no public IP) and SSH access is tunneled through IAP, which requires the
  operator to have `roles/iap.tunnelResourceAccessor` and authenticates via
  GCP IAM — mirroring the OpenShift model where SSH is tunneled through the
  K8s API server. When `GCP_USE_IAP=false`, the VM gets an external IP and
  SSH connects directly with trust-on-first-use host-key verification
  (`StrictHostKeyChecking=accept-new`) — the first connection accepts the key
  and subsequent connections within the same run reject changes.
  The script prints a command to remove the external IP afterward.
- GCE VMs are created with `--no-service-account --no-scopes`. The runner
  does not need a Compute Engine service account: operator `gcloud` runs on
  the workstation, and inference auth is GitLab OIDC → WIF. Attaching the
  default Compute SA (`roles/editor`) would expose a stealable OAuth token
  via the metadata server (`169.254.169.254`) to the orchestration container
  (`--network=host`, no L7 egress policy). Existing VMs created without these
  flags should have the SA removed (stop, set-service-account, start) or be
  recreated with `create-gcp-vm.sh`:

  ```bash
  gcloud compute instances stop "${vm}" --project="${GCP_PROJECT}" --zone="${GCP_ZONE}"
  gcloud compute instances set-service-account "${vm}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" \
    --no-service-account --no-scopes
  gcloud compute instances start "${vm}" --project="${GCP_PROJECT}" --zone="${GCP_ZONE}"
  ```

- The CA trust bootstrap uses trust-on-first-use (TOFU). For higher assurance,
  provide the CA bundle out-of-band before running setup.sh.
- The OCI CA-injection hook fires for all containers on the host. It only
  copies a CA bundle file and is scoped to the `createRuntime` stage.
- Job containers share the host network namespace (`--network=host`) to reach
  the OpenShell gateway. The gateway binds to `0.0.0.0` (required for the
  Podman compute driver — sandbox containers register via
  `host.containers.internal`). mTLS protects the endpoint. The gateway process
  itself is per-job: `prepare.sh` starts it against a wiped store, `cleanup.sh`
  stops it. A leftover from a killed job is reaped at the next prepare.
- Job containers receive read-only access to the runner's gateway mTLS
  credentials (`~/.config/openshell`). This is required for the fullsend
  agent inside job containers to authenticate to the gateway. Credential
  exposure depends on the runner scope:
  - **Project mode** (`PROJECT_ID`): the runner is scoped to one project by
    `runner_type=project_type` and `locked=true`, so only jobs from that
    project can access these credentials.
  - **Group mode** (`GROUP_ID`): the runner is scoped to the group by
    `runner_type=group_type`, so any project invited into the group tree can
    run tag-matched jobs on this VM and access the mounted gateway credentials.
    **This is a wider trust boundary than project mode** — credential access
    extends from a single project to every project in the group tree. For
    group-scoped runners, consider setting `RUNNER_ACCESS_LEVEL=ref_protected`
    as a compensating control to restrict jobs to protected branches and tags.
  In both modes, access is narrowed by `run_untagged=false` (only jobs tagged
  with the runner's tag are matched) and optionally by `ref_protected`
  (restricting to protected branches/tags). If job-scoped credential minting
  is added to the gateway, this mount should be replaced with short-lived
  per-job tokens.
