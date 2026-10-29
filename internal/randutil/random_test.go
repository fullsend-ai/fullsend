package randutil

import (
	"errors"
	"slices"
	"testing"
)

func TestRandomInt64N(t *testing.T) {
	for range 100 {
		if got := RandomInt64N(7); got < 0 || got >= 7 {
			t.Fatalf("RandomInt64N(7) = %d, want value in [0, 7)", got)
		}
	}
	assertPanics(t, func() { RandomInt64N(0) })
}

func TestRandomIntN(t *testing.T) {
	for range 100 {
		if got := RandomIntN(7); got < 0 || got >= 7 {
			t.Fatalf("RandomIntN(7) = %d, want value in [0, 7)", got)
		}
	}
	assertPanics(t, func() { RandomIntN(0) })
}

func TestRandomUint64(t *testing.T) {
	_ = RandomUint64()
}

func TestRandomUint64Read(t *testing.T) {
	got := randomUint64(func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	})
	if got != 0x0001020304050607 {
		t.Fatalf("randomUint64() = %#x, want %#x", got, uint64(0x0001020304050607))
	}
	assertPanics(t, func() {
		randomUint64(func([]byte) (int, error) { return 0, errors.New("read failed") })
	})
	assertPanics(t, func() {
		randomUint64(func([]byte) (int, error) { return 1, nil })
	})
}

func TestRandomFloat64(t *testing.T) {
	for range 100 {
		if got := RandomFloat64(); got < 0 || got >= 1 {
			t.Fatalf("RandomFloat64() = %f, want value in [0, 1)", got)
		}
	}
}

func TestRandomShuffle(t *testing.T) {
	values := []int{0, 1, 2, 3, 4, 5, 6, 7}
	RandomShuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
	if !slices.Equal(slices.Sorted(slices.Values(values)), []int{0, 1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("RandomShuffle changed the elements: %v", values)
	}
	RandomShuffle(1, func(i, j int) {})
	RandomShuffle(0, func(i, j int) {})
	assertPanics(t, func() { RandomShuffle(-1, func(i, j int) {}) })
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("function did not panic")
		}
	}()
	fn()
}
