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

	"github.com/stretchr/testify/assert"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

var cacheTestTimeouts = Timeouts{
	TTL:            7 * time.Second,
	NotFoundTTL:    5 * time.Second,
	ErrorTTL:       1 * time.Second,
	ReloadInterval: 3 * time.Second,
	Randomizer:     0,
}

func TestCache(t *testing.T) {
	t.Run("parallelism", testCacheParallelism)
	t.Run("entries_expiration", testCacheEntriesExpiration)
	t.Run("error_entry_reload", testCacheErrorEntryReload)
	t.Run("entry_ttl_prolong", testCacheEntryTTLProlong)
	t.Run("entry_automatic_reload_all", testCacheEntryAutomaticReloadAll)
	t.Run("entry_automatic_reload_accessed", testCacheEntryAutomaticReloadAccessed)
	t.Run("testCacheMemsizeCalculated", testCacheMemsizeCalculated)
	t.Run("testCacheMemsizeManual", testCacheMemsizeManual)
	t.Run("get_cached", testCacheGetCached)
	t.Run("force_set", testCacheForceSet)
	t.Run("expired_entry_concurrent_reload", testCacheExpiredEntryConcurrentReload)
	t.Run("expired_entry_double_check", testCacheExpiredEntryDoubleCheck)
}

func testCacheParallelism(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			loadCounter.Add(1)

			// 10% will return error
			if ID%10 == 0 {
				return test_utils.StringPointer("err"), errors.New("adhoc error")
			}
			// 20% will return not found
			if ID%10 < 3 {
				return test_utils.StringPointer("404"), ErrNotFound
			}
			// 70% will return value
			return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	routines := 100
	iterations := 100000
	maxID := 100

	getCounter := atomic.Int64{}
	invalidationsCounter := atomic.Int64{}
	removeCounter := atomic.Int64{}

	wg := sync.WaitGroup{}

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func() {
			for j := 0; j < iterations; j++ {
				id := rand.Intn(maxID)

				random := rand.Intn(10)
				// 10% chance to invalidate
				if random == 0 {
					invalidationsCounter.Add(1)
					c.Invalidate(id)
				}

				// 10% chance remove
				if random == 1 {
					removeCounter.Add(1)
					c.Remove(id)
				}

				getCounter.Add(1)
				value := c.Get(id)

				if id%10 == 0 {
					// error
					if value != nil {
						assert.Equal(t, "value_"+strconv.Itoa(id), *value)
					}
				} else if id%10 < 3 {
					// not found
					assert.Nil(t, value)
				} else {
					// success load
					assert.Equal(t, "value_"+strconv.Itoa(id), *value)
				}
			}
			wg.Done()
		}()
	}

	wg.Wait()

	t.Log("Gets:", getCounter.Load())
	t.Log("Loads:", loadCounter.Load())
	t.Log("Invalidation:", invalidationsCounter.Load())
	t.Log("Removals:", removeCounter.Load())
}

func testCacheErrorEntryReload(t *testing.T) {
	t.Parallel()

	timouts := Timeouts{
		TTL:            10 * time.Second,
		NotFoundTTL:    0,
		ErrorTTL:       0,
		ReloadInterval: 1 * time.Second,
		Randomizer:     0,
	}

	increment := 0

	c, err := New(Params[int, int]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *int, err error) {
			value := increment
			increment++
			return test_utils.IntPointer(value), nil
		},
		Timeouts:        timouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// 0s
	assert.Equal(t, 0, *c.Get(0))
	assert.Equal(t, 0, *c.Get(0))
	time.Sleep(500 * time.Millisecond)
	// 0.5s
	assert.Equal(t, 0, *c.Get(0))
	time.Sleep(1000 * time.Millisecond)
	// 1.5s (reload)
	assert.Equal(t, 1, *c.Get(0))
	time.Sleep(500 * time.Millisecond)
	// 2s
	assert.Equal(t, 1, *c.Get(0))
	assert.Equal(t, 1, *c.Get(0))
	time.Sleep(3000 * time.Millisecond)
	// 5s
	assert.Equal(t, 2, *c.Get(0))
}

func testCacheEntriesExpiration(t *testing.T) {
	t.Parallel()

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			if ID == 0 {
				return nil, errors.New("adhoc error")
			}
			if ID == 1 {
				return nil, ErrNotFound
			}
			return test_utils.StringPointer("value"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// 0s
	assert.Equal(t, 0, len(c.data))
	_ = c.Get(0)
	assert.Equal(t, 1, len(c.data))
	_ = c.Get(1)
	assert.Equal(t, 2, len(c.data))
	_ = c.Get(2)
	assert.Equal(t, 3, len(c.data))
	time.Sleep(500 * time.Millisecond)
	// 0.5s (all items in cache)
	assert.NotNil(t, c.data[0])
	assert.NotNil(t, c.data[1])
	assert.NotNil(t, c.data[2])
	time.Sleep(1000 * time.Millisecond)
	// 1.5s (removed error item)
	assert.Nil(t, c.data[0])
	assert.NotNil(t, c.data[1])
	assert.NotNil(t, c.data[2])
	time.Sleep(4500 * time.Millisecond)
	// 6s (removed not found item)
	assert.Nil(t, c.data[0])
	assert.Nil(t, c.data[1])
	assert.NotNil(t, c.data[2])
	time.Sleep(2000 * time.Millisecond)
	// 8s (removed all items)
	assert.Equal(t, 0, len(c.data))
}

func testCacheEntryTTLProlong(t *testing.T) {
	t.Parallel()

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			return test_utils.StringPointer("value"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// 0s
	assert.Nil(t, c.data[0])
	_ = c.Get(0) // lazy loaded
	assert.True(t, c.data[0].accessed.Load())
	assert.NotNil(t, c.data[0])
	time.Sleep(4 * time.Second)
	// 4s
	assert.NotNil(t, c.data[0])
	assert.True(t, c.data[0].accessed.Load())
	_ = c.Get(0) // lazy reloaded, TTL at 11s
	assert.True(t, c.data[0].accessed.Load())
	assert.NotNil(t, c.data[0])
	time.Sleep(6 * time.Second)
	// 10s
	assert.True(t, c.data[0].accessed.Load())
	assert.NotNil(t, c.data[0])
	time.Sleep(2 * time.Second)
	// 12s
	assert.Nil(t, c.data[0])
}

func testCacheEntryAutomaticReloadAll(t *testing.T) {
	t.Parallel()

	loadCounter := 0

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			loadCounter++
			return test_utils.StringPointer("value"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadAllEntries,
	})

	assert.Nil(t, err)

	assert.Equal(t, 0, loadCounter)
	_ = c.Get(0)
	assert.Equal(t, 1, loadCounter)
	time.Sleep(6500 * time.Millisecond)
	// 6.5 s
	assert.Equal(t, 3, loadCounter)
	assert.Equal(t, 1, len(c.data))
	time.Sleep(3 * time.Second)
	// 9.5 s
	assert.Equal(t, 4, loadCounter)
	assert.Equal(t, 1, len(c.data))
	time.Sleep(1 * time.Second)
	// 10.5 s
	assert.Equal(t, 4, loadCounter)
	assert.Equal(t, 0, len(c.data))
	time.Sleep(3 * time.Second)
	// 13.5 s
	assert.Equal(t, 4, loadCounter)
}

func testCacheEntryAutomaticReloadAccessed(t *testing.T) {
	t.Parallel()

	loadCounter := 0

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			loadCounter++
			return test_utils.StringPointer("value"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadAccessedEntries,
	})

	assert.Nil(t, err)

	// 0s - create entry
	_ = c.Get(0)
	time.Sleep(6500 * time.Millisecond)
	// 6.5 s (1x automatically reloaded)
	assert.Equal(t, 2, loadCounter)
	assert.Equal(t, 1, len(c.data))
	_ = c.Get(0) // lazy reload at 6.5s
	assert.Equal(t, 3, loadCounter)
	time.Sleep(2 * time.Second)
	// 8.5 s (2 seconds after lazy reload)
	assert.Equal(t, 3, loadCounter)
	assert.Equal(t, 1, len(c.data))
	_ = c.Get(0)
	assert.Equal(t, 3, loadCounter)
	time.Sleep(2 * time.Second)
	// 10.5 s (1x automatically reloaded at 9.5s)
	assert.Equal(t, 4, loadCounter)
	time.Sleep(2500 * time.Millisecond)
	// 13s (no automatic reload)
	assert.Equal(t, 4, loadCounter)
	assert.Equal(t, 1, len(c.data))
	time.Sleep(3 * time.Second)
	// 16s (no ttl expiration yet)
	assert.Equal(t, 4, loadCounter)
	assert.Equal(t, 1, len(c.data))
	time.Sleep(1 * time.Second)
	// 17s (ttl expiration at 16.5s)
	assert.Equal(t, 4, loadCounter)
	assert.Equal(t, 0, len(c.data))
}

func testCacheGetCached(t *testing.T) {
	t.Parallel()

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache_is_cached",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			return test_utils.StringPointer("value_" + strconv.Itoa(ID)), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// Test non-existent key
	value, exists := c.GetCached(0)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Load a value into cache
	_ = c.Get(0)
	value, exists = c.GetCached(0)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_0", *value)

	// Test another non-existent key
	value, exists = c.GetCached(1)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Load another value
	_ = c.Get(1)
	value, exists = c.GetCached(1)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_1", *value)
	value, exists = c.GetCached(0)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_0", *value)

	// Wait for entry to expire (ReloadInterval is 3s, so after 3.5s it should be expired)
	time.Sleep(3500 * time.Millisecond)
	value, exists = c.GetCached(0)
	assert.Nil(t, value)
	assert.False(t, exists)
	value, exists = c.GetCached(1)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Reload one entry
	_ = c.Get(0)
	value, exists = c.GetCached(0)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_0", *value)
	value, exists = c.GetCached(1)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Test Remove
	c.Remove(0)
	value, exists = c.GetCached(0)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Test Invalidate - entry should still exist but be expired
	_ = c.Get(2)
	value, exists = c.GetCached(2)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_2", *value)
	c.Invalidate(2)
	value, exists = c.GetCached(2)
	assert.Nil(t, value)
	assert.False(t, exists)

	// Wait for TTL expiration (TTL is 7s)
	_ = c.Get(3)
	value, exists = c.GetCached(3)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_3", *value)
	time.Sleep(7500 * time.Millisecond)
	// Entry should be removed by TTL watcher
	value, exists = c.GetCached(3)
	assert.Nil(t, value)
	assert.False(t, exists)
}

func testCacheForceSet(t *testing.T) {
	t.Parallel()

	loadCounter := 0

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache_force_set",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			loadCounter++
			return test_utils.StringPointer("loaded_" + strconv.Itoa(ID)), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// Test setting a new entry that doesn't exist
	value, exists := c.GetCached(0)
	assert.Nil(t, value)
	assert.False(t, exists)
	c.ForceSet(0, test_utils.StringPointer("forced_value_0"), nil)
	value, exists = c.GetCached(0)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "forced_value_0", *value)
	value = c.Get(0)
	assert.Equal(t, "forced_value_0", *value)
	assert.Equal(t, 0, loadCounter) // Load function should not be called

	// Test overriding an existing entry
	c.ForceSet(0, test_utils.StringPointer("forced_value_0_updated"), nil)
	value, exists = c.GetCached(0)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "forced_value_0_updated", *value)
	value = c.Get(0)
	assert.Equal(t, "forced_value_0_updated", *value)
	assert.Equal(t, 0, loadCounter) // Still no load calls

	// Test overriding an entry that was loaded normally
	_ = c.Get(1)
	assert.Equal(t, 1, loadCounter)
	value, exists = c.GetCached(1)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "loaded_1", *value)
	value = c.Get(1)
	assert.Equal(t, "loaded_1", *value)

	c.ForceSet(1, test_utils.StringPointer("forced_value_1"), nil)
	value, exists = c.GetCached(1)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "forced_value_1", *value)
	value = c.Get(1)
	assert.Equal(t, "forced_value_1", *value)
	assert.Equal(t, 1, loadCounter) // No additional load calls

	// Test that ForceSet respects TTL and ReloadInterval
	c.ForceSet(2, test_utils.StringPointer("forced_value_2"), nil)
	value, exists = c.GetCached(2)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "forced_value_2", *value)
	time.Sleep(3500 * time.Millisecond)
	// After ReloadInterval (3s), entry should be expired
	value, exists = c.GetCached(2)
	assert.Nil(t, value)
	assert.False(t, exists)
	// But entry should still exist in cache (not removed by TTL watcher yet)
	c.mu.RLock()
	_, exists = c.data[2]
	c.mu.RUnlock()
	assert.True(t, exists)

	// Test setting nil value (should work, but entry will have nil value)
	c.ForceSet(3, nil, nil)
	value, exists = c.GetCached(3)
	assert.Nil(t, value)
	assert.True(t, exists)
	value = c.Get(3)
	assert.Nil(t, value)

	// Test that ForceSet updates watchers correctly
	c.ForceSet(4, test_utils.StringPointer("forced_value_4"), nil)
	value, exists = c.GetCached(4)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "forced_value_4", *value)
	time.Sleep(7500 * time.Millisecond)
	// After TTL (7s), entry should be removed by TTL watcher
	value, exists = c.GetCached(4)
	assert.Nil(t, value)
	assert.False(t, exists)
	c.mu.RLock()
	_, exists = c.data[4]
	c.mu.RUnlock()
	assert.False(t, exists)

	// Test ForceSet with ErrNotFound
	c.ForceSet(5, nil, ErrNotFound)
	value, exists = c.GetCached(5)
	assert.Nil(t, value)
	assert.True(t, exists) // Entry exists but value is nil (NotFound)
	value = c.Get(5)
	assert.Nil(t, value)
	// Entry should exist in cache with NotFoundTTL (5s)
	c.mu.RLock()
	_, exists = c.data[5]
	c.mu.RUnlock()
	assert.True(t, exists)
	time.Sleep(5500 * time.Millisecond)
	// After NotFoundTTL (5s), entry should be removed by TTL watcher
	c.mu.RLock()
	_, exists = c.data[5]
	c.mu.RUnlock()
	assert.False(t, exists)

	// Test ForceSet with generic error (new entry)
	c.ForceSet(6, nil, errors.New("generic error"))
	value, exists = c.GetCached(6)
	assert.Nil(t, value)
	assert.True(t, exists) // Entry exists but value is nil (error)
	value = c.Get(6)
	assert.Nil(t, value)
	// Entry should exist in cache with ErrorTTL (1s) for new entries
	c.mu.RLock()
	_, exists = c.data[6]
	c.mu.RUnlock()
	assert.True(t, exists)
	time.Sleep(1500 * time.Millisecond)
	// After ErrorTTL (1s), entry should be removed by TTL watcher
	c.mu.RLock()
	_, exists = c.data[6]
	c.mu.RUnlock()
	assert.False(t, exists)

	// Test ForceSet with generic error (existing entry - should not update TTL or value)
	c.ForceSet(7, test_utils.StringPointer("value_7"), nil)
	value, exists = c.GetCached(7)
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_7", *value)
	// Now set it with an error - TTL and value should not be updated for existing entries
	c.ForceSet(7, nil, errors.New("generic error"))
	value, exists = c.GetCached(7)
	// Value should remain unchanged (not set to nil) because error on existing entry doesn't update value
	assert.NotNil(t, value)
	assert.True(t, exists)
	assert.Equal(t, "value_7", *value)
	// Entry should still exist but with original TTL (not ErrorTTL)
	c.mu.RLock()
	_, exists = c.data[7]
	c.mu.RUnlock()
	assert.True(t, exists)
	// Wait for original TTL (7s) to expire
	time.Sleep(7500 * time.Millisecond)
	c.mu.RLock()
	_, exists = c.data[7]
	c.mu.RUnlock()
	assert.False(t, exists)
}

// testCacheExpiredEntryConcurrentReload tests that concurrent Get calls on an expired entry
// with automatic reload disabled only trigger a single reload (double-check pattern prevents duplicates)
func testCacheExpiredEntryConcurrentReload(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}
	loadDelay := 50 * time.Millisecond // Simulate slow load function

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache_concurrent_reload",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			loadCounter.Add(1)
			time.Sleep(loadDelay) // Simulate slow loading
			return test_utils.StringPointer("value_" + strconv.FormatInt(loadCounter.Load(), 10)), nil
		},
		Timeouts: Timeouts{
			TTL:            10 * time.Second,
			NotFoundTTL:    5 * time.Second,
			ErrorTTL:       1 * time.Second,
			ReloadInterval: 1 * time.Second, // Entry expires after 1 second
			Randomizer:     0,
		},
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// Load entry initially
	value := c.Get(0)
	assert.Equal(t, "value_1", *value)
	assert.Equal(t, int64(1), loadCounter.Load())

	// Wait for entry to expire (ReloadInterval is 1s)
	time.Sleep(1100 * time.Millisecond)

	// Now make concurrent Get calls on expired entry
	// All should wait for the first one to reload, then use the reloaded value
	routines := 10
	wg := sync.WaitGroup{}
	results := make([]*string, routines)

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = c.Get(0)
		}(i)
	}

	wg.Wait()

	// All results should be the same (from single reload)
	expectedValue := results[0]
	assert.NotNil(t, expectedValue)
	for i := 1; i < routines; i++ {
		assert.Equal(t, expectedValue, results[i], "All concurrent Get calls should return the same value")
	}

	// Only one additional reload should have occurred (the first goroutine to acquire lock)
	// Other goroutines should have used the double-check and returned the reloaded value
	assert.Equal(t, int64(2), loadCounter.Load(), "Should only have 2 loads total (initial + one concurrent reload)")
}

// testCacheExpiredEntryDoubleCheck tests the double-check pattern where an entry
// is reloaded by another goroutine while waiting for the lock
func testCacheExpiredEntryDoubleCheck(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}
	reloadStarted := make(chan struct{})
	reloadComplete := make(chan struct{})

	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache_double_check",
		LoadOneFunc: func(ID int) (entry *string, err error) {
			count := loadCounter.Add(1)
			if count == 1 {
				// First load - return immediately
				return test_utils.StringPointer("value_1"), nil
			}
			// Second load - signal start, wait for signal to complete
			close(reloadStarted)
			<-reloadComplete
			return test_utils.StringPointer("value_2"), nil
		},
		Timeouts: Timeouts{
			TTL:            10 * time.Second,
			NotFoundTTL:    5 * time.Second,
			ErrorTTL:       1 * time.Second,
			ReloadInterval: 1 * time.Second,
			Randomizer:     0,
		},
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	// Load entry initially
	value := c.Get(0)
	assert.Equal(t, "value_1", *value)
	assert.Equal(t, int64(1), loadCounter.Load())

	// Wait for entry to expire
	time.Sleep(1100 * time.Millisecond)

	// Start first goroutine that will reload (and will wait for signal)
	go func() {
		_ = c.Get(0)
	}()

	// Wait for reload to start
	<-reloadStarted

	// Now start second goroutine - it should wait for lock, then use double-check
	// to see that entry was already reloaded and return the new value
	secondValue := make(chan *string, 1)
	go func() {
		secondValue <- c.Get(0)
	}()

	// Give second goroutine time to wait for lock
	time.Sleep(10 * time.Millisecond)

	// Complete the reload
	close(reloadComplete)

	// Wait for second goroutine to complete
	result := <-secondValue

	// Second goroutine should have used double-check and returned the reloaded value
	assert.NotNil(t, result)
	assert.Equal(t, "value_2", *result)

	// Only 2 loads total (initial + one reload)
	assert.Equal(t, int64(2), loadCounter.Load())
}
