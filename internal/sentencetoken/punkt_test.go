package sentencetoken

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type payload struct {
	Name          string `yaml:"name"`
	CommitMessage string `yaml:"commit_message"`
}

func loadPayloads(t *testing.T) []payload {
	t.Helper()
	dirs := []string{
		"../../experiments/prompt-injection-defense/attacks",
		"../../experiments/guardrails-eval/payloads",
	}
	var payloads []payload
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("reading %s: %v", e.Name(), err)
			}
			var p payload
			if err := yaml.Unmarshal(data, &p); err != nil {
				t.Fatalf("parsing %s: %v", e.Name(), err)
			}
			payloads = append(payloads, p)
		}
	}
	return payloads
}

func TestSplitSentences_Basic(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty string",
			input: "",
			want:  []string{""},
		},
		{
			name:  "single sentence",
			input: "Fix the bug in handler.",
			want:  []string{"Fix the bug in handler."},
		},
		{
			name:  "two sentences",
			input: "Fix the bug. Add tests.",
			want:  []string{"Fix the bug.", "Add tests."},
		},
		{
			name:  "abbreviation Dr.",
			input: "Dr. Smith went to the store. He bought milk.",
			want:  []string{"Dr. Smith went to the store.", "He bought milk."},
		},
		{
			name:  "abbreviation Mr.",
			input: "Mr. Jones arrived. He sat down.",
			want:  []string{"Mr. Jones arrived.", "He sat down."},
		},
		{
			name:  "ellipsis",
			input: "Wait... what happened? Nothing.",
			want:  []string{"Wait... what happened?", "Nothing."},
		},
		{
			name:  "question and exclamation",
			input: "Is this right? Yes! Do it.",
			want:  []string{"Is this right?", "Yes!", "Do it."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitSentences(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("SplitSentences(%q) = %d sentences, want %d\ngot:  %v\nwant: %v",
					tt.input, len(got), len(tt.want), got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sentence[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitSentences_Payloads(t *testing.T) {
	payloads := loadPayloads(t)
	if len(payloads) == 0 {
		t.Skip("no payload files found (experiments/ directory may not exist)")
	}

	for _, p := range payloads {
		t.Run(p.Name, func(t *testing.T) {
			text := strings.TrimSpace(p.CommitMessage)
			sents := SplitSentences(text)
			if len(sents) == 0 {
				t.Error("SplitSentences returned empty slice")
			}
			for i, s := range sents {
				if strings.TrimSpace(s) == "" {
					t.Errorf("sentence[%d] is empty/whitespace", i)
				}
			}
			t.Logf("%s: %d sentences", p.Name, len(sents))
			for i, s := range sents {
				t.Logf("  [%d] %s", i, s)
			}
		})
	}
}

func TestSplitSentences_NoModification(t *testing.T) {
	input := "Fix bug in v1.2.3. Added tests."
	sents := SplitSentences(input)
	joined := strings.Join(sents, " ")
	if !strings.Contains(joined, "v1.2.3") {
		t.Errorf("version number was modified: got %v", sents)
	}
}
