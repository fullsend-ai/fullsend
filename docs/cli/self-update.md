---
sidebar_label: fullsend self-update
---

# fullsend self-update

Replace the running `fullsend` binary with a release from
[GitHub Releases](https://github.com/fullsend-ai/fullsend/releases). With no
flags, the command installs the latest release.

The command changes only the CLI binary on your machine. To update the fullsend
workflows and configuration in a repository, use
[`fullsend repos install`](repos.md).

## Usage

```bash
fullsend self-update [flags]
```

## Flags

| Flag | Description |
|------|-------------|
| `--version` | Release to install, such as `v0.53.0`. Defaults to the latest release. It can be older than the running binary, which lets you roll back. It must be `v0.19.0` or later, the oldest release with a signature this command can verify. Releases from before `self-update` was added don't include the command, so after rolling back to one, you update by hand |
| `--dry-run` | Report what would be updated without replacing anything. With `--version`, it first downloads the release's `checksums.txt` to check that the release exists and has a build for your platform |

## What it does

1. Reads the version of the running binary. A development build has no release
   version to compare against, so the command stops. A development build
   reports `dev`, or a `git describe` version such as `v0.52.0-3-gabc1234`
   from `make go-build` on an untagged or modified checkout.
2. Resolves the path of the running binary. It follows symlinks, so it replaces
   the real file.
3. Picks the target release. The command reports the result and exits in two
   cases: the binary already runs the target release, or it runs a release
   newer than the latest one, such as a release candidate, and you did not pass
   `--version`.
4. Creates a temporary file next to the binary. If the directory is not
   writable, the command stops before it downloads anything.
5. Downloads `checksums.txt` and `checksums.txt.sigstore.json`, and verifies the
   signature. See [Signature verification](#signature-verification).
6. Downloads `fullsend_<version>_<os>_<arch>.tar.gz` for the current platform.
   It checks the archive against the SHA256 checksum in `checksums.txt`.
7. Extracts the binary and gives it the permissions of the old file. It then
   renames the new file over the old binary in one step.

The old binary stays in place until the last step. If a step fails, or you
press Ctrl-C, the command removes the temporary file and leaves the old binary
as it was.

## Signature verification

A checksum shows that the archive arrived intact. It does not show who
published it. The release workflow signs `checksums.txt` with keyless
[cosign](https://docs.sigstore.dev/) signing, which ties the signature to the
identity of the GitHub Actions run instead of a long-lived key. The command
verifies that signature before it trusts any checksum.

The signing certificate must meet these conditions:

- Fulcio, the Sigstore certificate authority, issued it for the GitHub Actions
  OIDC issuer (`https://token.actions.githubusercontent.com`).
- It names the workflow
  `https://github.com/fullsend-ai/fullsend/.github/workflows/release.yml` at
  `refs/tags/<tag>`, where `<tag>` is the release being installed. A signature
  from one release therefore cannot vouch for another.
- It comes from the `fullsend-ai/fullsend` repository and a GitHub-hosted
  runner.

The signature must also have an entry in Rekor, the Sigstore transparency log,
and a trusted timestamp. The command gets the Sigstore public keys from the
Sigstore public-good TUF repository, a signed store of trust material. It
caches them in `~/.sigstore/root`, the cache that cosign also uses.

To check the signature of a release by hand, download both files and run:

```bash
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/fullsend-ai/fullsend/.github/workflows/release.yml@refs/tags/<tag>
```

This command checks the issuer and the workflow identity, which already names
the repository and the tag. `fullsend self-update` also requires a
GitHub-hosted runner.

## Package-manager installs

The command stops for binaries that Homebrew or Nix manages, so it does not
break their records of what is installed:

- **Homebrew**: a binary in a Homebrew `Cellar` directory. Update it with your
  package manager.
- **Nix**: a binary in `/nix/store`, which is read-only. Update it through Nix.

## Permissions

The command needs write access to the directory that holds the binary, such
as `/usr/local/bin`. Without it, the command stops before it downloads
anything. Run it again with `sudo`, or reinstall `fullsend` in a directory you
own, such as `~/.local/bin`.

## Examples

```bash
# Update to the latest release
fullsend self-update

# Show what would change without downloading anything
fullsend self-update --dry-run

# Roll back to a specific release (v0.19.0 or later). If it came out
# before self-update was added, update by hand from then on.
fullsend self-update --version v0.52.0
```
