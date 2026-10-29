package randutil

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"math/big"
)

// RandomInt64N returns a cryptographically random integer in [0, n).
// It panics when n is not positive or the system random source fails.
func RandomInt64N(n int64) int64 {
	if n <= 0 {
		panic("randutil: RandomInt64N called with non-positive bound")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		panic(err)
	}
	return v.Int64()
}

// RandomIntN returns a cryptographically random integer in [0, n).
// It panics when n is not positive or the system random source fails.
func RandomIntN(n int) int {
	if n <= 0 {
		panic("randutil: RandomIntN called with non-positive bound")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// RandomUint64 returns a cryptographically random uint64.
// It panics if the system random source fails.
func RandomUint64() uint64 {
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

// RandomFloat64 returns a cryptographically random number in [0, 1).
func RandomFloat64() float64 {
	return math.Ldexp(float64(RandomUint64()>>11), -53)
}

// RandomShuffle pseudo-randomizes the order of n elements using Fisher-Yates.
// It panics if n is negative or the system random source fails.
func RandomShuffle(n int, swap func(i, j int)) {
	if n < 0 {
		panic("randutil: RandomShuffle called with negative length")
	}
	for i := n - 1; i > 0; i-- {
		j := RandomIntN(i + 1)
		swap(i, j)
	}
}
