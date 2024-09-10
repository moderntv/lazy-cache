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

func BenchmarkAtomicWrite(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = RandomizeDuration(10*time.Second, 0.2)
	}
}
