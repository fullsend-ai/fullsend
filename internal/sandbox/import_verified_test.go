package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeVerifiedOpenshellStub installs an openshell binary that logs every
// invocation and answers `provider list-profiles` from listJSON. listJSON
// may be a path prefixed with "@" to read the listing from that file on
// each call (so a test can change the gateway's answer between imports).
func writeVerifiedOpenshellStub(t *testing.T, argsLog, listJSON string) {
	t.Helper()
	dir := filepath.Dir(argsLog)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(argsLog) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'provider list-profiles')\n"
	if len(listJSON) > 0 && listJSON[0] == '@' {
		script += "    cat " + shellQuote(listJSON[1:]) + "\n"
	} else {
		script += "    printf '%s\\n' " + shellQuote(listJSON) + "\n"
	}
	script += "    exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestImportProfileVerified_PresentAfterFirstImport(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))
	argsLog := filepath.Join(dir, "args.log")
	writeVerifiedOpenshellStub(t, argsLog, `[{"id":"fullsend-vertex-ai"}]`)

	require.NoError(t, ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath))

	data, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := splitNonEmpty(string(data))
	require.Len(t, lines, 3, "delete, import, list: %q", lines)
	assert.Equal(t, "provider profile delete fullsend-vertex-ai", lines[0])
	assert.Equal(t, "provider profile import --file "+profilePath, lines[1])
	assert.Equal(t, "provider list-profiles -o json", lines[2])
}

func TestImportProfileVerified_StaleCacheAgainstEmptyGateway(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))

	// The regression #7218 calls out: ImportProfile trusts os.TempDir() and
	// skips the send on a hash match. Against a freshly-recreated (empty)
	// gateway that skip leaves the profile unregistered.
	hash, err := hashProfileFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profileFileCachePath("fullsend-vertex-ai"), []byte(hash), 0o600))

	argsLog := filepath.Join(dir, "args.log")
	listing := filepath.Join(dir, "listing.json")
	require.NoError(t, os.WriteFile(listing, []byte("[]\n"), 0o644))
	writeVerifiedOpenshellStub(t, argsLog, "@"+listing)

	err = ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath)
	require.Error(t, err, "empty listing after both attempts must fail")
	assert.Contains(t, err.Error(), "not on the gateway after import")

	data, readErr := os.ReadFile(argsLog)
	require.NoError(t, readErr)
	lines := splitNonEmpty(string(data))
	// forget cache + delete + import + list (missing) + forget + delete + import + list.
	require.Len(t, lines, 6, "%q", lines)
	assert.Equal(t, "provider profile delete fullsend-vertex-ai", lines[0], "stale cache must not skip the first send")
	assert.Equal(t, "provider profile import --file "+profilePath, lines[1])
	assert.Equal(t, "provider list-profiles -o json", lines[2])
	assert.Equal(t, "provider profile delete fullsend-vertex-ai", lines[3], "retry after empty listing")
	assert.Equal(t, "provider profile import --file "+profilePath, lines[4])
	assert.Equal(t, "provider list-profiles -o json", lines[5])
}

func TestImportProfileVerified_RetrySucceedsWhenGatewayCatchesUp(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))

	argsLog := filepath.Join(dir, "args.log")
	countFile := filepath.Join(dir, "list.count")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(argsLog) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'provider list-profiles')\n" +
		"    n=0; [ -f " + shellQuote(countFile) + " ] && n=$(cat " + shellQuote(countFile) + ")\n" +
		"    n=$((n + 1)); echo \"$n\" > " + shellQuote(countFile) + "\n" +
		"    if [ \"$n\" -eq 1 ]; then echo '[]'; else echo '[{\"id\":\"fullsend-vertex-ai\"}]'; fi\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath))

	data, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := splitNonEmpty(string(data))
	require.Len(t, lines, 6, "import, miss, retry, hit: %q", lines)
	assert.Equal(t, "provider list-profiles -o json", lines[2])
	assert.Equal(t, "provider profile delete fullsend-vertex-ai", lines[3])
	assert.Equal(t, "provider list-profiles -o json", lines[5])
}

func TestImportProfileVerified_ImportError(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))
	script := "#!/bin/sh\necho boom >&2; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile import")
}

func TestImportProfileVerified_ListError(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  'provider list-profiles') echo 'gateway unreachable' >&2; exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking provider profile")
}

// TestImportProfileVerified_RetryListError covers the retry path's
// ProfileExists call specifically: the first list must succeed (empty, so a
// retry is attempted) and the second list must fail, so a genuine listing
// error on the retry is reported as such (wrapped, via "checking provider
// profile") instead of being mislabeled as "not on the gateway after import".
func TestImportProfileVerified_RetryListError(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "vertex.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: fullsend-vertex-ai\n"), 0o644))

	argsLog := filepath.Join(dir, "args.log")
	countFile := filepath.Join(dir, "list.count")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(argsLog) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'provider list-profiles')\n" +
		"    n=0; [ -f " + shellQuote(countFile) + " ] && n=$(cat " + shellQuote(countFile) + ")\n" +
		"    n=$((n + 1)); echo \"$n\" > " + shellQuote(countFile) + "\n" +
		"    if [ \"$n\" -eq 1 ]; then echo '[]'; else echo 'gateway unreachable' >&2; exit 1; fi\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := ImportProfileVerified(context.Background(), "fullsend-vertex-ai", profilePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking provider profile")
	assert.NotContains(t, err.Error(), "not on the gateway after import")
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
