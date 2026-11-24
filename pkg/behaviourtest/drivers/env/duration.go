package env

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Duration reads a Go duration (for example "5s" or "1m30s") from the
// named environment variable. It returns fallback when the variable is
// unset or empty. A value that does not parse, or is not positive, is an
// error, so a typo fails the suite instead of silently keeping the default.
//
// The waits it tunes are sized for github.com, where events and runs lag.
// Against a local forge that delivers events at once they can be shortened.
func Duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %q", name, raw)
	}
	return d, nil
}

// MustDuration is Duration for package-level settings read once at start-up.
// It panics on an invalid value, naming the variable.
func MustDuration(name string, fallback time.Duration) time.Duration {
	d, err := Duration(name, fallback)
	if err != nil {
		panic(err)
	}
	return d
}
