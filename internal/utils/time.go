package utils

import (
	"math"
	"math/rand"
	"time"
)

type source interface {
	Float64() float64
	Int63n(n int64) int64
}

// globalSource uses the top-level functions of math/rand, which are safe for
// concurrent use (unlike rand.New)
type globalSource struct{}

func (globalSource) Float64() float64 {
	return rand.Float64()
}

func (globalSource) Int63n(n int64) int64 {
	return rand.Int63n(n)
}

// random is replaced by a seeded source in tests
var random source = globalSource{}

func RandomizeDuration(d time.Duration, randomizer float64) time.Duration {
	if randomizer == 0 {
		return d
	}

	add := time.Duration(float64(d) * random.Float64() * randomizer)
	if random.Int63n(2) == 0 {
		add = -add
	}

	return d + add
}

// RandomResidualDuration returns the time from a random moment to the next
// event of a long running sequence of events separated by intervals
// RandomizeDuration(d, randomizer). Sequences started after this time have
// events at a constant rate from the start, without waves.
// The result is from [0, (1 + randomizer) * d), uniform from [0, d) with zero
// randomizer. It returns 0 when d is not positive.
func RandomResidualDuration(d time.Duration, randomizer float64) time.Duration {
	if d <= 0 {
		return 0
	}

	shortest := float64(d) * (1 - randomizer)
	longest := float64(d) * (1 + randomizer)
	// the random moment falls into an interval with probability proportional
	// to its length, then uniformly into it
	interval := math.Sqrt(shortest*shortest + random.Float64()*(longest*longest-shortest*shortest))

	return time.Duration(random.Float64() * interval)
}
