package artifacts

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRunDir = "fs-cod-0a1b2c"

// contentTelemetry is one OTLP JSON record carrying content-capture
// attributes next to metadata attributes.
const contentTelemetry = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"fullsend"}}]},"scopeSpans":[{"scope":{"name":"fullsend"},"spans":[{"traceId":"01","spanId":"02","name":"invoke_agent","kind":1,"startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"gen_ai.usage.input_tokens","value":{"intValue":"9007199254740993"}},{"key":"gen_ai.input.messages","value":{"stringValue":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"private jira text\"}]}]"}},{"key":"gen_ai.output.messages","value":{"stringValue":"private answer"}},{"key":"fullsend.content.truncated","value":{"boolValue":true}}]}]}]}]}
`

// writeTree creates files (relative path → content) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// listTree returns every regular file and symlink under root, relative
// and slash-separated, sorted.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(out)
	return out
}

func fullRun() map[string]string {
	return map[string]string{
		testRunDir + "/metrics.json":                         `{"iterations":1}`,
		testRunDir + "/eval-measurements.jsonl":              `{"score":1}` + "\n",
		testRunDir + "/run-telemetry.jsonl":                  contentTelemetry,
		testRunDir + "/security/findings.jsonl":              `{"detail":"quoted tool output"}` + "\n",
		testRunDir + "/iteration-1/output.jsonl":             `{"type":"assistant"}` + "\n",
		testRunDir + "/iteration-1/transcripts/t.jsonl":      "transcript\n",
		testRunDir + "/iteration-1/output/agent-result.json": `{}`,
		testRunDir + "/iteration-1/metrics.json":             `{"nested":true}`,
		testRunDir + "/notes.txt":                            "other file",
		".fullsend-measure-code.yaml":                        "manifest",
		"run-telemetry.jsonl":                                contentTelemetry,
		"not-a-run-dir/metrics.json":                         `{}`,
	}
}

func TestStage_All(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTree(t, out, fullRun())
	require.NoError(t, os.Symlink("metrics.json", filepath.Join(out, testRunDir, "link.json")))
	before := listTree(t, out)

	require.NoError(t, Stage(out, staged, "all"))
	assert.Equal(t, before, listTree(t, staged), "all stages the whole tree")
	got, err := os.ReadFile(filepath.Join(staged, testRunDir, "run-telemetry.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, contentTelemetry, string(got), "all leaves telemetry as written")
	target, err := os.Readlink(filepath.Join(staged, testRunDir, "link.json"))
	require.NoError(t, err)
	assert.Equal(t, "metrics.json", target, "symlinks stay symlinks")
	assert.Equal(t, before, listTree(t, out), "output is untouched")
}

func TestStage_Metadata(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTree(t, out, fullRun())
	before := listTree(t, out)

	require.NoError(t, Stage(out, staged, "metadata"))
	assert.Equal(t, []string{
		testRunDir + "/eval-measurements.jsonl",
		testRunDir + "/metrics.json",
		testRunDir + "/run-telemetry.jsonl",
	}, listTree(t, staged))

	tel, err := os.ReadFile(filepath.Join(staged, testRunDir, "run-telemetry.jsonl"))
	require.NoError(t, err)
	s := string(tel)
	assert.NotContains(t, s, "private")
	assert.NotContains(t, s, "gen_ai.input.messages")
	assert.NotContains(t, s, "gen_ai.output.messages")
	assert.NotContains(t, s, "fullsend.content.")
	assert.Contains(t, s, `"gen_ai.usage.input_tokens"`)
	assert.Contains(t, s, `"9007199254740993"`)
	assert.Equal(t, before, listTree(t, out), "output is untouched")
	raw, err := os.ReadFile(filepath.Join(out, testRunDir, "run-telemetry.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, contentTelemetry, string(raw))
}

func TestStage_MetadataDropsUnparseableTelemetry(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTree(t, out, map[string]string{
		testRunDir + "/metrics.json":        `{}`,
		testRunDir + "/run-telemetry.jsonl": contentTelemetry + "{not json\n",
	})
	require.NoError(t, Stage(out, staged, "metadata"))
	assert.Equal(t, []string{testRunDir + "/metrics.json"}, listTree(t, staged))
}

func TestStage_MetadataSkipsSymlinks(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	secret := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(out, testRunDir), 0o755))
	require.NoError(t, os.Symlink(secret, filepath.Join(out, testRunDir, "metrics.json")))
	linkedRun := t.TempDir()
	writeTree(t, linkedRun, map[string]string{"metrics.json": "linked"})
	require.NoError(t, os.Symlink(linkedRun, filepath.Join(out, "agent-code-1-2")))

	require.NoError(t, Stage(out, staged, "metadata"))
	assert.Empty(t, listTree(t, staged))
}

func TestStage_None(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTree(t, out, fullRun())
	writeTree(t, staged, map[string]string{"stale.txt": "previous job"})

	require.NoError(t, Stage(out, staged, "none"))
	assert.Empty(t, listTree(t, staged))
}

func TestStage_UnknownLevelLeavesStagedEmpty(t *testing.T) {
	out := t.TempDir()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTree(t, out, fullRun())
	err := Stage(out, staged, "everything")
	require.Error(t, err)
	assert.Empty(t, listTree(t, staged))
}

func TestStage_MissingOutputDir(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "staged")
	missing := filepath.Join(t.TempDir(), "absent")
	for _, level := range []string{"all", "metadata", "none"} {
		require.NoError(t, Stage(missing, staged, level), level)
		assert.Empty(t, listTree(t, staged), level)
	}
}

func TestPrepareStagedDir_RejectsOverlap(t *testing.T) {
	out := t.TempDir()
	writeTree(t, out, map[string]string{"keep.txt": "raw"})
	for _, staged := range []string{out, filepath.Join(out, "staged"), filepath.Dir(out)} {
		err := PrepareStagedDir(out, staged)
		require.Error(t, err, staged)
		assert.True(t, strings.Contains(err.Error(), "overlap"), err.Error())
	}
	assert.Error(t, PrepareStagedDir(out, ""))
	assert.Equal(t, []string{"keep.txt"}, listTree(t, out), "the raw output survives a rejected stage")
}

func TestMetadataFiles(t *testing.T) {
	assert.Equal(t, []string{"metrics.json", "eval-measurements.jsonl", "run-telemetry.jsonl"}, MetadataFiles())
}
