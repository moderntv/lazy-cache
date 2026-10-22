package lazy

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

type entryTest struct {
	// loaded data
	loadedValue *string
	err         error
	// entry data
	expectedTTL        time.Duration
	expectedValue      *string
	expectedNextReload int64 // timestamp of next reload in milliseconds
	expectedAccessed   bool
}

var entryTestTimeouts = Timeouts{
	TTL:            8 * time.Second,
	NotFoundTTL:    5 * time.Second,
	ErrorTTL:       1 * time.Second,
	ReloadInterval: 3 * time.Second,
	Randomizer:     0,
}

// Test first entry set which fails - its reload is not spread (successful first
// set is tested by TestEntrySetFirstTimeSpreadsReload)
func TestEntrySetFirstTime(t *testing.T) {
	var nowMillis int64 = 1700000000

	tests := map[string]entryTest{
		"generic_error": {
			loadedValue:        nil,
			err:                errors.New("other error"),
			expectedTTL:        entryTestTimeouts.ErrorTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"not_found": {
			loadedValue:        nil,
			err:                ErrNotFound,
			expectedTTL:        entryTestTimeouts.NotFoundTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"not_found_extended": {
			loadedValue:        nil,
			err:                fmt.Errorf("custom not found: %w", ErrNotFound),
			expectedTTL:        entryTestTimeouts.NotFoundTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := newCachedEntry[int, string](0)

			ttl := e.set(tc.loadedValue, tc.err, nowMillis, &entryTestTimeouts, true)
			assert.Equal(t, tc.expectedTTL, ttl)
			assert.Equal(t, tc.expectedValue, e.value.Load())
			assert.Equal(t, tc.expectedNextReload, e.nextReload.Load())
			assert.Equal(t, tc.expectedAccessed, e.accessed.Load())

		})
	}
}

// First successful set plans the reload after Randomizer part of
// ReloadInterval plus utils.RandomResidualDuration: uniformly up to
// ReloadInterval, the remaining Randomizer part of reloads less and less
// likely up to (1 + 2 * Randomizer) * ReloadInterval
func TestEntrySetFirstTimeSpreadsReload(t *testing.T) {
	var nowMillis int64 = 1700000000
	reloadInterval := entryTestTimeouts.ReloadInterval.Milliseconds()

	// ReloadInterval is 3s, reload is expected in
	// [nowMillis + minReloadIn, nowMillis + maxReloadIn)
	tests := map[string]struct {
		randomizer  float64
		minReloadIn int64
		maxReloadIn int64
	}{
		"without_randomizer": {randomizer: 0, minReloadIn: 0, maxReloadIn: 3000},
		"randomizer":         {randomizer: 0.2, minReloadIn: 600, maxReloadIn: 4200},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			timeouts := entryTestTimeouts
			timeouts.Randomizer = tc.randomizer

			tries := 100000
			delta := float64(tries) / 100

			outOfRange := 0
			uniformHalves := [2]int{}
			afterReloadInterval := 0

			for i := 0; i < tries; i++ {
				e := newCachedEntry[int, string](0)
				_ = e.set(test_utils.StringPointer("value1"), nil, nowMillis, &timeouts, true)

				reloadIn := e.nextReload.Load() - nowMillis
				switch {
				case reloadIn < tc.minReloadIn || reloadIn >= tc.maxReloadIn:
					outOfRange++
				case reloadIn >= reloadInterval:
					afterReloadInterval++
				default:
					uniformHalves[(reloadIn-tc.minReloadIn)*2/(reloadInterval-tc.minReloadIn)]++
				}
			}

			t.Logf("nextReload in halves of [now + %d, now + ReloadInterval): %v", tc.minReloadIn, uniformHalves)
			t.Logf("nextReload after now + ReloadInterval: %d", afterReloadInterval)

			assert.Zero(t, outOfRange, "nextReload out of [now + %d, now + %d)", tc.minReloadIn, tc.maxReloadIn)
			for i, count := range uniformHalves {
				assert.InDelta(t, float64(tries)*(1-tc.randomizer)/2, count, delta,
					"nextReload in half %d of [now + %d, now + ReloadInterval)", i, tc.minReloadIn)
			}
			assert.InDelta(t, float64(tries)*tc.randomizer, afterReloadInterval, delta,
				"nextReload after now + ReloadInterval")
		})
	}
}

// Spreading of the first reload does not affect TTL which ends after the
// spread reload nor the loaded value
func TestEntrySetFirstTimeSpreadKeepsTTL(t *testing.T) {
	var nowMillis int64 = 1700000000

	e := newCachedEntry[int, string](0)
	e.accessed.Store(true)

	ttl := e.set(test_utils.StringPointer("value1"), nil, nowMillis, &entryTestTimeouts, true)
	assert.Equal(t, entryTestTimeouts.TTL, ttl, "incorrect TTL")
	assert.Equal(t, test_utils.StringPointer("value1"), e.value.Load(), "incorrect value")
	assert.False(t, e.accessed.Load(), "incorrect accessed")
}

// TTL which would end before the spread first reload is extended to it, so the
// entry is not evicted before the reload
func TestEntrySetFirstTimeSpreadExtendsTTL(t *testing.T) {
	var nowMillis int64 = 1700000000
	// TTL 3s +/- 50%, the spread reload in [1.5s, 6s)
	timeouts := entryTestTimeouts
	timeouts.TTL = 3 * time.Second
	timeouts.Randomizer = 0.5

	tries := 10000

	beforeReload := 0
	outOfRange := 0
	extended := 0

	for i := 0; i < tries; i++ {
		e := newCachedEntry[int, string](0)
		ttl := e.set(test_utils.StringPointer("value1"), nil, nowMillis, &timeouts, true)

		reloadIn := time.Duration(e.nextReload.Load()-nowMillis) * time.Millisecond
		if ttl < reloadIn {
			beforeReload++
		}
		if ttl < 1500*time.Millisecond || ttl > max(4500*time.Millisecond, reloadIn+time.Millisecond) {
			outOfRange++
		}
		if ttl > 4500*time.Millisecond {
			extended++
		}
	}

	t.Logf("TTL extended beyond TTL + 50%%: %d", extended)

	assert.Zero(t, beforeReload, "TTL ends before the spread reload")
	assert.Zero(t, outOfRange, "TTL out of [TTL - 50%, max(TTL + 50%, spread reload)]")
	assert.Positive(t, extended, "no TTL extended beyond TTL + 50%")
}

// Only the first reload is spread, reloads after it are planned after
// ReloadInterval randomized by Randomizer
func TestEntryReloadAfterSpreadFirstSet(t *testing.T) {
	var nowMillis int64 = 1700000000
	randomizedTimeouts := entryTestTimeouts
	randomizedTimeouts.Randomizer = 0.2
	// ReloadInterval 3s +/- 20%
	var minNextReload int64 = nowMillis + 2400
	var maxNextReload int64 = nowMillis + 3600
	var referenceNextReload int64 = nowMillis + 3000

	tries := 10000
	treshhold := 4000

	outOfRange := 0
	lessThanReference := 0
	greaterThanReference := 0

	for i := 0; i < tries; i++ {
		// entry was first loaded 500ms ago
		e := newCachedEntry[int, string](0)
		_ = e.set(test_utils.StringPointer("value0"), nil, nowMillis-500, &randomizedTimeouts, true)

		_ = e.set(test_utils.StringPointer("value1"), nil, nowMillis, &randomizedTimeouts, false)

		nextReload := e.nextReload.Load()
		if nextReload < minNextReload || nextReload > maxNextReload {
			outOfRange++
		}
		if nextReload < referenceNextReload {
			lessThanReference++
		} else if nextReload > referenceNextReload {
			greaterThanReference++
		}
	}

	assert.Zero(t, outOfRange, "nextReload out of ReloadInterval +/- Randomizer")
	assert.Greater(t, lessThanReference, treshhold)
	assert.Greater(t, greaterThanReference, treshhold)
}

// With DisableSpreadInitialReload the first successful set plans the reload
// after ReloadInterval like any other set
func TestEntrySetFirstTimeSpreadDisabled(t *testing.T) {
	var nowMillis int64 = 1700000000
	timeouts := entryTestTimeouts
	timeouts.DisableSpreadInitialReload = true

	e := newCachedEntry[int, string](0)

	ttl := e.set(test_utils.StringPointer("value1"), nil, nowMillis, &timeouts, true)
	assert.Equal(t, timeouts.TTL, ttl, "incorrect TTL")
	assert.Equal(t, test_utils.StringPointer("value1"), e.value.Load(), "incorrect value")
	assert.Equal(t, nowMillis+timeouts.ReloadInterval.Milliseconds(), e.nextReload.Load(), "incorrect next reload")
}

func TestEntryReloadAfterError(t *testing.T) {
	var nowMillis int64 = 1700000000

	tests := map[string]entryTest{
		"generic_error": {
			loadedValue:        nil,
			err:                errors.New("other error"),
			expectedTTL:        -1,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"not_found": {
			loadedValue:        nil,
			err:                ErrNotFound,
			expectedTTL:        entryTestTimeouts.NotFoundTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"success": {
			loadedValue:        test_utils.StringPointer("value1"),
			err:                nil,
			expectedTTL:        entryTestTimeouts.TTL,
			expectedValue:      test_utils.StringPointer("value1"),
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// entry was first loaded 500ms ago
			e := newCachedEntry[int, string](0)
			e.set(test_utils.StringPointer("invalidValue"), errors.New("other error"), nowMillis-500, &entryTestTimeouts, true)
			e.accessed.Store(true)

			ttl := e.set(tc.loadedValue, tc.err, nowMillis, &entryTestTimeouts, false)
			assert.Equal(t, tc.expectedTTL, ttl, "incorrect TTL")
			assert.Equal(t, tc.expectedValue, e.value.Load(), "incorrect value")
			assert.Equal(t, tc.expectedNextReload, e.nextReload.Load(), "incorrect next reload")
			assert.Equal(t, tc.expectedAccessed, e.accessed.Load(), "incorrect accessed")

		})
	}
}

func TestEntryReloadAfterNotFound(t *testing.T) {
	var nowMillis int64 = 1700000000

	tests := map[string]entryTest{
		"generic_error": {
			loadedValue:        nil,
			err:                errors.New("other error"),
			expectedTTL:        -1,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"not_found": {
			loadedValue:        nil,
			err:                ErrNotFound,
			expectedTTL:        entryTestTimeouts.NotFoundTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"success": {
			loadedValue:        test_utils.StringPointer("value1"),
			err:                nil,
			expectedTTL:        entryTestTimeouts.TTL,
			expectedValue:      test_utils.StringPointer("value1"),
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// entry was first loaded 500ms ago
			e := newCachedEntry[int, string](0)
			e.set(test_utils.StringPointer("invalidValue"), ErrNotFound, nowMillis-500, &entryTestTimeouts, true)
			e.accessed.Store(true)

			ttl := e.set(tc.loadedValue, tc.err, nowMillis, &entryTestTimeouts, false)
			assert.Equal(t, tc.expectedTTL, ttl, "incorrect TTL")
			assert.Equal(t, tc.expectedValue, e.value.Load(), "incorrect value")
			assert.Equal(t, tc.expectedNextReload, e.nextReload.Load(), "incorrect next reload")
			assert.Equal(t, tc.expectedAccessed, e.accessed.Load(), "incorrect accessed")

		})
	}
}

func TestEntryReloadAfterSuccess(t *testing.T) {
	var nowMillis int64 = 1700000000

	tests := map[string]entryTest{
		"generic_error": {
			loadedValue:        nil,
			err:                errors.New("other error"),
			expectedTTL:        -1,
			expectedValue:      test_utils.StringPointer("value0"),
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"not_found": {
			loadedValue:        nil,
			err:                ErrNotFound,
			expectedTTL:        entryTestTimeouts.NotFoundTTL,
			expectedValue:      nil,
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
		"success": {
			loadedValue:        test_utils.StringPointer("value1"),
			err:                nil,
			expectedTTL:        entryTestTimeouts.TTL,
			expectedValue:      test_utils.StringPointer("value1"),
			expectedNextReload: nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds(),
			expectedAccessed:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// entry was first loaded 500ms ago
			e := newCachedEntry[int, string](0)
			e.set(test_utils.StringPointer("value0"), nil, nowMillis-500, &entryTestTimeouts, true)
			e.accessed.Store(true)

			ttl := e.set(tc.loadedValue, tc.err, nowMillis, &entryTestTimeouts, false)
			assert.Equal(t, tc.expectedTTL, ttl, "incorrect TTL")
			assert.Equal(t, tc.expectedValue, e.value.Load(), "incorrect value")
			assert.Equal(t, tc.expectedNextReload, e.nextReload.Load(), "incorrect next reload")
			assert.Equal(t, tc.expectedAccessed, e.accessed.Load(), "incorrect accessed")

		})
	}
}

func TestEntryRandomizedTTL(t *testing.T) {
	var nowMillis int64 = 1700000000
	randomizedTimeouts := entryTestTimeouts
	randomizedTimeouts.Randomizer = 0.2

	tries := 10000
	treshhold := 4000

	lessThanReference := 0
	greaterThanReference := 0

	e := newCachedEntry[int, string](0)

	for i := 0; i < tries; i++ {
		ttl := e.set(test_utils.StringPointer("value0"), nil, nowMillis, &randomizedTimeouts, false)

		if ttl < entryTestTimeouts.TTL {
			lessThanReference++
		} else if ttl > entryTestTimeouts.TTL {
			greaterThanReference++
		}
	}

	t.Logf("TTL less than reference duration: %d", lessThanReference)
	t.Logf("TTL greater than reference duration: %d", greaterThanReference)

	assert.Greater(t, lessThanReference, treshhold)
	assert.Greater(t, greaterThanReference, treshhold)
}

func TestEntryRandomizedNextReload(t *testing.T) {
	var nowMillis int64 = 1700000000
	randomizedTimeouts := entryTestTimeouts
	randomizedTimeouts.Randomizer = 0.2

	tries := 10000
	treshhold := 4000

	lessThanReference := 0
	greaterThanReference := 0

	referenceNextReload := nowMillis + entryTestTimeouts.ReloadInterval.Milliseconds()

	e := newCachedEntry[int, string](0)

	for i := 0; i < tries; i++ {
		_ = e.set(test_utils.StringPointer("value0"), nil, nowMillis, &randomizedTimeouts, false)

		nextReload := e.nextReload.Load()
		if nextReload < referenceNextReload {
			lessThanReference++
		} else if nextReload > referenceNextReload {
			greaterThanReference++
		}
	}

	t.Logf("nextReload less than reference ReloadInterval: %d", lessThanReference)
	t.Logf("nextReload greater reference ReloadInterval: %d", greaterThanReference)

	assert.Greater(t, lessThanReference, treshhold)
	assert.Greater(t, greaterThanReference, treshhold)
}

func TestEntryMutex(t *testing.T) {
	var nowMillis int64 = 1700000000

	routines := 100
	iterations := 10000

	wg := sync.WaitGroup{}

	e := newCachedEntry[int, string](0)
	e.mu.Lock()

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func() {
			for j := 0; j < iterations; j++ {
				_ = e.set(test_utils.StringPointer("value0"), nil, nowMillis, &entryTestTimeouts, false)

				_ = e.get()

				_ = e.set(test_utils.StringPointer("value0"), nil, nowMillis, &entryTestTimeouts, true)
				_ = e.set(test_utils.StringPointer("value0"), nil, nowMillis, &entryTestTimeouts, true)

				_ = e.get()
				_ = e.get()
				_ = e.get()
			}
			wg.Done()
		}()
	}

	wg.Wait()

	e.mu.Unlock()
}

func BenchmarkAtomicRead(b *testing.B) {
	a := atomic.Bool{}

	for i := 0; i < b.N; i++ {
		_ = a.Load()
	}
}

func BenchmarkAtomicWrite(b *testing.B) {
	a := atomic.Bool{}

	for i := 0; i < b.N; i++ {
		a.Store(true)
	}
}

func BenchmarkEntryGet(b *testing.B) {
	e := newCachedEntry[int, string](0)
	e.set(test_utils.StringPointer("invalidValue"), nil, 1500000000, &entryTestTimeouts, false)

	for i := 0; i < b.N; i++ {
		_ = e.get()
	}
}

func BenchmarkEntrySetWithoutRandomizer(b *testing.B) {
	e := newCachedEntry[int, string](0)
	value := test_utils.StringPointer("invalidValue")

	for i := 0; i < b.N; i++ {
		e.set(value, nil, 1500000000, &entryTestTimeouts, false)
	}
}

func BenchmarkEntrySetWithRandomizer(b *testing.B) {
	randomizedTimeouts := entryTestTimeouts
	randomizedTimeouts.Randomizer = 0.2

	e := newCachedEntry[int, string](0)
	value := test_utils.StringPointer("invalidValue")

	for i := 0; i < b.N; i++ {
		e.set(value, nil, 1500000000, &randomizedTimeouts, false)
	}
}
