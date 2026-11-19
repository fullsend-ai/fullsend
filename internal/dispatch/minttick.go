package dispatch

import (
	"fmt"
	"strings"
	"time"
)

// Mint timer (ADR 0171) deploy settings shared by the Cloudflare and
// Cloud Function provisioners. The mint reads the interval from
// MintTickIntervalEnv (internal/mintcore/tick.go accepts 1m to 5m in whole
// seconds); a hosted scheduler can only fire on whole minutes, so a deploy
// accepts whole minutes only and sets the env var and the schedule
// together. The sweep's rotation needs the two to match.
const (
	// MintTickIntervalEnv is the mint setting that holds the interval.
	MintTickIntervalEnv = "FULLSEND_MINT_TICK_INTERVAL"

	// DefaultMintTickInterval is the interval when none is given.
	DefaultMintTickInterval = 5 * time.Minute
)

// ParseMintTickInterval parses a deploy's tick interval: a Go duration of
// whole minutes from 1m to 5m. Empty means DefaultMintTickInterval.
func ParseMintTickInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMintTickInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute || d > 5*time.Minute || d%time.Minute != 0 {
		return 0, fmt.Errorf("invalid mint tick interval %q: must be 1m, 2m, 3m, 4m or 5m", raw)
	}
	return d, nil
}

// MintTickIntervalValue formats d as the value of MintTickIntervalEnv,
// for example "5m".
func MintTickIntervalValue(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

// MintTickCron returns the cron schedule that fires every d, where d is a
// whole number of minutes from 1 to 5: "* * * * *" or "*/N * * * *". 60 is
// a multiple of each N, so the gap is N minutes across the hour too.
func MintTickCron(d time.Duration) string {
	n := int(d / time.Minute)
	if n <= 1 {
		return "* * * * *"
	}
	return fmt.Sprintf("*/%d * * * *", n)
}
