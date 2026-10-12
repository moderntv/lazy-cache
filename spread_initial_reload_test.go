package lazy

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

const spreadTestEntries = 200

func spreadTestValue(ID int) *string {
	return test_utils.StringPointer("value_" + strconv.Itoa(ID))
}

func newSpreadTestCache(t *testing.T, timeouts Timeouts, preload <-chan LoadedEntry[int, string]) *Cache[int, string] {
	c, err := New(Params[int, string]{
		Context: testContext(t),
		Log:     test_utils.Logger(),
		Name:    "test_spread_initial_reload",
		LoadOneFunc: func(ID int) (*string, error) {
			return spreadTestValue(ID), nil
		},
		LoadMultipleFunc: func(IDs []int) (entries []LoadedEntry[int, string]) {
			for _, ID := range IDs {
				entries = append(entries, LoadedEntry[int, string]{ID: ID, Value: spreadTestValue(ID)})
			}
			return
		},
		Timeouts:        timeouts,
		PreloadChan:     preload,
		AutomaticReload: AutomaticReloadDisabled,
	})
	require.NoError(t, err)

	return c
}

// spreadTestFirstLoads load all test entries at once into a new cache, each by
// another path which loads entries for the first time
var spreadTestFirstLoads = map[string]func(t *testing.T, timeouts Timeouts) *Cache[int, string]{
	"preload": func(t *testing.T, timeouts Timeouts) *Cache[int, string] {
		preload := make(chan LoadedEntry[int, string], spreadTestEntries)
		for ID := range spreadTestEntries {
			preload <- LoadedEntry[int, string]{ID: ID, Value: spreadTestValue(ID)}
		}
		close(preload)

		c := newSpreadTestCache(t, timeouts, preload)
		c.Ready()

		return c
	},
	"get": func(t *testing.T, timeouts Timeouts) *Cache[int, string] {
		c := newSpreadTestCache(t, timeouts, nil)
		for ID := range spreadTestEntries {
			require.Equal(t, spreadTestValue(ID), c.Get(ID))
		}

		return c
	},
	"get_multiple": func(t *testing.T, timeouts Timeouts) *Cache[int, string] {
		c := newSpreadTestCache(t, timeouts, nil)
		IDs := make([]int, 0, spreadTestEntries)
		for ID := range spreadTestEntries {
			IDs = append(IDs, ID)
		}
		require.Len(t, c.GetMultiple(IDs), spreadTestEntries)

		return c
	},
}

// spreadTestEntryTimes returns cache times of the next reload and the TTL
// eviction of the entry
func spreadTestEntryTimes(t *testing.T, c *Cache[int, string], ID int) (nextReload int64, expiresAt int64) {
	t.Helper()

	entry := cacheEntryOf(c, ID)
	require.NotNil(t, entry, "entry %d not in cache", ID)

	c.mu.RLock()
	defer c.mu.RUnlock()

	return entry.nextReload.Load(), entry.expiresAt
}

// Entries loaded at once have their first reloads spread over the whole
// ReloadInterval, their TTL stays the same
func TestCacheSpreadInitialReload(t *testing.T) {
	// cache time starts at 0 when the cache is created
	reloadInterval := cacheTestTimeouts.ReloadInterval.Milliseconds()
	ttl := cacheTestTimeouts.TTL.Milliseconds()

	for name, firstLoad := range spreadTestFirstLoads {
		t.Run(name, func(t *testing.T) {
			c := firstLoad(t, cacheTestTimeouts)
			loadedAt := c.now()

			reloadOutOfRange := 0
			ttlOutOfRange := 0
			firstHalf := 0
			secondHalf := 0

			for ID := range spreadTestEntries {
				nextReload, expiresAt := spreadTestEntryTimes(t, c, ID)

				if expiresAt < ttl || expiresAt > loadedAt+ttl {
					ttlOutOfRange++
				}

				if nextReload < 0 || nextReload >= loadedAt+reloadInterval {
					reloadOutOfRange++
				} else if nextReload < reloadInterval/2 {
					firstHalf++
				} else {
					secondHalf++
				}
			}

			t.Logf("next reloads in first half of ReloadInterval: %d", firstHalf)
			t.Logf("next reloads in second half of ReloadInterval: %d", secondHalf)

			assert.Zero(t, reloadOutOfRange, "next reloads out of [0, loadedAt + ReloadInterval)")
			assert.Zero(t, ttlOutOfRange, "TTL evictions out of [TTL, loadedAt + TTL]")
			assert.Greater(t, firstHalf, spreadTestEntries/4)
			assert.Greater(t, secondHalf, spreadTestEntries/4)
		})
	}
}

// With DisableSpreadInitialReload all entries loaded at once are reloaded
// after ReloadInterval
func TestCacheSpreadInitialReloadDisabled(t *testing.T) {
	timeouts := cacheTestTimeouts
	timeouts.DisableSpreadInitialReload = true

	// cache time starts at 0 when the cache is created
	reloadInterval := timeouts.ReloadInterval.Milliseconds()
	ttl := timeouts.TTL.Milliseconds()

	for name, firstLoad := range spreadTestFirstLoads {
		t.Run(name, func(t *testing.T) {
			c := firstLoad(t, timeouts)
			loadedAt := c.now()

			reloadOutOfRange := 0
			ttlOutOfRange := 0

			for ID := range spreadTestEntries {
				nextReload, expiresAt := spreadTestEntryTimes(t, c, ID)

				if nextReload < reloadInterval || nextReload > loadedAt+reloadInterval {
					reloadOutOfRange++
				}
				if expiresAt < ttl || expiresAt > loadedAt+ttl {
					ttlOutOfRange++
				}
			}

			assert.Zero(t, reloadOutOfRange, "next reloads out of [ReloadInterval, loadedAt + ReloadInterval]")
			assert.Zero(t, ttlOutOfRange, "TTL evictions out of [TTL, loadedAt + TTL]")
		})
	}
}

// Entries spread by the first load are reloaded on Get when their spread reload
// time passes and the reload plans the next one after the whole ReloadInterval
func TestCacheReloadAfterSpreadInitialReload(t *testing.T) {
	timeouts := cacheTestTimeouts
	timeouts.ReloadInterval = 300 * time.Millisecond
	reloadInterval := timeouts.ReloadInterval.Milliseconds()

	c := spreadTestFirstLoads["preload"](t, timeouts)

	// all spread reloads are due
	time.Sleep(timeouts.ReloadInterval)

	reloadedAt := c.now()
	for ID := range spreadTestEntries {
		require.Equal(t, spreadTestValue(ID), c.Get(ID))
	}
	loadedAt := c.now()

	reloadOutOfRange := 0
	for ID := range spreadTestEntries {
		nextReload, _ := spreadTestEntryTimes(t, c, ID)

		if nextReload < reloadedAt+reloadInterval || nextReload > loadedAt+reloadInterval {
			reloadOutOfRange++
		}
	}

	assert.Zero(t, reloadOutOfRange, "next reloads out of [reloadedAt + ReloadInterval, loadedAt + ReloadInterval]")
}
