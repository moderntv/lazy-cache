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

func newSpreadTestCache(
	t *testing.T,
	timeouts Timeouts,
	automaticReload AutomaticReload,
	preload <-chan LoadedEntry[int, string],
) *Cache[int, string] {
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
		AutomaticReload: automaticReload,
	})
	require.NoError(t, err)

	return c
}

// spreadTestFirstLoads load all test entries at once into a new cache, each by
// another path which loads entries for the first time
var spreadTestFirstLoads = map[string]func(t *testing.T, timeouts Timeouts, automaticReload AutomaticReload) *Cache[int, string]{
	"preload": func(t *testing.T, timeouts Timeouts, automaticReload AutomaticReload) *Cache[int, string] {
		preload := make(chan LoadedEntry[int, string], spreadTestEntries)
		for ID := range spreadTestEntries {
			preload <- LoadedEntry[int, string]{ID: ID, Value: spreadTestValue(ID)}
		}
		close(preload)

		c := newSpreadTestCache(t, timeouts, automaticReload, preload)
		c.Ready()

		return c
	},
	"get": func(t *testing.T, timeouts Timeouts, automaticReload AutomaticReload) *Cache[int, string] {
		c := newSpreadTestCache(t, timeouts, automaticReload, nil)
		for ID := range spreadTestEntries {
			require.Equal(t, spreadTestValue(ID), c.Get(ID))
		}

		return c
	},
	"get_multiple": func(t *testing.T, timeouts Timeouts, automaticReload AutomaticReload) *Cache[int, string] {
		c := newSpreadTestCache(t, timeouts, automaticReload, nil)
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
	// spreading needs automatic reload; the intervals are long, so no
	// automatic reload runs during the test
	timeouts := cacheTestTimeouts
	timeouts.ReloadInterval = time.Hour
	timeouts.TTL = 3 * time.Hour
	reloadInterval := float64(timeouts.ReloadInterval.Milliseconds())
	ttl := float64(timeouts.TTL.Milliseconds())

	// half of next reloads are expected before split (part of ReloadInterval)
	tests := map[string]struct {
		randomizer float64
		split      float64
	}{
		"without_randomizer": {randomizer: 0, split: 0.5},
		"randomizer":         {randomizer: 0.5, split: 1},
	}

	for name, tc := range tests {
		timeouts.Randomizer = tc.randomizer

		// cache time starts at 0 when the cache is created; next reloads are
		// expected in [minNextReload, loadedAt + maxReloadIn) and some of them
		// from late, after the window [Randomizer * ReloadInterval,
		// (1 + Randomizer) * ReloadInterval) of the former uniform spread
		minNextReload := int64(reloadInterval * tc.randomizer)
		maxReloadIn := int64(reloadInterval * (1 + 2*tc.randomizer))
		split := int64(reloadInterval * tc.split)
		late := int64(reloadInterval * (1 + tc.randomizer))
		minTTL := int64(ttl * (1 - tc.randomizer))
		maxTTL := int64(ttl * (1 + tc.randomizer))

		for firstLoadName, firstLoad := range spreadTestFirstLoads {
			t.Run(name+"/"+firstLoadName, func(t *testing.T) {
				c := firstLoad(t, timeouts, AutomaticReloadAccessedEntries)
				loadedAt := c.now()

				reloadOutOfRange := 0
				ttlOutOfRange := 0
				expiresBeforeReload := 0
				firstHalf := 0
				secondHalf := 0
				lateReloads := 0

				for ID := range spreadTestEntries {
					nextReload, expiresAt := spreadTestEntryTimes(t, c, ID)

					if expiresAt < minTTL || expiresAt > loadedAt+maxTTL {
						ttlOutOfRange++
					}
					if expiresAt < nextReload {
						expiresBeforeReload++
					}
					if nextReload >= loadedAt+late {
						lateReloads++
					}

					if nextReload < minNextReload || nextReload >= loadedAt+maxReloadIn {
						reloadOutOfRange++
					} else if nextReload < split {
						firstHalf++
					} else {
						secondHalf++
					}
				}

				t.Logf("next reloads before %d: %d", split, firstHalf)
				t.Logf("next reloads from %d: %d", split, secondHalf)

				assert.Zero(t, reloadOutOfRange, "next reloads out of [%d, loadedAt + %d)", minNextReload, maxReloadIn)
				assert.Zero(t, ttlOutOfRange, "TTL evictions out of [%d, loadedAt + %d]", minTTL, maxTTL)
				assert.Zero(t, expiresBeforeReload, "TTL evictions before the first reload")
				assert.Greater(t, firstHalf, spreadTestEntries/4)
				assert.Greater(t, secondHalf, spreadTestEntries/4)
				if tc.randomizer > 0 {
					t.Logf("next reloads from loadedAt + %d: %d", late, lateReloads)
					assert.Positive(t, lateReloads, "no next reloads from loadedAt + %d", late)
				}
			})
		}
	}
}

// With DisableSpreadInitialReload, or without automatic reload, all entries
// loaded at once are reloaded after ReloadInterval
func TestCacheSpreadInitialReloadDisabled(t *testing.T) {
	tests := map[string]struct {
		disableSpread   bool
		automaticReload AutomaticReload
	}{
		"disabled":                  {disableSpread: true, automaticReload: AutomaticReloadAccessedEntries},
		"automatic_reload_disabled": {disableSpread: false, automaticReload: AutomaticReloadDisabled},
	}

	// cache time starts at 0 when the cache is created
	reloadInterval := cacheTestTimeouts.ReloadInterval.Milliseconds()
	ttl := cacheTestTimeouts.TTL.Milliseconds()

	for name, tc := range tests {
		timeouts := cacheTestTimeouts
		timeouts.DisableSpreadInitialReload = tc.disableSpread

		for firstLoadName, firstLoad := range spreadTestFirstLoads {
			t.Run(name+"/"+firstLoadName, func(t *testing.T) {
				c := firstLoad(t, timeouts, tc.automaticReload)
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
}

// Entries spread by the first load are reloaded on Get when their spread reload
// time passes and the reload plans the next one after the whole ReloadInterval
func TestCacheReloadAfterSpreadInitialReload(t *testing.T) {
	timeouts := cacheTestTimeouts
	timeouts.ReloadInterval = 300 * time.Millisecond
	reloadInterval := timeouts.ReloadInterval.Milliseconds()

	// automatic reload skips preloaded entries, they are not accessed
	c := spreadTestFirstLoads["preload"](t, timeouts, AutomaticReloadAccessedEntries)

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
