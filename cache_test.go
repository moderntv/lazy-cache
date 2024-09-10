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
}

func testCacheParallelism(t *testing.T) {
	t.Parallel()

	loadCounter := atomic.Int64{}

	c, err := NewCache(Params[int, string]{
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

	c, err := NewCache(Params[int, int]{
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

	c, err := NewCache(Params[int, string]{
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

	c, err := NewCache(Params[int, string]{
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

	c, err := NewCache(Params[int, string]{
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

	c, err := NewCache(Params[int, string]{
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
