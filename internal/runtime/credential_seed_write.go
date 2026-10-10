package runtime

import (
	"strconv"
	"strings"
)

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

// seedPreviousEnv names the variable a re-seed sets to the placeholders it
// retires, space-separated. See SeedReplacing.
const seedPreviousEnv = "FULLSEND_SEED_PREVIOUS"

// SeedReplacing returns seed, a fragment built on orderedSeedWrite, run so
// that it first records previous, the placeholders the re-seed retires, as
// rotated out. The refresher knows them from the rotations it observed; a
// seed holding one may not have written yet, and without this record that
// seed would not be recognized as stale (fullsend#8311). previous holds the
// placeholder the file named and any generation the refresher saw in the
// sandbox whose own hand-off failed before it was recorded: a later rotation
// retires those too. Empty entries are dropped; with none left, seed is
// returned unchanged.
func SeedReplacing(seed string, previous ...string) string {
	var kept []string
	for _, p := range previous {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return seed
	}
	return seedPreviousEnv + "=" + shellQuote(strings.Join(kept, " ")) + "; export " + seedPreviousEnv + "; " + seed
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
// Skipping is not a failure when the file already holds that newer
// generation, so the writer first checks that the file names the history's
// last entry, which must be a well-formed placeholder that the file holds as
// a whole token, not a blank line or a fragment of some other value. The history is a plain file in a directory the agent can
// write: an agent that truncates or reorders it must not turn a skipped
// write into a silent success while the file holds something else. A file
// that does not name the last entry fails the seed instead.
//
// A stalled seed may not have recorded its placeholder when the refresher
// rotates past it, so arrival order alone cannot mark it stale. The
// refresher therefore runs its re-seed through SeedReplacing, which names
// the placeholders being retired in seedPreviousEnv. Under the lock, before
// anything else, a re-seed puts each of them that is missing in the history
// ahead of every other entry. The refresher rotates one generation at a time
// and re-seeds each, and it carries a generation whose hand-off failed over
// to the next re-seed, so every generation it rotates out is recorded this
// way.
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
// the placeholder the file still holds is older and stays refused (the seed
// fails rather than skip, per the check above, until a newer placeholder is
// written), and any newer placeholder can still be written. Recording after
// the rename would let the generation it replaced be written back.
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
	last := `"$fullsend_last"`
	readLast := `{ fullsend_last=$(command -p tail -n 1 ` + generations + ` 2>/dev/null) || :; }`
	// lastHeld succeeds only when the history's last entry is a well-formed
	// placeholder and the credential file holds it as a whole token. A blank
	// or truncated entry, or one that is merely part of some other value,
	// does not show that the file holds the newest generation.
	lastHeld := `{ case ` + last + ` in ''|*[!A-Za-z0-9_:]*) command -p false ;;` +
		` *) command -p grep -Eq -e "(^|[^A-Za-z0-9_:])${fullsend_last}([^A-Za-z0-9_:]|\$)" ` + shellQuote(final) + ` 2>/dev/null ;; esac; }`
	validatePrev := `case "${` + seedPreviousEnv + `:-}" in *[!A-Za-z0-9_:\ ]*) echo 'fullsend: ` + seedPreviousEnv + ` has unexpected characters; refusing to seed' >&2; ` + failStmt + ` ;; esac`
	recordPrev := `{ test -z "${` + seedPreviousEnv + `:-}" ||` +
		` { { for fullsend_g in $` + seedPreviousEnv + `; do command -p grep -qxF -e "$fullsend_g" ` + generations + ` 2>/dev/null || printf '%s\n' "$fullsend_g"; done;` +
		` command -p cat ` + generations + ` 2>/dev/null || :; } > ` + generationsTmp +
		` && command -p mv -f ` + generationsTmp + ` ` + generations + `; }; }`
	return `{ ` + validatePrev + ` && command -p mkdir -p ` + shellQuote(dir) +
		` && { command -p flock -w ` + strconv.Itoa(seedLockWaitSeconds) + ` 9` +
		` && ` + recordPrev +
		` && ` + readLast +
		` && if test ` + last + ` != ` + v + ` && command -p grep -qxF -e ` + v + ` ` + generations + ` 2>/dev/null;` +
		` then if ` + lastHeld + `;` +
		` then echo 'fullsend: ` + envVar + ` is an older generation than the credential file holds; leaving the file as it is' >&2;` +
		` else echo 'fullsend: ` + envVar + ` is older than the newest generation in the history, but the credential file does not hold that one; not treating the skipped write as done' >&2; command -p false; fi;` +
		` else { test ` + last + ` = ` + v + ` || printf '%s\n' ` + v + ` >> ` + generations + `; }` +
		` && ` + write + ` > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + shellQuote(final) + `; fi; } 9>>` + lock +
		` || { command -p rm -f ` + tmp + ` ` + generationsTmp + `; echo '` + failMsg + `' >&2; ` + failStmt + `; }; }`
}
