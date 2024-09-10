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

// Test entry set without error
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
			e := &cachedEntry[string]{}

			ttl := e.set(tc.loadedValue, tc.err, nowMillis, &entryTestTimeouts, true)
			assert.Equal(t, tc.expectedTTL, ttl)
			assert.Equal(t, tc.expectedValue, e.value.Load())
			assert.Equal(t, tc.expectedNextReload, e.nextReload.Load())
			assert.Equal(t, tc.expectedAccessed, e.accessed.Load())

		})
	}
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
			e := &cachedEntry[string]{}
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
			e := &cachedEntry[string]{}
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
			e := &cachedEntry[string]{}
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

	e := &cachedEntry[string]{}

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

	e := &cachedEntry[string]{}

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

	e := &cachedEntry[string]{}
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
	e := &cachedEntry[string]{}
	e.set(test_utils.StringPointer("invalidValue"), nil, 1500000000, &entryTestTimeouts, false)

	for i := 0; i < b.N; i++ {
		_ = e.get()
	}
}

func BenchmarkEntrySetWithoutRandomizer(b *testing.B) {
	e := &cachedEntry[string]{}
	value := test_utils.StringPointer("invalidValue")

	for i := 0; i < b.N; i++ {
		e.set(value, nil, 1500000000, &entryTestTimeouts, false)
	}
}

func BenchmarkEntrySetWithRandomizer(b *testing.B) {
	randomizedTimeouts := entryTestTimeouts
	randomizedTimeouts.Randomizer = 0.2

	e := &cachedEntry[string]{}
	value := test_utils.StringPointer("invalidValue")

	for i := 0; i < b.N; i++ {
		e.set(value, nil, 1500000000, &randomizedTimeouts, false)
	}
}
