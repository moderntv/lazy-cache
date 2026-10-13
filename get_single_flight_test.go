package lazy

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

// TestGetMissingEntryConcurrentLoad checks that concurrent Get calls of an
// entry missing in cache load it only once - the routines wait for the load of
// the routine which stored the entry first instead of loading it again
func TestGetMissingEntryConcurrentLoad(t *testing.T) {
	t.Parallel()

	notFoundNotCached := timeoutsNoSpread(cacheTestTimeouts)
	notFoundNotCached.NotFoundTTL = 0

	tests := map[string]struct {
		loadErr  error // error of the load, the value is returned without it
		timeouts Timeouts
		cached   bool // the loaded entry is stored in cache
	}{
		"found": {
			// the first reload must not be due before the other routines get the entry
			timeouts: timeoutsNoSpread(cacheTestTimeouts),
			cached:   true,
		},
		// the entry loaded with zero TTL is not stored, but the routines waiting
		// for its load still use it
		"not_found_not_cached": {
			loadErr:  ErrNotFound,
			timeouts: notFoundNotCached,
			cached:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			loadCounter := atomic.Int64{}

			c, err := New(Params[int, string]{
				Context: testContext(t),
				Log:     test_utils.Logger(),
				Name:    "test_get_missing_entry_concurrent_load",
				LoadOneFunc: func(ID int) (*string, error) {
					loadCounter.Add(1)
					time.Sleep(50 * time.Millisecond) // keep the load in progress while the others miss the entry
					if tc.loadErr != nil {
						return nil, tc.loadErr
					}
					return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
				},
				Timeouts:        tc.timeouts,
				AutomaticReload: AutomaticReloadDisabled,
			})
			require.NoError(t, err)

			type result struct {
				value *string
				err   error
			}

			routines := 10
			wg := sync.WaitGroup{}
			results := make([]result, routines)

			// the routines wait for the cache lock, so all of them find the entry
			// missing at once when it is released
			c.Lock()
			for i := 0; i < routines; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					value, err := c.GetWithError(0)
					results[idx] = result{value: value, err: err}
				}(i)
			}
			time.Sleep(50 * time.Millisecond) // let all routines block on the lock
			c.Unlock()

			wg.Wait()

			assert.Equal(t, int64(1), loadCounter.Load(), "the missing entry should be loaded only once")
			for _, r := range results {
				if tc.loadErr != nil {
					assert.Nil(t, r.value)
					assert.ErrorIs(t, r.err, tc.loadErr)
					continue
				}
				if assert.NotNil(t, r.value) {
					assert.Equal(t, "value_0", *r.value)
				}
				assert.NoError(t, r.err)
			}

			if !tc.cached {
				assert.Equal(t, 0, cacheSize(c))
				assert.Equal(t, 0, scheduleSize(c))
				return
			}

			// the only stored entry is the loaded one, scheduled for TTL eviction
			assert.Equal(t, 1, cacheSize(c))
			assert.Equal(t, 1, scheduleSize(c))
			assert.Equal(t, "value_0", *c.Get(0))
			assert.Equal(t, int64(1), loadCounter.Load(), "the stored entry should be used")
		})
	}
}

// TestGetConcurrentMissesLoadOnce checks the single load of missing entries
// when the routines are not lined up on the cache lock (see
// TestGetMissingEntryConcurrentLoad): routines of many entries start at once,
// like concurrent requests needing the same data
func TestGetConcurrentMissesLoadOnce(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}

	c, err := New(Params[int, string]{
		Context: testContext(t),
		Log:     test_utils.Logger(),
		Name:    "test_get_concurrent_misses_load_once",
		LoadOneFunc: func(ID int) (*string, error) {
			loadCounter.Add(1)
			time.Sleep(time.Millisecond)
			return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
		},
		// a spread first reload could be due right after the load, a Get which
		// comes later would then reload the entry rightfully
		Timeouts:        timeoutsNoSpread(cacheTestTimeouts),
		AutomaticReload: AutomaticReloadDisabled,
	})
	require.NoError(t, err)

	entries := 100
	routines := 10 // per entry
	start := make(chan struct{})
	wg := sync.WaitGroup{}

	for ID := 0; ID < entries; ID++ {
		for i := 0; i < routines; i++ {
			wg.Add(1)
			go func(ID int) {
				defer wg.Done()
				<-start
				value := c.Get(ID)
				if assert.NotNil(t, value) {
					assert.Equal(t, "value_"+strconv.Itoa(ID), *value)
				}
			}(ID)
		}
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(entries), loadCounter.Load(), "each missing entry should be loaded only once")
}
