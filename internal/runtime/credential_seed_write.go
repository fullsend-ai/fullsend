package runtime

import "strconv"

// seedLockWaitSeconds bounds how long a credential seed waits for the
// seed lock. The lock is held only for the few commands that write one
// small file, so a wait this long means the holder is stuck; the seed then
// fails closed rather than writing without the ordering guarantee. It
// stays well inside one runner exec (openAIPlaceholderExecTimeout).
const seedLockWaitSeconds = 10

// seedLockSuffix, seedGenerationsSuffix and seedTempSuffix name the files a
// credential seed keeps next to its credential file.
//
// seedLockSuffix must not be ".lock". pi locks auth.json through
// proper-lockfile, which takes its lock by creating a directory at
// auth.json.lock. A regular file left at that path makes pi's lock fail, and
// pi then never loads the seeded credential.
const (
	seedLockSuffix        = ".fullsend-seed.lock"
	seedGenerationsSuffix = ".generations"
	seedTempSuffix        = ".fullsend"
)

// seedPreviousEnv names the variable a re-seed sets to the placeholder it
// replaces. See SeedReplacing.
const seedPreviousEnv = "FULLSEND_SEED_PREVIOUS"

// SeedReplacing returns seed, a fragment built on orderedSeedWrite, run so
// that it first records previous, the placeholder the re-seed replaces, as
// rotated out. The refresher knows previous from the rotation it observed;
// a seed holding it may not have written yet, and without this record that
// seed would not be recognized as stale (fullsend#8311). An empty previous
// returns seed unchanged.
func SeedReplacing(seed, previous string) string {
	if previous == "" {
		return seed
	}
	return seedPreviousEnv + "=" + shellQuote(previous) + "; export " + seedPreviousEnv + "; " + seed
}

// orderedSeedWrite renders the POSIX sh fragment a credential seed uses to
// replace its credential file, so that a seed holding an older generation
// cannot replace a newer one (fullsend#8311).
//
// The iteration-start seed takes its placeholder from its own environment
// and may stall at any point. Meanwhile the refresher's re-seed can write
// and verify a newer placeholder. Placeholders are opaque, so the newer one
// cannot be recognized from its text. But a generation never returns once
// it has been rotated out. So every writer records its placeholder in an
// append-only history file (final + seedGenerationsSuffix) before its
// rename. A placeholder that appears in the history but is not its last
// line was replaced by a newer generation, and its writer skips the rename.
// Skipping is not a failure: the file already holds a newer generation.
//
// A stalled seed may not have recorded its placeholder when the refresher
// rotates past it, so arrival order alone cannot mark it stale. The
// refresher therefore runs its re-seed through SeedReplacing, which names
// the placeholder being replaced in seedPreviousEnv. Under the lock, before
// anything else, a re-seed puts that placeholder in the history ahead of
// every other entry unless it is already there. The refresher rotates one
// generation at a time and re-seeds each, so every generation it rotates out
// is recorded this way.
//
// The check, the history append and the rename run under flock(1) on
// final + seedLockSuffix. That makes them one step against every other
// seed: the iteration-start seed runs in the sandbox, and the refresher's
// host-side sandbox lock does not cover it. The kernel drops the lock when
// its holder exits, so a seed killed mid-write cannot wedge later seeds.
// A lock that cannot be taken within seedLockWaitSeconds fails the seed.
//
// The placeholder is recorded before the rename. A seed that dies between
// the two leaves the history one step ahead of the file, and that is safe:
// the placeholder the file still holds is older and stays refused, and
// any newer placeholder can still be written. Recording after the rename
// would let the generation it replaced be written back.
//
// envVar names the variable holding the placeholder; the caller must have
// validated its value to placeholder characters first. dir and final are
// unquoted paths. The temp file is final + seedTempSuffix + the shell's pid.
// write is the command whose stdout becomes the file's content. failMsg and
// failStmt report a failed write after the temp files are removed.
func orderedSeedWrite(envVar, dir, final, write, failMsg, failStmt string) string {
	lock := shellQuote(final + seedLockSuffix)
	generations := shellQuote(final + seedGenerationsSuffix)
	tmp := shellQuote(final+seedTempSuffix) + `.$$`
	generationsTmp := generations + `.$$`
	v := `"$` + envVar + `"`
	last := `"$(command -p tail -n 1 ` + generations + ` 2>/dev/null)"`
	prev := `"$` + seedPreviousEnv + `"`
	validatePrev := `case "${` + seedPreviousEnv + `:-}" in *[!A-Za-z0-9_:]*) echo 'fullsend: ` + seedPreviousEnv + ` has unexpected characters; refusing to seed' >&2; ` + failStmt + ` ;; esac`
	recordPrev := `{ test -z "${` + seedPreviousEnv + `:-}" || command -p grep -qxF -e ` + prev + ` ` + generations + ` 2>/dev/null ||` +
		` { { printf '%s\n' ` + prev + `; command -p cat ` + generations + ` 2>/dev/null || :; } > ` + generationsTmp +
		` && command -p mv -f ` + generationsTmp + ` ` + generations + `; }; }`
	return `{ ` + validatePrev + ` && command -p mkdir -p ` + shellQuote(dir) +
		` && { command -p flock -w ` + strconv.Itoa(seedLockWaitSeconds) + ` 9` +
		` && ` + recordPrev +
		` && if test ` + last + ` != ` + v + ` && command -p grep -qxF -e ` + v + ` ` + generations + ` 2>/dev/null;` +
		` then echo 'fullsend: ` + envVar + ` is an older generation than the credential file holds; leaving the file as it is' >&2;` +
		` else { test ` + last + ` = ` + v + ` || printf '%s\n' ` + v + ` >> ` + generations + `; }` +
		` && ` + write + ` > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + shellQuote(final) + `; fi; } 9>>` + lock +
		` || { command -p rm -f ` + tmp + ` ` + generationsTmp + `; echo '` + failMsg + `' >&2; ` + failStmt + `; }; }`
}
