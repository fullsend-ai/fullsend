// Package randutil provides cryptographically secure random number utilities.
package randutil

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"math/big"
)

// Int64N returns a cryptographically random integer in [0, n).
// It panics when n is not positive or the system random source fails.
func Int64N(n int64) int64 {
	if n <= 0 {
		panic("randutil: Int64N called with non-positive bound")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		panic(err)
	}
	return v.Int64()
}

// IntN returns a cryptographically random integer in [0, n).
// It panics when n is not positive or the system random source fails.
func IntN(n int) int {
	if n <= 0 {
		panic("randutil: IntN called with non-positive bound")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// Uint64 returns a cryptographically random uint64.
// It panics if the system random source fails.
func Uint64() uint64 {
	return randomUint64(rand.Read)
}

func randomUint64(read func([]byte) (int, error)) uint64 {
	var b [8]byte
	n, err := read(b[:])
	if err != nil {
		panic(err)
	}
	if n != len(b) {
		panic("randutil: short read from system random source")
	}
	return binary.BigEndian.Uint64(b[:])
}

// Float64 returns a cryptographically random number in [0, 1).
func Float64() float64 {
	return math.Ldexp(float64(Uint64()>>11), -53)
}

// Shuffle pseudo-randomizes the order of n elements using Fisher-Yates.
// It panics if n is negative or the system random source fails.
func Shuffle(n int, swap func(i, j int)) {
	if n < 0 {
		panic("randutil: Shuffle called with negative length")
	}
	for i := n - 1; i > 0; i-- {
		j := IntN(i + 1)
		swap(i, j)
	}
}
