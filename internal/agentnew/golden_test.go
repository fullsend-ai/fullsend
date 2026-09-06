package agentnew

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// TestGolden pins the exact bytes of a generated tree. A template edit that
// changes generated output shows up as a reviewable diff rather than passing
// silently — which matters because these files land in users' repositories
// and nothing regenerates them afterwards.
//
// Regenerate with: go test ./internal/agentnew/ -run TestGolden -update
func TestGolden(t *testing.T) {
	// Two trees, not one per role. The roles differ only in table data —
	// image, providers, profiles — which TestRoleTableMatchesMint and its
	// siblings assert directly and more precisely than a byte diff does.
	// validation-loop is kept because it is the only variant that exercises
	// an actual conditional in the generator: `if opts.ValidationLoop` in
	// both buildHarness and sharedAssets.
	cases := []struct {
		name string
		opts func() Options
	}{
		{"triage", func() Options { return testOptions("lint-docs", "triage") }},
		{"validation-loop", func() Options {
			o := testOptions("lint-docs", "triage")
			o.ValidationLoop = true
			return o
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Render(tc.opts())
			if err != nil {
				t.Fatal(err)
			}
			got := renderTree(files)
			goldenPath := filepath.Join("testdata", "golden", tc.name+".txt")

			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading golden file (run with -update to create it): %v", err)
			}
			if got != string(want) {
				t.Errorf("generated tree differs from %s\n"+
					"If the change is intended, re-run with -update and review the diff.\n\n%s",
					goldenPath, firstDifference(string(want), got))
			}
		})
	}
}

// renderTree renders a file set as a stable, diffable document. The harness
// digest is the one value that legitimately changes on a fleet repin, so it
// is left in: a repin SHOULD show up as a golden diff.
func renderTree(files []File) string {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var b strings.Builder
	for _, f := range sorted {
		shared := ""
		if f.Shared {
			shared = " (shared, written only when absent)"
		}
		b.WriteString("=== " + f.Path + " mode=" + octal(f.Mode) + shared + "\n")
		// Shared assets are byte-identical copies of files that already live
		// in the repo; their contents are asserted elsewhere, and inlining
		// them here would bury the generated files in noise.
		if f.Shared {
			b.WriteString("<copied verbatim from the embedded scaffold>\n")
			continue
		}
		b.Write(f.Data)
		if !bytes.HasSuffix(f.Data, []byte("\n")) {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func octal(mode uint32) string {
	const digits = "01234567"
	if mode == 0 {
		return "0"
	}
	var out []byte
	for mode > 0 {
		out = append([]byte{digits[mode%8]}, out...)
		mode /= 8
	}
	return string(out)
}

// firstDifference reports the first differing line, which is far easier to
// read than a whole-tree diff when a template changes.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) && i < len(gotLines); i++ {
		if wantLines[i] != gotLines[i] {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n" +
				"  want: " + wantLines[i] + "\n" +
				"   got: " + gotLines[i] + "\n"
		}
	}
	return "want " + strconv.Itoa(len(wantLines)) + " lines, got " + strconv.Itoa(len(gotLines)) + "\n"
}
