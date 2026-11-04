package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/security"
)

func TestProgressPatternRedactionBeforeUnicodeComposition(t *testing.T) {
	// Repetitive synthetic fixtures exercise patterns; never authenticate.
	tokens := []string{strings.Join([]string{"gh", "p_"}, "") + strings.Repeat("a", 35) + "e", strings.Join([]string{"AK", "IA"}, "") + strings.Repeat("A", 15) + "E"}
	for index, token := range tokens {
		for _, withCallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("pattern%d/callback=%t", index, withCallback), func(t *testing.T) {
				t.Setenv("GITHUB_ACTIONS", "true")
				var out bytes.Buffer
				r := newTestRenderer(&out)
				if withCallback {
					r.RedactProgress = func(s string) string { return strings.ReplaceAll(s, "caller-owned-placeholder", "[REDACTED]") }
				}
				stderr := captureStderr(t, func() {
					r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: " \t" + token + "\u0301 \n"})
					r.Handle(StatusProgressEvent{Kind: "notice", ID: "one", Label: "Notice", Detail: " \t" + token + "\u0301 \n"})
				})
				check := func(where, s string) {
					t.Helper()
					if strings.Contains(s, token[:len(token)-1]) {
						t.Fatalf("pattern credential prefix exposed in %s", where)
					}
				}
				check("console", out.String())
				check("CI", stderr)
				for _, row := range r.progress.taskProgressSeen {
					check("task cache", fmt.Sprintf("%+v", row.event))
				}
				for _, row := range r.progress.statusProgressSeen {
					check("status cache", fmt.Sprintf("%+v", row))
				}
			})
		}
	}
}

func TestProgressRegisteredUnicodeLiteralWithoutCallback(t *testing.T) {
	const secret = "opaque-\u202eruntime-progress-for-test"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: secret})
	if strings.Contains(out.String(), "opaque-") || !strings.Contains(out.String(), "***") {
		t.Fatal("registered literal escaped normalization")
	}
}

func TestProgressPatternPaddingKeepsExactRegisteredMatches(t *testing.T) {
	const secret = "\t opaque-padded-runtime-progress-only \n"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic padded literal")
	}
	if got := scanProgressSecrets(secret); strings.Contains(got, "opaque-padded") {
		t.Fatal("padding optimization lost exact registered match")
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "padded", State: TaskRunning, Label: secret})
	if strings.Contains(out.String(), "opaque-padded") || !strings.Contains(out.String(), "***") {
		t.Fatal("nil-callback padded registered literal escaped")
	}
	const ordinary = " \tordinary status \n"
	if got := scanProgressSecrets(ordinary); got != ordinary {
		t.Fatal("nonsecret raw text changed before normalization")
	}
	if got := scanProgressSecrets(" \t \n"); got != " \t \n" {
		t.Fatal("empty scanner candidate changed raw text")
	}
}

func TestProgressFallbackRedactsNormalizedRuntimeSecret(t *testing.T) {
	// A registered credential containing whitespace variants (here a tab)
	// normalizes to different text than the registered literal. Without a
	// RedactProgress callback the fallback must still mask the normalized
	// literal, or the reconstructed credential is displayed and retained.
	const secret = "opaque-runtime-secret \twith-tab-for-test"
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	cleaned := NormalizeProgressText(secret)
	if cleaned == secret || !strings.Contains(cleaned, "with-tab-for-test") {
		t.Fatalf("setup: secret did not normalize differently: %q", cleaned)
	}
	if got := scanProgressSecrets("progress " + cleaned + " done"); strings.Contains(got, "opaque-runtime-secret") || !strings.Contains(got, "***") {
		t.Fatalf("normalized registered literal escaped the fallback: %q", got)
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "progress " + cleaned + " done"})
	if strings.Contains(out.String(), "opaque-runtime-secret") || !strings.Contains(out.String(), "***") {
		t.Fatalf("normalized registered literal escaped rendering: %q", out.String())
	}
}

// A registered credential whose normalized form is shorter than the
// registration floor must still be masked: the original literal passed the
// floor at registration, so every non-empty normalized derivative stays an
// authenticated secret. No RedactProgress callback is set, exercising the
// renderer fallback.
func TestProgressFallbackRedactsShortNormalizedRuntimeSecret(t *testing.T) {
	const secret = "zz\x1b[32m\x1b[0mtok" // raw length 14; normalizes to "zztok" (5)
	if !security.RegisterRuntimeSecret(secret) {
		t.Fatal("register synthetic literal")
	}
	cleaned := NormalizeProgressText(secret)
	if cleaned != "zztok" {
		t.Fatalf("setup: unexpected normalized form %q", cleaned)
	}
	if got := scanProgressSecrets("status " + cleaned + " done"); strings.Contains(got, "zztok") || !strings.Contains(got, "***") {
		t.Fatalf("short normalized registered literal escaped the fallback: %q", got)
	}
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.Handle(TaskProgressEvent{TaskID: "one", State: TaskRunning, Label: "status " + cleaned + " done"})
	if strings.Contains(out.String(), "zztok") || !strings.Contains(out.String(), "***") {
		t.Fatalf("short normalized registered literal escaped rendering: %q", out.String())
	}
}
