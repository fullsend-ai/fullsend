// Package selfupdate replaces the running fullsend CLI binary with a
// release from GitHub Releases. It installs a release only when the
// Sigstore signature on its checksums.txt verifies for that tag and the
// archive matches its recorded SHA256.
package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/binary"
)

const (
	checksumsAsset = "checksums.txt"
	bundleAsset    = "checksums.txt.sigstore.json"

	maxChecksumsSize = 64 * 1024
	maxBundleSize    = 1024 * 1024
	maxArchiveSize   = 200 * 1024 * 1024
)

// ErrDevBuild is returned when the running binary is not a release build,
// so there is no release version to compare against.
var ErrDevBuild = errors.New("this fullsend binary is a development build; self-update only replaces release binaries")

// minInstallable is the oldest release whose signature bundle
// verifyChecksums can read. Older releases have none, or an older format.
var minInstallable = version{minor: 19}

// Status says what Run did.
type Status int

const (
	// StatusUpToDate means the binary already runs the target release.
	StatusUpToDate Status = iota
	// StatusAhead means the binary is newer than the latest release (for
	// example a release candidate) and no version was requested.
	StatusAhead
	// StatusWouldUpdate means a dry run found an update to install.
	StatusWouldUpdate
	// StatusUpdated means the binary was replaced.
	StatusUpdated
)

// Options configures a Run.
type Options struct {
	// CurrentVersion is the version of the running binary.
	CurrentVersion string
	// TargetVersion is the release to install, such as "v0.53.0". Empty
	// means the latest release. An explicit version may be older than the
	// current one, which allows rolling back.
	TargetVersion string
	// DryRun reports what would change without installing anything. With
	// TargetVersion set, it checks that the release has a build for this
	// platform.
	DryRun bool
}

// Result describes the outcome of a Run.
type Result struct {
	Status Status
	// From and To are release tags, for example "v0.52.0".
	From, To string
	// Path is the resolved path of the binary.
	Path string
}

// Updater replaces the running binary. Build one with New; the function
// fields are test seams for this package.
type Updater struct {
	goos, goarch string

	// executable returns the path of the running binary.
	executable func() (string, error)
	// latestTag returns the tag of the latest release.
	latestTag func(ctx context.Context) (string, error)
	// fetchAsset downloads a release asset, failing above maxSize bytes.
	fetchAsset func(ctx context.Context, tag, name string, maxSize int64) ([]byte, error)
	// verifySignature checks that bundleJSON is a valid signature over
	// checksums made by the fullsend release workflow for tag.
	verifySignature func(ctx context.Context, bundleJSON, checksums []byte, tag string) error
}

// New returns an Updater for the current platform that talks to GitHub
// Releases and the Sigstore public-good instance.
func New() *Updater {
	return &Updater{
		goos:            runtime.GOOS,
		goarch:          runtime.GOARCH,
		executable:      os.Executable,
		latestTag:       binary.ResolveLatestReleaseTag,
		fetchAsset:      binary.FetchReleaseFile,
		verifySignature: verifyReleaseSignature,
	}
}

// Run updates the running binary according to opts. Once ctx is cancelled,
// the binary is not replaced.
func (u *Updater) Run(ctx context.Context, opts Options) (Result, error) {
	current, ok := parseVersion(opts.CurrentVersion)
	if !ok {
		return Result{}, fmt.Errorf("%w (version %q)", ErrDevBuild, opts.CurrentVersion)
	}

	exe, err := u.resolveExecutable()
	if err != nil {
		return Result{}, err
	}
	if err := checkNotManaged(exe); err != nil {
		return Result{}, err
	}

	target, err := u.resolveTarget(ctx, opts.TargetVersion)
	if err != nil {
		return Result{}, err
	}

	res := Result{From: current.tag(), To: target.tag(), Path: exe}
	switch cmp := target.compare(current); {
	case cmp == 0:
		res.Status = StatusUpToDate
		return res, nil
	case cmp < 0 && opts.TargetVersion == "":
		res.Status = StatusAhead
		return res, nil
	}

	if opts.DryRun {
		// A requested release may not exist, so check it has a build for
		// this platform. The latest release was just looked up.
		if opts.TargetVersion != "" {
			if _, _, _, err := u.platformChecksum(ctx, target.tag()); err != nil {
				return Result{}, err
			}
		}
		res.Status = StatusWouldUpdate
		return res, nil
	}

	if err := u.install(ctx, target, exe); err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("self-update cancelled, %s was not changed: %w", exe, ctx.Err())
		}
		return Result{}, err
	}
	res.Status = StatusUpdated
	return res, nil
}

func (u *Updater) resolveExecutable() (string, error) {
	exe, err := u.executable()
	if err != nil {
		return "", fmt.Errorf("locating the running fullsend binary: %w", err)
	}
	// Replace the real file, not a symlink pointing at it.
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", exe, err)
	}
	return resolved, nil
}

func (u *Updater) resolveTarget(ctx context.Context, requested string) (version, error) {
	tag := requested
	if tag == "" {
		latest, err := u.latestTag(ctx)
		if err != nil {
			return version{}, fmt.Errorf("finding the latest release: %w", err)
		}
		tag = latest
	}
	v, ok := parseVersion(tag)
	if !ok {
		if requested != "" {
			return version{}, fmt.Errorf("invalid version %q: want a release version such as v0.53.0", requested)
		}
		return version{}, fmt.Errorf("latest release has unexpected tag %q", tag)
	}
	if requested != "" && v.compare(minInstallable) < 0 {
		return version{}, fmt.Errorf("%s can't be installed with self-update: releases before %s have no signature it can verify; download it from GitHub Releases instead", v.tag(), minInstallable.tag())
	}
	return v, nil
}

// checkNotManaged refuses binaries that Homebrew or Nix owns, so their
// records stay correct.
func checkNotManaged(exe string) error {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/"):
		return fmt.Errorf("%s is managed by Homebrew; update it with your package manager", exe)
	case strings.HasPrefix(p, "/nix/store/"):
		return fmt.Errorf("%s is in the Nix store; update fullsend through Nix instead", exe)
	}
	return nil
}

// install downloads, verifies, and atomically swaps in the binary for
// target. It creates the temp file next to exe before downloading, so a
// permission problem fails early and the rename stays on one filesystem.
func (u *Updater) install(ctx context.Context, target version, exe string) (err error) {
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("reading %s: %w", exe, err)
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".fullsend-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("cannot write to %s: re-run with sudo, or reinstall fullsend in a directory you own: %w", dir, err)
		}
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	archive, err := u.download(ctx, target)
	if err != nil {
		return err
	}
	if err := binary.ExtractFullsendFromTarGz(bytes.NewReader(archive), tmpPath); err != nil {
		return fmt.Errorf("extracting fullsend from the release archive: %w", err)
	}
	// Flush the new binary to disk, so a crash after the rename can't
	// leave an empty or partial file in place of the old one.
	if err := syncFile(tmpPath); err != nil {
		return fmt.Errorf("writing the new binary to disk: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		return fmt.Errorf("setting permissions on the new binary: %w", err)
	}
	// Ctrl-C only cancels ctx (main absorbs the signal), so check it
	// before the binary is replaced.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// platformChecksum downloads checksums.txt for tag and returns it, with the
// name of this platform's archive and its expected SHA256. The checksums
// are not verified yet.
func (u *Updater) platformChecksum(ctx context.Context, tag string) (checksums []byte, assetName, want string, err error) {
	checksums, err = u.fetchAsset(ctx, tag, checksumsAsset, maxChecksumsSize)
	if errors.Is(err, binary.ErrReleaseFileNotFound) {
		return nil, "", "", fmt.Errorf("release %s not found", tag)
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("release %s: downloading %s: %w", tag, checksumsAsset, err)
	}
	assetName = fmt.Sprintf("fullsend_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), u.goos, u.goarch)
	want, err = binary.ChecksumForAsset(checksums, assetName)
	if err != nil {
		return nil, "", "", fmt.Errorf("release %s has no build for %s/%s: %w", tag, u.goos, u.goarch, err)
	}
	return checksums, assetName, want, nil
}

// download fetches the platform archive for target and returns it once the
// checksums signature and the archive checksum both verify.
func (u *Updater) download(ctx context.Context, target version) ([]byte, error) {
	tag := target.tag()
	checksums, assetName, want, err := u.platformChecksum(ctx, tag)
	if err != nil {
		return nil, err
	}
	bundleJSON, err := u.fetchAsset(ctx, tag, bundleAsset, maxBundleSize)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", bundleAsset, err)
	}
	if err := u.verifySignature(ctx, bundleJSON, checksums, tag); err != nil {
		return nil, fmt.Errorf("release %s failed signature verification, refusing to update: %w", tag, err)
	}

	archive, err := u.fetchAsset(ctx, tag, assetName, maxArchiveSize)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", assetName, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}
	return archive, nil
}
