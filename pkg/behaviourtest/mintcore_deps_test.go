package behaviourtest

import (
	"os/exec"
	"strings"
	"testing"
)

const mintcorePrefix = "github.com/fullsend-ai/fullsend/internal/mintcore"

// TestBehaviourtestDepsExcludeMintcore guards the public build graph of
// pkg/behaviourtest. internal/mintcore is a nested module resolved in this
// repository by a local replace that downstream modules do not inherit.
// Any import of it (including mintconsts) makes go build fail for external
// consumers with "unknown revision internal/mintcore/v0.0.0".
func TestBehaviourtestDepsExcludeMintcore(t *testing.T) {
	t.Parallel()
	for _, tags := range []string{"", "behaviour"} {
		name := "untagged"
		if tags != "" {
			name = "tags=" + tags
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := []string{"list", "-deps"}
			if tags != "" {
				args = append(args, "-tags", tags)
			}
			args = append(args, "github.com/fullsend-ai/fullsend/pkg/behaviourtest")
			out, err := exec.Command("go", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, out)
			}
			var hits []string
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == mintcorePrefix || strings.HasPrefix(line, mintcorePrefix+"/") {
					hits = append(hits, line)
				}
			}
			if len(hits) > 0 {
				t.Fatalf("pkg/behaviourtest import graph includes nested mintcore module (replace is local-only):\n  %s", strings.Join(hits, "\n  "))
			}
		})
	}
}
