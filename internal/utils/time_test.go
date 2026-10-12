package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRandomDuration(t *testing.T) {
	tries := 1000
	treshhold := 900

	results := make(map[time.Duration]struct{}, tries)

	for i := 0; i < tries; i++ {
		d := RandomizeDuration(10*time.Second, 0.2)
		results[d] = struct{}{}
	}

	t.Log(len(results))
	assert.Greater(t, len(results), treshhold)
}

func TestRandomDurationUpTo(t *testing.T) {
	tries := 10000
	treshhold := 4000

	outOfRange := 0
	firstHalf := 0
	secondHalf := 0

	for i := 0; i < tries; i++ {
		d := RandomDurationUpTo(10 * time.Second)

		if d < 0 || d >= 10*time.Second {
			outOfRange++
		} else if d < 5*time.Second {
			firstHalf++
		} else {
			secondHalf++
		}
	}

	assert.Zero(t, outOfRange)
	assert.Greater(t, firstHalf, treshhold)
	assert.Greater(t, secondHalf, treshhold)
}

func TestRandomDurationUpToNotPositive(t *testing.T) {
	assert.Zero(t, RandomDurationUpTo(0))
	assert.Zero(t, RandomDurationUpTo(-time.Second))
}

func BenchmarkAtomicWrite(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = RandomizeDuration(10*time.Second, 0.2)
	}
}
