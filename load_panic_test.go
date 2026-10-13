package lazy

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

// getWithTimeout calls Get and fails the test when it does not return in time
// (e.g. it waits for an entry left locked)
func getWithTimeout[K comparable, T any](t *testing.T, c *Cache[K, T], ID K) *T {
	t.Helper()

	done := make(chan *T, 1)
	go func() { done <- c.Get(ID) }()

	select {
	case value := <-done:
		return value
	case <-time.After(time.Second):
		t.Fatal("Get is blocked")
		return nil
	}
}

// TestLoadPanicReleasesEntry checks that a panic of LoadOneFunc in Get does not
// leave the entry locked: the panic is passed to the caller of Get, and both the
// routine waiting for the load and the following Get load the entry again.
func TestLoadPanicReleasesEntry(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cachedBefore bool // entry is in cache (expired) before the panicking load
	}{
		"first_load": {cachedBefore: false},
		"reload":     {cachedBefore: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			panicLoad := atomic.Bool{}
			loadStarted := make(chan struct{})
			loadRelease := make(chan struct{})

			c, err := New(Params[int, string]{
				Context: testContext(t),
				Log:     test_utils.Logger(),
				Name:    "test_load_panic_releases_entry",
				LoadOneFunc: func(ID int) (*string, error) {
					if panicLoad.CompareAndSwap(true, false) {
						close(loadStarted)
						<-loadRelease
						panic("load failed")
					}
					return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
				},
				Timeouts:        cacheTestTimeouts,
				AutomaticReload: AutomaticReloadDisabled,
			})
			require.NoError(t, err)

			if tc.cachedBefore {
				require.Equal(t, "value_0", *c.Get(0))
				c.Invalidate(0)
			}

			panicLoad.Store(true)
			panicked := make(chan any, 1)
			go func() {
				defer func() { panicked <- recover() }()
				c.Get(0)
			}()
			<-loadStarted

			waiting := make(chan *string, 1)
			go func() { waiting <- c.Get(0) }()
			time.Sleep(50 * time.Millisecond) // let the routine block on the entry

			close(loadRelease)
			assert.Equal(t, "load failed", <-panicked, "the panic should be passed to the caller of Get")

			select {
			case value := <-waiting:
				if assert.NotNil(t, value) {
					assert.Equal(t, "value_0", *value)
				}
			case <-time.After(time.Second):
				t.Fatal("Get waiting for the panicked load is blocked")
			}

			value := getWithTimeout(t, c, 0)
			if assert.NotNil(t, value) {
				assert.Equal(t, "value_0", *value)
			}
			assert.Equal(t, 1, cacheSize(c))
			assert.Equal(t, 1, scheduleSize(c))
		})
	}
}

// TestAutomaticReloadPanic checks that a panic of LoadOneFunc in the reload
// worker does not crash the application nor leave the entry locked
func TestAutomaticReloadPanic(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}

	c, err := New(Params[int, string]{
		Context: testContext(t),
		Log:     test_utils.Logger(),
		Name:    "test_automatic_reload_panic",
		LoadOneFunc: func(ID int) (*string, error) {
			// the first automatic reload panics
			if loadCounter.Add(1) == 2 {
				panic("load failed")
			}
			return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
		},
		Timeouts: timeoutsNoSpread(Timeouts{
			TTL:            10 * time.Second,
			NotFoundTTL:    time.Second,
			ErrorTTL:       time.Second,
			ReloadInterval: 200 * time.Millisecond,
		}),
		AutomaticReload: AutomaticReloadAllEntries,
	})
	require.NoError(t, err)

	require.Equal(t, "value_0", *c.Get(0))

	require.Eventually(t, func() bool { return loadCounter.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(250 * time.Millisecond) // let the data of the first load expire

	// the expired entry is reloaded by Get
	value := getWithTimeout(t, c, 0)
	if assert.NotNil(t, value) {
		assert.Equal(t, "value_0", *value)
	}
	assert.Equal(t, int64(3), loadCounter.Load())
}
