package utils

import (
	"fmt"
	"math"
	"math/rand"
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

func TestRandomResidualDuration(t *testing.T) {
	tries := 10000

	for _, randomizer := range []float64{0, 0.1, 0.5, 1} {
		t.Run(fmt.Sprint(randomizer), func(t *testing.T) {
			longest := time.Duration(float64(10*time.Second) * (1 + randomizer))

			outOfRange := 0
			for i := 0; i < tries; i++ {
				d := RandomResidualDuration(10*time.Second, randomizer)
				if d < 0 || d >= longest {
					outOfRange++
				}
			}

			assert.Zero(t, outOfRange, "duration out of [0, %s)", longest)
		})
	}
}

func TestRandomResidualDurationNotPositive(t *testing.T) {
	assert.Zero(t, RandomResidualDuration(0, 0.5))
	assert.Zero(t, RandomResidualDuration(-time.Second, 0.5))
}

// seedRandom makes the random durations of the test reproducible
func seedRandom(t *testing.T) {
	random = rand.New(rand.NewSource(1))
	t.Cleanup(func() {
		random = globalSource{}
	})
}

const (
	rateTestEntries  = 100000
	rateTestInterval = time.Hour
	rateTestBucket   = rateTestInterval / 20
	rateTestHorizon  = 4 * rateTestInterval
	// about 7 standard deviations of the number of reloads in a bucket, so
	// the test does not depend on the seed
	rateTestTolerance = 0.1
)

// simulateReloads simulates rateTestEntries entries loaded at time 0, each
// reloaded first after firstReload and then after rateTestInterval randomized
// by randomizer. It returns the number of reloads before gap and in each
// rateTestBucket up to rateTestHorizon.
func simulateReloads(
	randomizer float64,
	gap time.Duration,
	firstReload func() time.Duration,
) (inGap int, buckets []int) {
	buckets = make([]int, rateTestHorizon/rateTestBucket)
	for range rateTestEntries {
		at := firstReload()
		for at < rateTestHorizon {
			if at < gap {
				inGap++
			}
			buckets[at/rateTestBucket]++
			at += RandomizeDuration(rateTestInterval, randomizer)
		}
	}

	return
}

// maxRateDeviation returns the largest relative deviation of a bucket after
// gap from the constant rate of rateTestEntries reloads per rateTestInterval
func maxRateDeviation(buckets []int, gap time.Duration) (deviation float64) {
	expected := float64(rateTestEntries) * float64(rateTestBucket) / float64(rateTestInterval)
	for _, count := range buckets[(gap+rateTestBucket-1)/rateTestBucket:] {
		deviation = max(deviation, math.Abs(float64(count)-expected)/expected)
	}

	return
}

// Entries loaded at once and reloaded first after the gap of randomizer part
// of the interval plus RandomResidualDuration are reloaded at a constant rate
// right after the gap
func TestRandomResidualDurationConstantReloadRate(t *testing.T) {
	for _, randomizer := range []float64{0, 0.1, 0.5, 1} {
		t.Run(fmt.Sprint(randomizer), func(t *testing.T) {
			seedRandom(t)
			gap := time.Duration(float64(rateTestInterval) * randomizer)

			inGap, buckets := simulateReloads(randomizer, gap, func() time.Duration {
				return gap + RandomResidualDuration(rateTestInterval, randomizer)
			})
			deviation := maxRateDeviation(buckets, gap)

			t.Logf("reloads in buckets: %v", buckets)

			assert.Zero(t, inGap, "reloads before the end of the gap")
			assert.LessOrEqual(t, deviation, rateTestTolerance, "deviation from the constant reload rate")
		})
	}
}

// The first reload uniform from a window as wide as the interval after the gap
// leads to waves of reloads
func TestUniformFirstReloadRateNotConstant(t *testing.T) {
	seedRandom(t)
	randomizer := 0.5
	gap := time.Duration(float64(rateTestInterval) * randomizer)

	_, buckets := simulateReloads(randomizer, gap, func() time.Duration {
		return gap + time.Duration(random.Int63n(int64(rateTestInterval)))
	})
	deviation := maxRateDeviation(buckets, gap)

	t.Logf("reloads in buckets: %v", buckets)

	assert.Greater(t, deviation, rateTestTolerance, "deviation from the constant reload rate")
}

func BenchmarkAtomicWrite(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = RandomizeDuration(10*time.Second, 0.2)
	}
}
