package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// requireSeedLock skips a test that runs a credential seed when the host
// has no flock(1) on the default PATH, which the seed resolves it from.
// The sandbox image has it (util-linux); some developer machines do not.
func requireSeedLock(t *testing.T) {
	t.Helper()
	if err := exec.Command("sh", "-c", "command -p flock -V").Run(); err != nil {
		t.Skip("flock(1) not on the default PATH")
	}
}

// orderedSeedCase is one credential seed rendered against a temp dir.
type orderedSeedCase struct {
	name string
	env  string
	file string
	seed func(dir string) string
}

func orderedSeedCases() []orderedSeedCase {
	return []orderedSeedCase{
		{
			name: "pi openai",
			env:  "OPENAI_API_KEY",
			file: piOpenAIAuthFile,
			seed: PiOpenAIAuthSeed,
		},
		{
			name: "pi gateway",
			env:  piGatewayCredentialEnv,
			file: piInferenceGatewayTokenFile,
			seed: PiGatewayTokenSeed,
		},
		{
			name: "codex openai",
			env:  "OPENAI_API_KEY",
			file: codexTokenFile,
			seed: func(dir string) string {
				return strings.ReplaceAll(CodexRuntime{}.OpenAIAuthSeed(), sandbox.SandboxCodexConfig, dir)
			},
		},
	}
}

func (c orderedSeedCase) placeholder(gen string) string {
	return piPlaceholderPrefix + "v" + gen + "_" + c.env
}

func (c orderedSeedCase) command(seed, gen string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", seed)
	cmd.Env = append(os.Environ(), c.env+"="+c.placeholder(gen))
	return cmd
}

func (c orderedSeedCase) run(t *testing.T, dir, gen string) {
	t.Helper()
	out, err := c.command(c.seed(dir), gen).CombinedOutput()
	require.NoError(t, err, "seed %s: %s", gen, out)
}

func (c orderedSeedCase) holds(t *testing.T, dir, gen string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, c.file))
	require.NoError(t, err)
	assert.Contains(t, string(data), c.placeholder(gen), "the credential file holds generation %s", gen)
	for _, other := range []string{"1", "2", "3"} {
		if other != gen {
			assert.NotContains(t, string(data), c.placeholder(other))
		}
	}
}

// pausedAt injects a pause into seed just before anchor. The paused shell
// creates ready and waits until release exists. anchor must occur exactly
// once, so the pause lands where the test means it to.
func pausedAt(t *testing.T, seed, anchor, ready, release string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(seed, anchor), "pause anchor %q", anchor)
	pause := `{ : > ` + shellQuote(ready) + ` && while ! test -f ` + shellQuote(release) + `; do command -p sleep 0.01; done; } && `
	return strings.Replace(seed, anchor, pause+anchor, 1)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 10*time.Second, 5*time.Millisecond, "waiting for %s", path)
}

// TestOrderedSeed_StaleWriterCannotReplaceNewer reproduces fullsend#8311
// for every seed. An iteration-start seed captures placeholder 1 from its
// environment and stalls. A re-seed then writes placeholder 2 and verifies
// it. The stalled seed resumes, and the file must still hold 2.
func TestOrderedSeed_StaleWriterCannotReplaceNewer(t *testing.T) {
	requireSeedLock(t)
	for _, c := range orderedSeedCases() {
		t.Run(c.name+"/stalled before the lock", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			sync := t.TempDir()
			ready, release := filepath.Join(sync, "ready"), filepath.Join(sync, "release")

			// The file already holds generation 1, as it would once the
			// previous iteration or refresh had seeded it.
			c.run(t, dir, "1")

			old := c.command(pausedAt(t, c.seed(dir), "command -p flock", ready, release), "1")
			var oldOut strings.Builder
			old.Stdout, old.Stderr = &oldOut, &oldOut
			require.NoError(t, old.Start())
			waitForFile(t, ready)

			c.run(t, dir, "2")
			c.holds(t, dir, "2")

			require.NoError(t, os.WriteFile(release, nil, 0o600))
			require.NoError(t, old.Wait(), "a stale seed is not a failure: %s", oldOut.String())
			assert.Contains(t, oldOut.String(), "older generation")
			c.holds(t, dir, "2")
			assertOnlySeedFiles(t, dir, c.file)
		})

		t.Run(c.name+"/stalled before the rename", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			sync := t.TempDir()
			ready, release := filepath.Join(sync, "ready"), filepath.Join(sync, "release")
			c.run(t, dir, "1")

			// The stale seed is paused inside the lock, just before its
			// rename. The re-seed cannot write until it lets go, so the
			// re-seed lands last.
			old := c.command(pausedAt(t, c.seed(dir), "command -p mv -f "+shellQuote(filepath.Join(dir, c.file+seedTempSuffix)), ready, release), "1")
			var oldOut strings.Builder
			old.Stdout, old.Stderr = &oldOut, &oldOut
			require.NoError(t, old.Start())
			waitForFile(t, ready)

			fresh := c.command(c.seed(dir), "2")
			var freshOut strings.Builder
			fresh.Stdout, fresh.Stderr = &freshOut, &freshOut
			require.NoError(t, fresh.Start())
			done := make(chan error, 1)
			go func() { done <- fresh.Wait() }()
			select {
			case err := <-done:
				t.Fatalf("the re-seed finished while the stale seed held the lock: %v: %s", err, freshOut.String())
			case <-time.After(200 * time.Millisecond):
			}

			require.NoError(t, os.WriteFile(release, nil, 0o600))
			require.NoError(t, old.Wait(), oldOut.String())
			require.NoError(t, <-done, freshOut.String())
			c.holds(t, dir, "2")
			assertOnlySeedFiles(t, dir, c.file)
		})

		t.Run(c.name+"/newer generations replace older ones", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			c.run(t, dir, "1")
			c.holds(t, dir, "1")
			c.run(t, dir, "2")
			c.holds(t, dir, "2")
			// Re-seeding the generation the file holds rewrites it.
			require.NoError(t, os.Remove(filepath.Join(dir, c.file)))
			c.run(t, dir, "2")
			c.holds(t, dir, "2")
			// A replaced generation stays refused, however late it shows up.
			c.run(t, dir, "1")
			c.holds(t, dir, "2")
			c.run(t, dir, "3")
			c.holds(t, dir, "3")
			c.run(t, dir, "2")
			c.holds(t, dir, "3")
			assertOnlySeedFiles(t, dir, c.file)
		})

		t.Run(c.name+"/a failed write leaves no temp file", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			c.run(t, dir, "1")
			// Recording the generation fails: the history's path is now
			// a directory. The seed fails closed and writes nothing.
			gens := filepath.Join(dir, c.file+seedGenerationsSuffix)
			require.NoError(t, os.Remove(gens))
			require.NoError(t, os.MkdirAll(gens, 0o755))
			out, err := c.command(c.seed(dir), "2").CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), "failed")
			c.holds(t, dir, "1")
			assertOnlySeedFiles(t, dir, c.file)
		})

		// With no initial seed the history is empty, so the stalled seed
		// has recorded nothing when the refresher rotates past it. The
		// re-seed names the placeholder it replaces, and that alone must
		// keep the stalled seed from overwriting the newer file.
		t.Run(c.name+"/stalled before the lock with no initial seed", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			sync := t.TempDir()
			ready, release := filepath.Join(sync, "ready"), filepath.Join(sync, "release")

			old := c.command(pausedAt(t, c.seed(dir), "command -p flock", ready, release), "1")
			var oldOut strings.Builder
			old.Stdout, old.Stderr = &oldOut, &oldOut
			require.NoError(t, old.Start())
			waitForFile(t, ready)

			out, err := c.command(SeedReplacing(c.seed(dir), c.placeholder("1")), "2").CombinedOutput()
			require.NoError(t, err, string(out))
			c.holds(t, dir, "2")

			require.NoError(t, os.WriteFile(release, nil, 0o600))
			require.NoError(t, old.Wait(), "a stale seed is not a failure: %s", oldOut.String())
			assert.Contains(t, oldOut.String(), "older generation")
			c.holds(t, dir, "2")
			assertOnlySeedFiles(t, dir, c.file)

			// The next rotation records generation 2 the same way.
			out, err = c.command(SeedReplacing(c.seed(dir), c.placeholder("2")), "3").CombinedOutput()
			require.NoError(t, err, string(out))
			c.holds(t, dir, "3")
			c.run(t, dir, "2")
			c.holds(t, dir, "3")
		})

		// A seed for the next generation that ran before the refresher's
		// re-seed leaves the history ahead of it; the replaced placeholder
		// is recorded before it, not as the newest.
		t.Run(c.name+"/replaced placeholder is recorded behind a newer one", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			c.run(t, dir, "2")
			out, err := c.command(SeedReplacing(c.seed(dir), c.placeholder("1")), "2").CombinedOutput()
			require.NoError(t, err, string(out))
			c.holds(t, dir, "2")
			c.run(t, dir, "1")
			c.holds(t, dir, "2")
		})

		t.Run(c.name+"/lock does not collide with pi's auth.json.lock", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			c.run(t, dir, "1")
			// proper-lockfile takes its lock by mkdir on <file>.lock; a
			// regular file there would break pi's credential storage.
			_, err := os.Lstat(filepath.Join(dir, c.file+".lock"))
			assert.True(t, os.IsNotExist(err), "seed left %s.lock behind", c.file)
		})

		t.Run(c.name+"/unexpected previous placeholder fails the seed", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cfg")
			out, err := c.command(SeedReplacing(c.seed(dir), "x\ny z"), "1").CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), "unexpected characters")
			_, statErr := os.Stat(filepath.Join(dir, c.file))
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

// assertOnlySeedFiles checks dir holds the credential file and the seed's
// two sidecar files, and no temp file.
func assertOnlySeedFiles(t *testing.T, dir, file string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{file, file + seedGenerationsSuffix, file + seedLockSuffix}
	sort.Strings(want)
	assert.Equal(t, want, names)
}
