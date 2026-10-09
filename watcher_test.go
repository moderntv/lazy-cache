package lazy

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

// assertScheduleConsistent checks the schedule heap and its invariant: an entry
// is scheduled only while it is the entry stored in cache for its ID. When
// `allScheduled` is set, every stored entry must be scheduled for TTL eviction
// (true whenever no initial load is in progress).
func assertScheduleConsistent[K comparable, T any](t *testing.T, c *Cache[K, T], allScheduled bool) {
	t.Helper()

	c.mu.RLock()
	defer c.mu.RUnlock()

	for i, entry := range c.schedule {
		assert.Equal(t, i, entry.heapIdx, "heap index of %v", entry.id)
		assert.Same(t, c.data[entry.id], entry, "scheduled entry %v is not stored in cache", entry.id)
		assert.NotEqual(t, noDeadline, entry.deadline(), "entry %v scheduled without deadline", entry.id)
		if i > 0 {
			parent := c.schedule[(i-1)/2]
			assert.LessOrEqual(t, parent.deadline(), entry.deadline(), "heap order broken at %d", i)
		}
	}

	for id, entry := range c.data {
		assert.Equal(t, id, entry.id)
		if allScheduled {
			assert.GreaterOrEqual(t, entry.heapIdx, 0, "entry %v in cache is not scheduled", id)
			assert.NotEqual(t, noDeadline, entry.expiresAt, "entry %v in cache has no TTL", id)
		}
	}

	if c.metrics != nil {
		assert.Equal(t, float64(len(c.data)), testutil.ToFloat64(c.metrics.ItemsCount), "items count metric")
	}
}

// testContext returns context cancelled when the test finishes, so background
// goroutines of the tested cache stop
func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return ctx
}

func scheduleSize[K comparable, T any](c *Cache[K, T]) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.schedule)
}

// TestWatcherConcurrentConsistency runs all cache operations concurrently while
// the watcher evicts and reloads entries, then checks that every entry is
// scheduled exactly as it should be and that everything expires in the end
// (no entry is left in cache without TTL).
func TestWatcherConcurrentConsistency(t *testing.T) {
	t.Parallel()

	load := func(ID int) (*string, error) {
		switch {
		case ID%10 == 0:
			return nil, errors.New("adhoc error")
		case ID%10 < 3:
			return nil, ErrNotFound
		}
		return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
	}

	c, err := New(Params[int, string]{
		Context:         testContext(t),
		Log:             test_utils.Logger(),
		Name:            "test_watcher_concurrent",
		MetricsRegistry: test_utils.Metrics("test_watcher_concurrent"),
		LoadOneFunc:     load,
		LoadMultipleFunc: func(IDs []int) (entries []LoadedEntry[int, string]) {
			for _, ID := range IDs {
				value, err := load(ID)
				entries = append(entries, LoadedEntry[int, string]{ID: ID, Value: value, Err: err})
			}
			return
		},
		Timeouts: Timeouts{
			TTL:            300 * time.Millisecond,
			NotFoundTTL:    200 * time.Millisecond,
			ErrorTTL:       100 * time.Millisecond,
			ReloadInterval: 100 * time.Millisecond,
			Randomizer:     0.5,
		},
		AutomaticReload: AutomaticReloadAllEntries,
	})
	assert.Nil(t, err)

	routines := 16
	maxID := 200
	stop := time.Now().Add(1500 * time.Millisecond)

	wg := sync.WaitGroup{}
	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for time.Now().Before(stop) {
				ID := rand.Intn(maxID)

				switch rand.Intn(10) {
				case 0:
					c.Remove(ID)
				case 1:
					c.Invalidate(ID)
				case 2:
					_ = c.GetMultiple([]int{ID, (ID + 1) % maxID, (ID + 7) % maxID})
				default:
					value := c.Get(ID)
					if ID%10 >= 3 {
						assert.Equal(t, "value_"+strconv.Itoa(ID), *value)
					}
				}
			}
		}()
	}
	wg.Wait()

	assertScheduleConsistent(t, c, true)

	// nobody accesses the cache anymore, so every entry has to expire
	assert.Eventually(t, func() bool { return cacheSize(c) == 0 }, 3*time.Second, 50*time.Millisecond)
	assert.Equal(t, 0, scheduleSize(c))
	assertScheduleConsistent(t, c, true)
}

// TestWatcherMassExpiration checks that a huge number of entries expiring at
// the very same time is evicted completely (in several batches).
func TestWatcherMassExpiration(t *testing.T) {
	t.Parallel()

	entries := 50 * watcherBatchSize

	preloadChan := make(chan LoadedEntry[int, string], 1000)
	go func() {
		for i := 0; i < entries; i++ {
			preloadChan <- LoadedEntry[int, string]{ID: i, Value: test_utils.StringPointer("value")}
		}
		close(preloadChan)
	}()

	c, err := New(Params[int, string]{
		Context:         testContext(t),
		Log:             test_utils.Logger(),
		Name:            "test_watcher_mass_expiration",
		MetricsRegistry: test_utils.Metrics("test_watcher_mass_expiration"),
		LoadOneFunc: func(ID int) (*string, error) {
			return test_utils.StringPointer("value"), nil
		},
		Timeouts: Timeouts{
			TTL:            500 * time.Millisecond,
			NotFoundTTL:    500 * time.Millisecond,
			ErrorTTL:       500 * time.Millisecond,
			ReloadInterval: 500 * time.Millisecond,
			Randomizer:     0,
		},
		PreloadChan:     preloadChan,
		AutomaticReload: AutomaticReloadDisabled,
	})
	assert.Nil(t, err)

	c.Ready()
	assert.Equal(t, entries, cacheSize(c))
	assertScheduleConsistent(t, c, true)

	assert.Eventually(t, func() bool { return cacheSize(c) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, scheduleSize(c))
	assertScheduleConsistent(t, c, true)
}

// TestWatcherInvalidateReloadsImmediately checks that invalidated entry is
// reloaded in background right away (it does not wait for the next planned
// run of the watcher).
func TestWatcherInvalidateReloadsImmediately(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}

	c, err := New(Params[int, string]{
		Context: testContext(t),
		Log:     test_utils.Logger(),
		Name:    "test_watcher_invalidate",
		LoadOneFunc: func(ID int) (*string, error) {
			return test_utils.StringPointer("value_" + strconv.FormatInt(loadCounter.Add(1), 10)), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadAllEntries,
	})
	assert.Nil(t, err)

	assert.Equal(t, "value_1", *c.Get(0))

	c.Invalidate(0)
	assert.Eventually(t, func() bool { return loadCounter.Load() == 2 }, time.Second, time.Millisecond)

	value, exists := c.GetCached(0)
	assert.True(t, exists)
	assert.Equal(t, "value_2", *value)
	assert.Equal(t, int64(2), loadCounter.Load())
	assertScheduleConsistent(t, c, true)
}

// TestWatcherInvalidateDuringReload checks that Invalidate called while the
// entry is being reloaded in background is not lost. The reload could have
// read the data before they changed, so the entry has to be reloaded again.
func TestWatcherInvalidateDuringReload(t *testing.T) {
	t.Parallel()

	version := atomic.Int64{} // version of data in the data storage
	loadCounter := atomic.Int64{}
	reloadStarted := make(chan struct{})
	reloadRelease := make(chan struct{})

	c, err := New(Params[int, string]{
		Context: testContext(t),
		Log:     test_utils.Logger(),
		Name:    "test_watcher_invalidate_during_reload",
		LoadOneFunc: func(ID int) (*string, error) {
			value := "value_" + strconv.FormatInt(version.Load(), 10)
			// only the first automatic reload is blocked
			if loadCounter.Add(1) == 2 {
				close(reloadStarted)
				<-reloadRelease
			}
			return &value, nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadAllEntries,
	})
	assert.Nil(t, err)

	assert.Equal(t, "value_0", *c.Get(0))

	// the data change while the automatic reload is in progress
	c.Invalidate(0)
	<-reloadStarted
	version.Store(1)
	c.Invalidate(0)
	close(reloadRelease)

	assert.Eventually(t, func() bool {
		value, exists := c.GetCached(0)
		return exists && *value == "value_1"
	}, time.Second, time.Millisecond)
	assertScheduleConsistent(t, c, true)
}

// TestWatcherEntryRemovedDuringLoad checks that an entry removed while being
// loaded is not scheduled afterwards (it would evict its successor otherwise).
func TestWatcherEntryRemovedDuringLoad(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}
	loadStarted := make(chan struct{})
	loadRelease := make(chan struct{})

	c, err := New(Params[int, string]{
		Context:         testContext(t),
		Log:             test_utils.Logger(),
		Name:            "test_watcher_removed_during_load",
		MetricsRegistry: test_utils.Metrics("test_watcher_removed_during_load"),
		LoadOneFunc: func(ID int) (*string, error) {
			// only the first load is blocked (automatic reloads may follow)
			if loadCounter.Add(1) == 1 {
				close(loadStarted)
				<-loadRelease
			}
			return test_utils.StringPointer("value"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadAllEntries,
	})
	assert.Nil(t, err)

	done := make(chan *string)
	go func() { done <- c.Get(0) }()

	<-loadStarted
	assert.Equal(t, 1, cacheSize(c))
	c.Remove(0)
	close(loadRelease)

	assert.Equal(t, "value", *<-done)
	assert.Equal(t, 0, cacheSize(c))
	assert.Equal(t, 0, scheduleSize(c))
	assertScheduleConsistent(t, c, true)
}

// TestWatcherEntryReplacedDuringLoad checks that an entry replaced (e.g. by
// GetMultiple) while being loaded by Get does not affect its successor.
func TestWatcherEntryReplacedDuringLoad(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}
	loadStarted := make(chan struct{})
	loadRelease := make(chan struct{})

	c, err := New(Params[int, string]{
		Context:         testContext(t),
		Log:             test_utils.Logger(),
		Name:            "test_watcher_replaced_during_load",
		MetricsRegistry: test_utils.Metrics("test_watcher_replaced_during_load"),
		LoadOneFunc: func(ID int) (*string, error) {
			// only the first load is blocked (automatic reloads may follow)
			if loadCounter.Add(1) == 1 {
				close(loadStarted)
				<-loadRelease
			}
			return nil, ErrNotFound
		},
		LoadMultipleFunc: func(IDs []int) (entries []LoadedEntry[int, string]) {
			for _, ID := range IDs {
				entries = append(entries, LoadedEntry[int, string]{ID: ID, Value: test_utils.StringPointer("batch")})
			}
			return
		},
		Timeouts: Timeouts{
			TTL:            7 * time.Second,
			NotFoundTTL:    0, // Get removes not found entries
			ErrorTTL:       1 * time.Second,
			ReloadInterval: 3 * time.Second,
		},
		AutomaticReload: AutomaticReloadAllEntries,
	})
	assert.Nil(t, err)

	done := make(chan *string)
	go func() { done <- c.Get(0) }()

	<-loadStarted
	assert.Equal(t, "batch", *c.GetMultiple([]int{0})[0])
	close(loadRelease)

	assert.Nil(t, <-done)

	// the entry from GetMultiple stays in cache and scheduled
	value, exists := c.GetCached(0)
	assert.True(t, exists)
	assert.Equal(t, "batch", *value)
	assert.Equal(t, 1, scheduleSize(c))
	assertScheduleConsistent(t, c, true)
}
