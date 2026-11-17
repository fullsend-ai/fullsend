package dispatch

import (
	"testing"
	"time"
)

func TestParseMintTickInterval(t *testing.T) {
	valid := map[string]time.Duration{
		"":       DefaultMintTickInterval,
		"  ":     DefaultMintTickInterval,
		"1m":     time.Minute,
		"2m":     2 * time.Minute,
		"3m0s":   3 * time.Minute,
		"240s":   4 * time.Minute,
		"5m":     5 * time.Minute,
		" 5m\n":  5 * time.Minute,
		"0h5m0s": 5 * time.Minute,
	}
	for in, want := range valid {
		got, err := ParseMintTickInterval(in)
		if err != nil || got != want {
			t.Errorf("ParseMintTickInterval(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"30s", "90s", "6m", "0", "-1m", "5", "fast", "1h"} {
		if _, err := ParseMintTickInterval(in); err == nil {
			t.Errorf("ParseMintTickInterval(%q): want an error", in)
		}
	}
}

func TestMintTickCronAndValue(t *testing.T) {
	cases := map[time.Duration][2]string{
		time.Minute:     {"* * * * *", "1m"},
		2 * time.Minute: {"*/2 * * * *", "2m"},
		3 * time.Minute: {"*/3 * * * *", "3m"},
		4 * time.Minute: {"*/4 * * * *", "4m"},
		5 * time.Minute: {"*/5 * * * *", "5m"},
	}
	for d, want := range cases {
		if got := MintTickCron(d); got != want[0] {
			t.Errorf("MintTickCron(%v) = %q, want %q", d, got, want[0])
		}
		if got := MintTickIntervalValue(d); got != want[1] {
			t.Errorf("MintTickIntervalValue(%v) = %q, want %q", d, got, want[1])
		}
	}
}
