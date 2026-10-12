package utils

import (
	"math/rand"
	"time"
)

func RandomizeDuration(d time.Duration, randomizer float64) time.Duration {
	if randomizer == 0 {
		return d
	}

	add := time.Duration(float64(d) * rand.Float64() * randomizer)
	if rand.Intn(2) == 0 {
		add = -add
	}

	return d + add
}

// RandomDurationUpTo returns a random duration from interval [0, d).
// It returns 0 when d is not positive.
func RandomDurationUpTo(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}

	return time.Duration(rand.Int63n(int64(d)))
}
