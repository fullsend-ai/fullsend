package binary

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReleaseBaseURL is the GitHub releases download base URL. Tests may override.
// Not safe for concurrent test mutation.
var ReleaseBaseURL = "https://github.com/fullsend-ai/fullsend/releases/download"

// HTTPClient is used for release downloads. Tests may override.
// Not safe for concurrent test mutation.
var HTTPClient = &http.Client{Timeout: 120 * time.Second}

const defaultMaxDownloadSize = 200 * 1024 * 1024 // 200 MB compressed

// maxDownloadSize caps release asset downloads. Tests may lower temporarily.
var maxDownloadSize = defaultMaxDownloadSize

const maxBinarySize = 500 * 1024 * 1024 // 500 MB — reasonable upper bound for a Go binary

// DownloadRelease downloads the fullsend binary for linux/{arch} from the
// GitHub Release matching the given version, verifies its SHA256 checksum
// against the release checksums.txt, and writes it to destPath.
func DownloadRelease(ver, arch, destPath string) error {
	cleanVer := strings.TrimPrefix(ver, "v")
	assetName := fmt.Sprintf("fullsend_%s_linux_%s.tar.gz", cleanVer, arch)

	expectedHash, err := downloadChecksumForAsset(ver, assetName)
	if err != nil {
		return fmt.Errorf("fetching checksum for %s: %w", assetName, err)
	}

	url := fmt.Sprintf("%s/v%s/%s", ReleaseBaseURL, cleanVer, assetName)
	resp, err := HTTPClient.Get(url) //nolint:gosec // URL is constructed from known constants
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	maxSize := int64(maxDownloadSize)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(resp.Body, maxSize+1)); err != nil {
		return fmt.Errorf("reading %s: %w", assetName, err)
	}
	if int64(buf.Len()) > maxSize {
		return fmt.Errorf("download of %s exceeds maximum size (%d bytes)", assetName, maxSize)
	}

	h := sha256.Sum256(buf.Bytes())
	actualHash := hex.EncodeToString(h[:])
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, actualHash, expectedHash)
	}

	return ExtractFullsendFromTarGz(bytes.NewReader(buf.Bytes()), destPath)
}

func downloadChecksumForAsset(ver, assetName string) (string, error) {
	cleanVer := strings.TrimPrefix(ver, "v")
	url := fmt.Sprintf("%s/v%s/checksums.txt", ReleaseBaseURL, cleanVer)

	resp, err := HTTPClient.Get(url) //nolint:gosec // URL is constructed from known constants
	if err != nil {
		return "", fmt.Errorf("fetching checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 64*1024))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			hash := strings.ToLower(parts[0])
			if len(hash) != 64 {
				return "", fmt.Errorf("invalid hash length for %s in checksums.txt", assetName)
			}
			if _, err := hex.DecodeString(hash); err != nil {
				return "", fmt.Errorf("invalid hex hash for %s in checksums.txt: %w", assetName, err)
			}
			return hash, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}
	return "", fmt.Errorf("asset %s not found in checksums.txt", assetName)
}

// DownloadLatestRelease resolves the latest release tag from the GitHub API
// and downloads the Linux binary for the given arch.
func DownloadLatestRelease(arch, destPath string) error {
	tag, err := resolveLatestReleaseTag()
	if err != nil {
		return err
	}
	return DownloadRelease(tag, arch, destPath)
}

func resolveLatestReleaseTag() (string, error) {
	resp, err := HTTPClient.Get("https://api.github.com/repos/fullsend-ai/fullsend/releases/latest") //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		return "", fmt.Errorf("parsing release JSON: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("empty tag_name in latest release")
	}
	return release.TagName, nil
}

// SourceArchiveBaseURL is the GitHub source archive base URL. Tests may override.
var SourceArchiveBaseURL = "https://github.com/fullsend-ai/fullsend/archive/refs/tags"

// FetchSourceTree downloads the fullsend source tree for the given release
// version and extracts it into destDir (module root contents, not wrapped).
func FetchSourceTree(version, destDir string) error {
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + strings.TrimPrefix(version, "v")
	}
	url := fmt.Sprintf("%s/%s.tar.gz", SourceArchiveBaseURL, tag)

	resp, err := HTTPClient.Get(url) //nolint:gosec // URL is constructed from known constants
	if err != nil {
		return fmt.Errorf("fetching source archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	maxSize := int64(maxDownloadSize)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(resp.Body, maxSize+1)); err != nil {
		return fmt.Errorf("reading source archive: %w", err)
	}
	if int64(buf.Len()) > maxSize {
		return fmt.Errorf("source archive exceeds maximum size (%d bytes)", maxSize)
	}

	return extractSourceTree(bytes.NewReader(buf.Bytes()), destDir)
}

func pathWithinDir(dir, target string) bool {
	dir = filepath.Clean(dir)
	target = filepath.Clean(target)
	if target == dir {
		return true
	}
	return strings.HasPrefix(target, dir+string(os.PathSeparator))
}

func extractSourceTree(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tmpDir, err := os.MkdirTemp(filepath.Dir(destDir), "fullsend-src-*")
	if err != nil {
		return fmt.Errorf("creating temp extract dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tr := tar.NewReader(gz)
	var rootPrefix string
	var totalExtracted int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading source tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		if rootPrefix == "" {
			parts := strings.SplitN(clean, "/", 2)
			if len(parts) == 0 || parts[0] == "" {
				return fmt.Errorf("unexpected source archive layout")
			}
			rootPrefix = parts[0] + "/"
		}
		if !strings.HasPrefix(clean+"/", rootPrefix) {
			continue
		}
		rel := strings.TrimPrefix(clean, rootPrefix)
		if rel == "" || rel == "." {
			continue
		}
		target := filepath.Join(tmpDir, rel)
		if !pathWithinDir(tmpDir, target) {
			return fmt.Errorf("extract path escapes destination: %s", rel)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating dir %s: %w", rel, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent for %s: %w", rel, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("creating file %s: %w", rel, err)
			}
			n, err := io.Copy(f, io.LimitReader(tr, int64(maxDownloadSize)+1))
			if err != nil {
				f.Close()
				return fmt.Errorf("extracting %s: %w", rel, err)
			}
			if n > int64(maxDownloadSize) {
				f.Close()
				return fmt.Errorf("extracted file %s exceeds maximum size (%d bytes)", rel, maxDownloadSize)
			}
			totalExtracted += n
			if totalExtracted > int64(maxDownloadSize) {
				f.Close()
				return fmt.Errorf("aggregate extracted size exceeds maximum (%d bytes)", maxDownloadSize)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("closing %s: %w", rel, err)
			}
		}
	}

	if err := os.RemoveAll(destDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("preparing dest dir: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating dest dir: %w", err)
	}
	return copyDirContents(tmpDir, destDir)
}

func copyDirContents(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// ExtractFullsendFromTarGz reads a tar.gz stream and extracts the "fullsend"
// binary to destPath.
func ExtractFullsendFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("fullsend binary not found in archive")
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		if filepath.Base(clean) == "fullsend" && hdr.Typeflag == tar.TypeReg {
			f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return fmt.Errorf("creating %s: %w", destPath, err)
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, maxBinarySize+1))
			if copyErr != nil {
				f.Close()
				return fmt.Errorf("extracting fullsend: %w", copyErr)
			}
			if n > maxBinarySize {
				f.Close()
				os.Remove(destPath)
				return fmt.Errorf("binary exceeds maximum size (%d bytes)", maxBinarySize)
			}
			return f.Close()
		}
	}
}
