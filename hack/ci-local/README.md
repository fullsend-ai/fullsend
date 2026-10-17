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
step with a message saying so.

Apple silicon runs arm64 Linux. Use `--amd64` to match GitHub's x86_64
runners exactly (emulated, slow). It needs Rosetta emulation, which Docker
Desktop uses (`CI_LOCAL_ENGINE=docker`); under QEMU emulation, the default
for a Podman machine, the x86_64 `uv` binary segfaults.

## Isolation

The container keeps **host secrets** out. It is **not a sandbox** against the
code under test, which has the same access to the checkout as `make test` on
the host.

- No host environment variables, SSH agent or credential files from outside
  the checkout enter the container. `GH_TOKEN`/`GITHUB_TOKEN` are empty, as on
  CI. Podman builds and runs with `--http-proxy=false`, so host
  `HTTP(S)_PROXY` values (which can carry credentials) are not copied in.
  Docker has no such switch: it passes any `proxies` set in
  `~/.docker/config.json` into every build step and container. As on CI, there
  is no proxy support: a network that requires one to reach the internet
  can't build the image.
- The whole checkout is mounted, **including untracked and gitignored files**
  such as `.env.local`, so tested code can read them, as it can on the host.
- The checkout and its `.git` directory are mounted read-write (`lint`
  writes the git index), so tested code could change git config or hooks that
  a later host-side git command runs. Again, the same as on the host.
- The host checkout changes only as it would on the host: lint hooks apply
  their autofixes (gofmt, ruff, whitespace), and builds leave gitignored
  output. `CI=true` skips git hook installation, `docs` builds from a
  throwaway clone, and Linux `node_modules` live in named volumes.
- On SELinux hosts (Fedora, RHEL), mounts use `:z`, which relabels the
  checkout's files so the container can read them.

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

Floating versions (`"3.14"` → newest 3.14.x, `"22"` → newest 22.x, uv) resolve
to the newest release when the image is built, as CI's setup actions do.
`hack/ci-local/ci-local build-image` rebuilds without cache to pick up new
patch releases.

On amd64, lychee and pinact are verified against the SHA256s that `lint.yml`
pins. `lint.yml` pins no arm64 checksums, so on arm64 (Apple silicon) they,
like Node and uv everywhere, are verified against the publisher's checksum
file from the same release.

Each version set leaves its own image behind. To reclaim space, remove them all
with `podman image prune -a --filter label=fullsend-ci-local=1` (or `docker`);
the next run rebuilds the current one. Download caches (Go, npm, uv,
pre-commit) are volumes shared by all checkouts; `node_modules` volumes are per
checkout (`fullsend-ci-local-nm-*-<checkout-hash>`), so concurrent runs in two
worktrees never share one. Remove them all with
`podman volume rm $(podman volume ls -q | grep '^fullsend-ci-local')`.

Not covered: `behaviour-test` / `playback-test` (they need a real GitHub org,
the OIDC mint and secrets) and `check-gateway-doc-configs` (it starts its own
container; the main lint job skips it too).
