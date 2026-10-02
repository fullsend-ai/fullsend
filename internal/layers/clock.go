package layers

import "time"

// clock abstracts time.Now and time.After for testability. Production
// code uses realClock (the default); tests inject a fake that owns both
// current time and sleep so poll/retry loops complete without
// wall-clock delays and can assert deadline-bounded backoff.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}
