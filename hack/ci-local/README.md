# ci-local

Run the repo's CI tests and linters in a container that matches the
`ubuntu-24.04` GitHub Actions runner, so a local run predicts CI on macOS and
on Linux distributions other than Ubuntu (#8479).

```bash
make ci-local                              # everything PR CI runs
make ci-local T="go-test lint-all"         # selected targets
hack/ci-local/ci-local coverage            # coverage of the files you changed
hack/ci-local/ci-local -C ../wt go-test    # another checkout or worktree
hack/ci-local/ci-local help                # all targets and options
```

The first run builds the image (a few minutes). Later runs reuse it and the
cache volumes, until the checkout's tool versions change (see below).

## Setup

| Platform | One-time setup |
|---|---|
| macOS + Podman (preferred) | `brew install podman && podman machine init --cpus 4 --memory 8192 && podman machine start` |
| macOS + Docker Desktop | Docker Desktop running; set `CI_LOCAL_ENGINE=docker` if Podman is also installed |
| Fedora / RHEL / other Linux | install `podman` or `docker`; SELinux `:z` labelling is automatic |

The `Containerfile` uses only classic instructions (no `RUN` heredocs, no
`COPY --chmod`), so it builds with older Buildah/Podman too. Tested with
Podman 6.1, Buildah 1.28 (the version behind Debian 12's Podman 4.3) and
Docker 29 with BuildKit. Docker's **legacy builder** (`DOCKER_BUILDKIT=0`) is
not supported: it leaves `TARGETARCH` unset, and the build stops at its first
step with a message saying so. `ci-local` itself needs **Podman 4.5 or
later** for its [isolation](#isolation) override, and stops before any build
or run with an older one; use Docker (`CI_LOCAL_ENGINE=docker`) there.
The tests run as your uid, as on CI's non-root runner. Rootless Podman maps
you onto the image's `runner` user (`--userns=keep-id`); a rootful Podman
(`podman machine set --rootful`) maps that to root, so `ci-local` passes
`--user` with your uid:gid instead, as for Docker. It refuses rootful Podman
run as root (`sudo`).

Apple silicon runs arm64 Linux. Use `--amd64` to match GitHub's x86_64
runners exactly (emulated, slow). It needs Rosetta emulation, which Docker
Desktop uses (`CI_LOCAL_ENGINE=docker`); under QEMU emulation, the default
for a Podman machine, the x86_64 `uv` binary segfaults.

## Isolation

The container keeps **host secrets** out. It is **not a sandbox** against the
code under test, which has the same access to the checkout as `make test` on
the host.

- `ci-local` passes no host environment variables, SSH agent or credential
  files from outside the checkout into the container. Podman's own
  configuration could add some: any `env`, `env_host = true`, `volumes`,
  `mounts` or `devices` setting in `containers.conf`, and the default mounts
  in `mounts.conf` (on RHEL, the host's subscription certificates under
  `/run/secrets`). `ci-local` turns these off for its Podman builds and runs:
  it points `CONTAINERS_CONF_OVERRIDE` at a file that sets
  `env_host = false` and `env`, `volumes`, `mounts` and `devices` to `[]`
  (replacing any override you set). A remote Podman client (macOS, Windows)
  reads that file too, because it adds its own `containers.conf` mounts to
  the container. With a local Podman, `ci-local` also passes
  `--default-mounts-file=/dev/null`; a remote client has no such flag and
  reads no `mounts.conf`. A remote client on Linux (`CONTAINER_HOST`,
  `--remote`, or `CI_LOCAL_ENGINE=podman-remote`) is refused: its service runs
  on that host and applies the host's `containers.conf` `env` and
  `mounts.conf` itself, out of the override's reach. Unset `CONTAINER_HOST`
  and drop `--remote`, or use Docker. Podman before 4.5 silently ignores
  `CONTAINERS_CONF_OVERRIDE`, so `ci-local` refuses to run with it.
  `GH_TOKEN`/`GITHUB_TOKEN` are empty, as on CI. No host proxy settings,
  which can carry credentials, reach builds or containers either. Podman
  builds and runs with `--http-proxy=false`, so host `HTTP(S)_PROXY` values
  are not copied in. Docker would inject any `proxies` set in
  `~/.docker/config.json`, so `ci-local` sets every proxy variable
  (`HTTP_PROXY`, `HTTPS_PROXY`, `FTP_PROXY`, `NO_PROXY`, `ALL_PROXY`, upper
  and lower case) to empty for Docker builds and runs. As on CI, there is no
  proxy support: a network that requires one to reach the internet can't
  build the image.
- The whole checkout is mounted, **including untracked and gitignored files**
  such as `.env.local`, so tested code can read them, as it can on the host.
- The checkout and its `.git` directory are mounted read-write (`lint`
  writes the git index), so tested code could change git config or hooks that
  a later host-side git command runs. Again, the same as on the host. In a
  submodule or `--separate-git-dir` checkout, the git config is instead a
  read-only copy without `core.worktree`, whose relative path only works on
  the host. A git command in the container that writes repo config (`git
  config`, `git remote add`, `git submodule init`) fails there with "could
  not write config file"; no current target or `script-test` suite does
  this. With Docker Desktop, changing that config on the host during a
  run (`git config`, `git push -u`) replaces the copy with the host's own
  config in the container, so git fails there until the next run. Nested
  submodules initialized inside such a checkout keep their host paths, so
  git commands inside them fail in the container.
- In a worktree or submodule checkout, the whole shared git dir is mounted,
  and the paths of the host's other worktrees don't exist in the container.
  Git there treats them as deleted: `git worktree prune` removes their
  records at once, and `git gc` once they're older than
  `gc.worktreePruneExpire` (3 months by default). The host worktree then
  has to be added again; `git worktree lock` protects one.
- The download caches (Go modules and `go/bin`, Go build, pre-commit, uv,
  npm) are **shared by every checkout and persist between runs**, and they
  hold programs that later runs execute. `/home/runner/go/bin` also comes
  before the system directories on `PATH`, as on CI. So code tested in one
  checkout can leave programs that a later run in any checkout executes,
  with that checkout's access. Per-checkout caches would close this but cost
  a full re-download in every new worktree, so `ci-local` keeps them shared.
  Before and after running code you don't trust, remove the volumes (see
  [Tool versions follow CI](#tool-versions-follow-ci)), or run it with a
  separate engine or Podman machine.
- The host checkout changes only as it would on the host: lint hooks apply
  their autofixes (gofmt, ruff, whitespace), and builds leave gitignored
  output. `CI=true` skips git hook installation, `docs` builds from a
  throwaway clone, and Linux `node_modules` live in named volumes.
- On SELinux hosts (Fedora, RHEL), mounts use `:z`, which relabels the
  checkout's files so the container can read them.
- DNS lookups go to the host's own resolvers (see [DNS](#dns)); `ci-local`
  adds no resolver of its own.

## DNS

By default the container uses the host's default resolvers, in the host's
order: on macOS the first resolver that `scutil --dns` lists, on Linux the
`nameserver` lines of `/run/systemd/resolve/resolv.conf` (or
`/etc/resolv.conf`). Only IPv4 resolvers are used. Loopback and IPv6 resolvers
can't be reached from the container. Each address is passed to the engine as
`--dns`, so lookups go where they go on the host, which is not always where
the engine's own forwarding sends them.

Per-domain (split) DNS is not carried over. Names that the host sends to a
separate resolver, such as a VPN's internal domains, may not resolve in the
container. The CI targets only need public names.

On Docker Desktop, `ci-local` also passes `--dns-option use-vc`, so lookups
use TCP. With **Use kernel networking for UDP** enabled (Settings → Resources
→ Network), UDP leaves Docker's VM directly and can't reach a resolver that
is only routable through the host, such as a VPN's. TCP goes through the host.

### Overriding and adding a fallback

Set `CI_LOCAL_DNS` to a space-separated list of resolver addresses:

```bash
CI_LOCAL_DNS="<primary> <fallback>" hack/ci-local/ci-local ci
CI_LOCAL_DNS= hack/ci-local/ci-local ci   # the engine's own DNS, no --dns
```

To find your resolvers, run `scutil --dns` (macOS) or `resolvectl status`
(Linux). Use resolvers that you or your organization trust.

The container tries resolvers in order, but it moves to the next one only
when a resolver times out or refuses the query. A resolver that answers "no
such name" (`NXDOMAIN`) ends the lookup. Some routers and ISP resolvers return
`NXDOMAIN` for domains they filter, including `example.com`, which the
`internal/fetch` tests need. Put a resolver that answers those names first,
not after the one that filters them.

## Tool versions follow CI

`Containerfile` pins no versions. On each run, `ci-local` reads them from the
checkout and passes them in as build args:

| Tool | Read from |
|---|---|
| Go | `go.mod` (`go` line), as `actions/setup-go` does |
| Python, Node | `python-version` / `node-version` in `.github/workflows/lint.yml` |
| pip packages | the `uv pip install --system` step in `lint.yml` |
| lychee, pinact | the download URLs and SHA256s in `lint.yml` |
| uv | latest release (`lint.yml` doesn't pin it) |

The image is tagged with a hash of those versions and the `Containerfile`, so
a bump (by Renovate or by hand) rebuilds it on the next run. If a version
can't be read, `ci-local` stops with an error rather than guessing.

`resolve_versions` in `ci-local` reads the `lint.yml` lines in the table above
with simple patterns. Changing how those lines are written can stop
`ci-local` from reading them, and CI catches that in the same PR:
`make script-test` runs `ci-local_test.sh`, which reads every version from
the real `lint.yml` and `go.mod` and fails if one is missing. If it fails,
update `resolve_versions` in that PR. This replaces the "comment in each
place" suggested in #8479: CI enforces the link, so `lint.yml` doesn't need a
note that could go stale.

Floating versions (`"3.14"` → 3.14.x, `"22"` → 22.x, uv) resolve to the
newest matching release when the image is built. CI can run an older patch:
`actions/setup-python` and `actions/setup-node` run without `check-latest`, so
they take the match already in the runner's toolcache (listed in the
[runner-images README](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md)).
Python also comes from a different build: uv's python-build-standalone here,
actions/python-versions in CI. `hack/ci-local/ci-local build-image` rebuilds
without cache to pick up new patch releases.

On amd64, lychee and pinact are verified against the SHA256s that `lint.yml`
pins. `lint.yml` pins no arm64 checksums, so on arm64 (Apple silicon) they,
like Node and uv everywhere, are verified against the publisher's checksum
file from the same release.

Each version set leaves its own image behind. `hack/ci-local/ci-local clean`
removes every `ci-local` image except the current one for this checkout (add
`--amd64` for the amd64 images). A checkout with different tool versions
rebuilds its image on its next run. It also removes the `node_modules`
volumes of checkouts that no longer exist: each per-checkout volume is
labelled with its checkout's path, and is removed once that path is gone.
Volumes from older versions of `ci-local` (unsuffixed, or without the label)
are removed too; the next run in a live checkout reinstalls its
`node_modules`. It removes
nothing by force: an image or volume that a running container uses, such as a
`ci-local` run in another worktree, is kept and reported. It never touches the
download caches or other images. To remove
every `ci-local` image instead, run
`podman image prune -a --filter label=fullsend-ci-local=1` (or `docker`); the
next run rebuilds the current one. Download caches (Go, npm, uv,
pre-commit) are volumes shared by all checkouts; `node_modules` volumes are per
checkout (`fullsend-ci-local-nm-*-<checkout-hash>`), so concurrent runs in two
worktrees never share one. Remove them all with
`podman volume rm $(podman volume ls -q | grep '^fullsend-ci-local')`.

Not covered: `behaviour-test` / `playback-test` (they need a real GitHub org,
the OIDC mint and secrets) and `check-gateway-doc-configs` (it starts its own
container; the main lint job skips it too).
