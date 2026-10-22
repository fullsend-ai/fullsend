package layers

import "time"

// fakeClock implements clock with channels that resolve immediately,
// eliminating wall-clock delays in poll/retry loops.
type fakeClock struct{}

func (fakeClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}
