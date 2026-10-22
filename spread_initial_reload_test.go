package lazy

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
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

// Entries loaded at once are not reloaded for Randomizer part of
// ReloadInterval, then their first reloads are spread uniformly up to
// ReloadInterval and the remaining Randomizer part of them up to
// (1 + 2 * Randomizer) * ReloadInterval, their TTL does not end before the
// first reload
func TestCacheSpreadInitialReload(t *testing.T) {
	// cache time starts at 0 when the cache is created, ReloadInterval is 3s
	// and TTL 7s; next reloads are expected in
	// [minNextReload, loadedAt + maxReloadIn), half of them before split and
	// some from late, after the window [Randomizer * ReloadInterval,
	// (1 + Randomizer) * ReloadInterval) of the former uniform spread
	tests := map[string]struct {
		randomizer    float64
		minNextReload int64
		maxReloadIn   int64
		split         int64
		late          int64 // 0 means not checked
		minTTL        int64
		maxTTL        int64
	}{
		"without_randomizer": {
			randomizer:    0,
			minNextReload: 0,
			maxReloadIn:   3000,
			split:         1500,
			minTTL:        7000,
			maxTTL:        7000,
		},
		"randomizer": {
			randomizer:    0.5,
			minNextReload: 1500,
			maxReloadIn:   6000,
			split:         3000,
			late:          4500,
			minTTL:        3500,
			maxTTL:        10500,
		},
	}

	for name, tc := range tests {
		timeouts := cacheTestTimeouts
		timeouts.Randomizer = tc.randomizer

		for firstLoadName, firstLoad := range spreadTestFirstLoads {
			t.Run(name+"/"+firstLoadName, func(t *testing.T) {
				c := firstLoad(t, timeouts)
				loadedAt := c.now()

				reloadOutOfRange := 0
				ttlOutOfRange := 0
				expiresBeforeReload := 0
				firstHalf := 0
				secondHalf := 0
				lateReloads := 0

				for ID := range spreadTestEntries {
					nextReload, expiresAt := spreadTestEntryTimes(t, c, ID)

					if expiresAt < tc.minTTL || expiresAt > loadedAt+tc.maxTTL {
						ttlOutOfRange++
					}
					if expiresAt < nextReload {
						expiresBeforeReload++
					}
					if tc.late > 0 && nextReload >= tc.late {
						lateReloads++
					}

					if nextReload < tc.minNextReload || nextReload >= loadedAt+tc.maxReloadIn {
						reloadOutOfRange++
					} else if nextReload < tc.split {
						firstHalf++
					} else {
						secondHalf++
					}
				}

				t.Logf("next reloads before %d: %d", tc.split, firstHalf)
				t.Logf("next reloads from %d: %d", tc.split, secondHalf)

				assert.Zero(t, reloadOutOfRange, "next reloads out of [%d, loadedAt + %d)", tc.minNextReload, tc.maxReloadIn)
				assert.Zero(t, ttlOutOfRange, "TTL evictions out of [%d, loadedAt + %d]", tc.minTTL, tc.maxTTL)
				assert.Zero(t, expiresBeforeReload, "TTL evictions before the first reload")
				assert.Greater(t, firstHalf, spreadTestEntries/4)
				assert.Greater(t, secondHalf, spreadTestEntries/4)
				if tc.late > 0 {
					t.Logf("next reloads from %d: %d", tc.late, lateReloads)
					assert.Positive(t, lateReloads, "no next reloads from %d", tc.late)
				}
			})
		}
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

// syncBuffer collects logs written by cache goroutines
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// New warns when accessed entries can expire before their automatic reload:
// the shortest TTL is below the latest automatic reload
// 0.9 * (1 + Randomizer) * ReloadInterval. The later spread first reload
// extends TTL by itself.
func TestCacheWarnsTTLShorterThanReload(t *testing.T) {
	tests := map[string]struct {
		timeouts        Timeouts
		automaticReload AutomaticReload
		expectedWarning bool
	}{
		"ttl_too_short": {
			// shortest TTL 24h, latest reload 32.4h
			timeouts:        Timeouts{TTL: 48 * time.Hour, ReloadInterval: 24 * time.Hour, Randomizer: 0.5},
			automaticReload: AutomaticReloadAccessedEntries,
			expectedWarning: true,
		},
		"ttl_long_enough": {
			// shortest TTL 24h, latest reload 16.2h
			timeouts:        Timeouts{TTL: 48 * time.Hour, ReloadInterval: 12 * time.Hour, Randomizer: 0.5},
			automaticReload: AutomaticReloadAccessedEntries,
			expectedWarning: false,
		},
		"only_spread_first_reload_later": {
			// shortest TTL 24h, latest reload 21.6h, latest spread first
			// reload 28.8h
			timeouts:        Timeouts{TTL: 48 * time.Hour, ReloadInterval: 16 * time.Hour, Randomizer: 0.5},
			automaticReload: AutomaticReloadAllEntries,
			expectedWarning: false,
		},
		"spread_disabled_ttl_too_short": {
			timeouts: Timeouts{
				TTL:                        48 * time.Hour,
				ReloadInterval:             24 * time.Hour,
				Randomizer:                 0.5,
				DisableSpreadInitialReload: true,
			},
			automaticReload: AutomaticReloadAccessedEntries,
			expectedWarning: true,
		},
		"automatic_reload_disabled": {
			timeouts:        Timeouts{TTL: 48 * time.Hour, ReloadInterval: 24 * time.Hour, Randomizer: 0.5},
			automaticReload: AutomaticReloadDisabled,
			expectedWarning: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs := &syncBuffer{}
			_, err := New(Params[int, string]{
				Context: testContext(t),
				Log:     zerolog.New(logs),
				Name:    "test_ttl_warning",
				LoadOneFunc: func(ID int) (*string, error) {
					return spreadTestValue(ID), nil
				},
				Timeouts:        tc.timeouts,
				AutomaticReload: tc.automaticReload,
			})
			require.NoError(t, err)

			warning := "TTL is shorter than the automatic reload interval"
			assert.Equal(t, tc.expectedWarning, strings.Contains(logs.String(), warning), "logs: %s", logs.String())
		})
	}
}
