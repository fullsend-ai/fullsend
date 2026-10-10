package runtime

import "strconv"

// seedLockWaitSeconds bounds how long a credential seed waits for the
// seed lock. The lock is held only for the few commands that write one
// small file, so a wait this long means the holder is stuck; the seed then
// fails closed rather than writing without the ordering guarantee. It
// stays well inside one runner exec (openAIPlaceholderExecTimeout).
const seedLockWaitSeconds = 10

// seedLockSuffix and seedGenerationsSuffix name the two sidecar files a
// credential seed keeps next to its credential file.
const (
	seedLockSuffix        = ".lock"
	seedGenerationsSuffix = ".generations"
)

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
// unquoted paths. tmp is the temp file as a shell word, already quoted (it
// may end in an unquoted `.$$`). write is the command whose stdout becomes
// the file's content. failMsg and failStmt report a failed write after the
// temp file is removed.
func orderedSeedWrite(envVar, dir, final, tmp, write, failMsg, failStmt string) string {
	lock := shellQuote(final + seedLockSuffix)
	generations := shellQuote(final + seedGenerationsSuffix)
	v := `"$` + envVar + `"`
	last := `"$(command -p tail -n 1 ` + generations + ` 2>/dev/null)"`
	return `{ command -p mkdir -p ` + shellQuote(dir) +
		` && { command -p flock -w ` + strconv.Itoa(seedLockWaitSeconds) + ` 9` +
		` && if test ` + last + ` != ` + v + ` && command -p grep -qxF -e ` + v + ` ` + generations + ` 2>/dev/null;` +
		` then echo 'fullsend: ` + envVar + ` is an older generation than the credential file holds; leaving the file as it is' >&2;` +
		` else { test ` + last + ` = ` + v + ` || printf '%s\n' ` + v + ` >> ` + generations + `; }` +
		` && ` + write + ` > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + shellQuote(final) + `; fi; } 9>>` + lock +
		` || { command -p rm -f ` + tmp + `; echo '` + failMsg + `' >&2; ` + failStmt + `; }; }`
}
