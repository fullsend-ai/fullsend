package randutil

import (
	"errors"
	"slices"
	"testing"
)

func TestInt64N(t *testing.T) {
	for range 100 {
		if got := Int64N(7); got < 0 || got >= 7 {
			t.Fatalf("Int64N(7) = %d, want value in [0, 7)", got)
		}
	}
	assertPanics(t, func() { Int64N(0) })
}

func TestIntN(t *testing.T) {
	for range 100 {
		if got := IntN(7); got < 0 || got >= 7 {
			t.Fatalf("IntN(7) = %d, want value in [0, 7)", got)
		}
	}
	assertPanics(t, func() { IntN(0) })
}

func TestUint64(t *testing.T) {
	_ = Uint64()
}

func TestUint64Read(t *testing.T) {
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

func TestFloat64(t *testing.T) {
	for range 100 {
		if got := Float64(); got < 0 || got >= 1 {
			t.Fatalf("Float64() = %f, want value in [0, 1)", got)
		}
	}
}

func TestShuffle(t *testing.T) {
	values := []int{0, 1, 2, 3, 4, 5, 6, 7}
	Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
	if !slices.Equal(slices.Sorted(slices.Values(values)), []int{0, 1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("Shuffle changed the elements: %v", values)
	}
	Shuffle(1, func(i, j int) {})
	Shuffle(0, func(i, j int) {})
	assertPanics(t, func() { Shuffle(-1, func(i, j int) {}) })
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
