package lazy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moderntv/deathrow"
	"github.com/rs/zerolog"

	"github.com/moderntv/lazy-cache/internal/memsize"
	metrics_pkg "github.com/moderntv/lazy-cache/internal/metrics"
)

const (
	tllWatcherInterval                      = 100 * time.Millisecond
	reloadWatcherInterval                   = 100 * time.Millisecond
	automaticReloadIntervalFraction float64 = 0.9 // if automatic reload is enabled, the next reload is performed at 90% time of data expiration
	minAutomaticReloadDuration              = 100 * time.Millisecond
)

type Cache[K comparable, T any] struct {
	// static attributes (does not change its value after initialization)
	ctx                 context.Context
	log                 zerolog.Logger
	metrics             *metrics_pkg.Metrics
	name                string
	timeouts            Timeouts
	loadOneFunc         LoadOneFunc[K, T]
	loadMultipleFunc    LoadMultipleFunc[K, T]
	automaticReloadType AutomaticReload
	ttlWatcher          *deathrow.Prison[K]
	reloadWatcher       *deathrow.Prison[K]
	// dynamic attributes (not using mutex)
	memSizeValue atomic.Uint64
	// attributes protected by mutex
	mu   sync.RWMutex
	data map[K]*cachedEntry[T]
	// preloading synchronization
	preloadWG *sync.WaitGroup
}

func New[K comparable, T any](params Params[K, T]) (c *Cache[K, T], err error) {
	err = params.check()
	if err != nil {
		return
	}

	var metrics *metrics_pkg.Metrics
	if params.MetricsRegistry != nil {
		metrics, err = metrics_pkg.New(params.Name, params.MetricsRegistry)
		if err != nil {
			return
		}
	}

	log := params.Log.With().Str("cache", params.Name).Logger()

	c = &Cache[K, T]{
		ctx:                 params.Context,
		log:                 log,
		metrics:             metrics,
		name:                params.Name,
		timeouts:            params.Timeouts,
		loadOneFunc:         params.LoadOneFunc,
		loadMultipleFunc:    params.LoadMultipleFunc,
		automaticReloadType: params.AutomaticReload,
		ttlWatcher:          deathrow.NewPrison[K](),
		reloadWatcher:       deathrow.NewPrison[K](),
		data:                make(map[K]*cachedEntry[T]),
	}

	if params.PreloadChan != nil {
		c.preloadWG = &sync.WaitGroup{}
		c.preloadWG.Add(1)
		go c.startPreloading(params.PreloadChan)
	} else {
		c.log.Debug().Msg("preloading disabled")
	}

	go c.startTTLWatcher()

	if c.automaticReloadType != AutomaticReloadDisabled {
		realMinReloadInterval := time.Duration(
			float64(params.Timeouts.ReloadInterval.Milliseconds())*
				(1-params.Timeouts.Randomizer)*
				automaticReloadIntervalFraction) *
			time.Millisecond
		if realMinReloadInterval < minAutomaticReloadDuration {
			c.log.Warn().
				Dur("givenMinInterval", realMinReloadInterval).
				Dur("defaultMinInterval", minAutomaticReloadDuration).
				Msg("combination of automatic reload interval is too short, setting to minimum default value")
		}

		go c.startReloadWatcher()

	} else {
		c.log.Debug().Msg("automatic reload disabled")
	}

	if c.metrics != nil && params.Timeouts.MemsizeUpdate > 0 {
		c.log.Debug().Msg("memory size calculation enabled")
		go c.startMemoryMeassurement(params.Timeouts.MemsizeUpdate)
	} else {
		c.log.Debug().Msg("memory size calculation disabled")
	}

	return
}

func (c *Cache[K, T]) Get(ID K) *T {
	c.mu.RLock()
	entry, exists := c.data[ID]
	c.mu.RUnlock()

	if c.metrics != nil {
		c.metrics.ReadsCount.Inc()
	}

	nowMillis := time.Now().UnixMilli()

	// entry found in cache
	if exists {
		// valid value
		if nowMillis < entry.nextReload.Load() {
			return entry.get()
		}

		// data are expired, check if entry is being reloaded
		entry.mu.Lock()

		// check if entry was loaded by other routine during waiting for lock
		if nowMillis < entry.nextReload.Load() {
			entry.mu.Unlock()

			return entry.get()
		}

		// reload entry
		loadedValue, err := c.loadOneFunc(ID)
		ttl := entry.set(loadedValue, err, nowMillis, &c.timeouts, false)

		entry.mu.Unlock()

		// update watchers
		c.setEntryWatchers(ID, ttl, entry, nowMillis)

		if c.metrics != nil {
			c.metrics.LazyLoadCount.Inc()
			if err != nil && !errors.Is(err, ErrNotFound) {
				c.metrics.ErrorLoadCount.Inc()
			}
		}

		return entry.get()
	}

	// not found in cache
	entry = &cachedEntry[T]{}
	entry.mu.Lock()

	c.mu.Lock()
	c.data[ID] = entry
	c.mu.Unlock()

	loadedValue, err := c.loadOneFunc(ID)
	ttl := entry.set(loadedValue, err, nowMillis, &c.timeouts, true)

	entry.mu.Unlock()

	// do not store into cache when TTL is 0
	if ttl == 0 {
		c.mu.Lock()
		delete(c.data, ID)
		c.mu.Unlock()

		return entry.value.Load()
	}

	// update watchers
	c.setEntryWatchers(ID, ttl, entry, nowMillis)

	if c.metrics != nil {
		c.metrics.ItemsCount.Inc()
		c.metrics.LazyLoadCount.Inc()
		if err != nil && !errors.Is(err, ErrNotFound) {
			c.metrics.ErrorLoadCount.Inc()
		}
	}

	return entry.get()
}

// GetMultiple returns values for the given IDs. Entries which are still valid
// in cache are served from cache, all the remaining IDs are loaded by
// LoadMultipleFunc in a single call. Duplicate IDs on the input are merged.
//
// Keys missing in the returned map mean "not found" (or the load has failed) -
// the map never contains a nil value.
//
// The loader does not have to return an entry for every requested ID. IDs
// which are missing in its result are stored into cache as not-found, so the
// following GetMultiple call does not ask the loader for them again.
//
// If LoadMultipleFunc is not set, GetMultiple degrades to calling Get for each
// ID.
//
// Unlike Get, GetMultiple does NOT do single-flight. Two concurrent batches
// sharing an ID - or a batch concurrent with Get of the same ID - load that
// entry more than once. Both loads store the same value, so the only cost is
// a redundant load; preloading behaves the same way.
func (c *Cache[K, T]) GetMultiple(ids []K) (values map[K]*T) {
	values = make(map[K]*T, len(ids))

	if len(ids) == 0 {
		return
	}

	// deduplicate input IDs, keep their original order
	uniqueIDs := make(map[K]struct{}, len(ids))
	for _, id := range ids {
		uniqueIDs[id] = struct{}{}
	}

	// no batch loader available, fall back to loading entries one by one
	// (Get maintains its own metrics)
	if c.loadMultipleFunc == nil {
		for id := range uniqueIDs {
			value := c.Get(id)
			if value != nil {
				values[id] = value
			}
		}

		return
	}

	if c.metrics != nil {
		c.metrics.ReadsCount.Add(float64(len(ids)))
	}

	nowMillis := time.Now().UnixMilli()

	// serve what is valid in cache, collect the rest for the batch load
	toLoad := make([]K, 0, len(uniqueIDs))

	c.mu.RLock()
	for id := range uniqueIDs {
		entry, exists := c.data[id]
		if !exists || nowMillis >= entry.nextReload.Load() {
			toLoad = append(toLoad, id)
			continue
		}

		value := entry.get()
		if value != nil {
			values[id] = value
		}
	}
	c.mu.RUnlock()

	if len(toLoad) == 0 {
		return
	}

	if c.metrics != nil {
		c.metrics.BatchLoadCount.Inc()
		c.metrics.BatchLoadItemsCount.Add(float64(len(toLoad)))
	}

	loadedEntries := c.loadMultipleFunc(toLoad)

	loadedIDs := make(map[K]struct{}, len(loadedEntries))
	for _, loadedEntry := range loadedEntries {
		loadedIDs[loadedEntry.ID] = struct{}{}

		c.addLoadedEntry(loadedEntry, nowMillis)

		if loadedEntry.Err != nil || loadedEntry.Value == nil {
			continue
		}
		_, requested := uniqueIDs[loadedEntry.ID]
		if requested {
			values[loadedEntry.ID] = loadedEntry.Value
		}
	}

	// IDs the loader did not return are cached as not-found, otherwise every
	// following GetMultiple would ask the loader for them again
	for _, ID := range toLoad {
		_, loaded := loadedIDs[ID]
		if loaded {
			continue
		}

		c.addLoadedEntry(LoadedEntry[K, T]{ID: ID, Err: ErrNotFound}, nowMillis)
	}

	return
}

// GetCached returns value and exists flag directly from cache
// without any lazy loading or reloading
func (c *Cache[K, T]) GetCached(ID K) (value *T, exists bool) {
	c.mu.RLock()
	entry, exists := c.data[ID]
	c.mu.RUnlock()

	if c.metrics != nil {
		c.metrics.ReadsCount.Inc()
	}

	if !exists {
		return nil, false
	}

	// check if entry is still valid (not expired)
	nowMillis := time.Now().UnixMilli()
	if nowMillis >= entry.nextReload.Load() {
		return nil, false
	}

	return entry.get(), true
}

func (c *Cache[K, T]) Remove(ID K) {
	c.mu.Lock()

	_, exists := c.data[ID]
	if !exists {
		c.mu.Unlock()
		return
	}
	delete(c.data, ID)

	c.mu.Unlock()

	// remove watchers
	c.ttlWatcher.Drop(ID)
	c.reloadWatcher.Drop(ID)

	if c.metrics != nil {
		c.metrics.ItemsCount.Dec()
	}
}

func (c *Cache[K, T]) Invalidate(ID K) {
	c.mu.RLock()
	entry, exists := c.data[ID]
	c.mu.RUnlock()

	if !exists {
		return
	}

	entry.nextReload.Store(0)

	if c.automaticReloadType != AutomaticReloadDisabled {
		c.reloadWatcher.Push(ID, 0)
	}
}

func (c *Cache[K, T]) startPreloading(preloadChan <-chan LoadedEntry[K, T]) {
	defer c.preloadWG.Done()

	// read data from reload channel and store it to cache
	for {
		select {
		case loadedEntry, more := <-preloadChan:
			if !more {
				return
			}

			c.addLoadedEntry(loadedEntry, time.Now().UnixMilli())

		case <-c.ctx.Done():
			return
		}
	}
}

// Ready blocks until preloading is complete. If preloading is disabled,
// it returns immediately.
func (c *Cache[K, T]) Ready() {
	if c.preloadWG != nil {
		c.preloadWG.Wait()
	}
}

// Lock locks the cache's internal mutex for exclusive write access.
// The caller must call Unlock when done. This allows external code to
// perform atomic operations on the cache data (modifying them).
// During the lock all following functions will be blocked: Get,
// GetMultiple, GetCached, Remove, Invalidate.
func (c *Cache[K, T]) Lock() {
	c.mu.Lock()
}

// Unlock unlocks the cache's internal mutex. It must be called after Lock
// to enable using the cache again.
func (c *Cache[K, T]) Unlock() {
	c.mu.Unlock()
}

// addLoadedEntry adds already loaded entry to cache (if it makes sense)
func (c *Cache[K, T]) addLoadedEntry(loadedEntry LoadedEntry[K, T], nowMillis int64) {
	entry := &cachedEntry[T]{}
	ttl := entry.set(loadedEntry.Value, loadedEntry.Err, nowMillis, &c.timeouts, true)

	id := loadedEntry.ID

	loadFailed := loadedEntry.Err != nil && !errors.Is(loadedEntry.Err, ErrNotFound)
	if c.metrics != nil && loadFailed {
		c.metrics.ErrorLoadCount.Inc()
	}

	c.mu.Lock()

	_, exists := c.data[id]
	// do not override existing entry in case of error (except NotFound)
	if exists && loadFailed {
		c.mu.Unlock()

		return
	}

	c.data[id] = entry

	c.mu.Unlock()

	// update TTL watcher
	c.setEntryWatchers(id, ttl, entry, nowMillis)

	if c.metrics != nil {
		if !exists {
			c.metrics.ItemsCount.Inc()
		}
	}
}

func (c *Cache[K, T]) startTTLWatcher() {
	ch := c.ttlWatcher.PopperWithResolution(c.ctx, tllWatcherInterval)

	// read data from TTL watcher and remove expired entries from cache
	// channel is closed when context is done
	for {
		item, more := <-ch
		if !more {
			break
		}

		ID := item.ID()

		c.mu.Lock()

		_, exists := c.data[ID]
		if !exists {
			c.mu.Unlock()
			continue
		}

		delete(c.data, ID)

		c.mu.Unlock()

		// remove from TTL watcher
		c.reloadWatcher.Drop(ID)

		if c.metrics != nil {
			c.metrics.ItemsCount.Dec()
		}
	}
}

func (c *Cache[K, T]) startReloadWatcher() {
	ch := c.reloadWatcher.PopperWithResolution(c.ctx, reloadWatcherInterval)

	// read data from reload watcher and reload expired entries
	// channel is closed when context is done
	for {
		item, more := <-ch
		if !more {
			break
		}

		id := item.ID()

		c.mu.Lock()
		entry, exists := c.data[id]
		c.mu.Unlock()

		if !exists {
			continue
		}

		// prevent unnecessary reloads of entries that are not used
		// if entry is later accessed, it is lazy-reloaded
		if c.automaticReloadType == AutomaticReloadAccessedEntries && !entry.accessed.Load() {
			continue
		}

		entry.mu.Lock()

		nowMillis := time.Now().UnixMilli()
		loadedValue, err := c.loadOneFunc(id)
		accessed := entry.accessed.Load()
		ttl := entry.set(loadedValue, err, nowMillis, &c.timeouts, false)
		if !accessed {
			ttl = -1 // do not prolong TTL for not accessed entries
		}

		entry.mu.Unlock()

		// update watchers
		c.setEntryWatchers(id, ttl, entry, nowMillis)

		if c.metrics != nil {
			c.metrics.AutomaticLoadCount.Inc()
			if err != nil && !errors.Is(err, ErrNotFound) {
				c.metrics.ErrorLoadCount.Inc()
			}
		}
	}
}

func (c *Cache[K, T]) setEntryWatchers(
	entryID K,
	ttl time.Duration,
	entry *cachedEntry[T],
	nowMillis int64,
) {
	if ttl >= 0 {
		c.ttlWatcher.Push(entryID, ttl)
	}

	if c.automaticReloadType == AutomaticReloadDisabled {
		return
	}

	nextAutomaticReloadDuration := time.Duration(float64(entry.nextReload.Load()-nowMillis)*automaticReloadIntervalFraction) * time.Millisecond
	if nextAutomaticReloadDuration < minAutomaticReloadDuration {
		nextAutomaticReloadDuration = minAutomaticReloadDuration
	}
	c.reloadWatcher.Push(entryID, nextAutomaticReloadDuration)
}

func (c *Cache[K, T]) startMemoryMeassurement(interval time.Duration) {
	for {
		timer := time.NewTimer(interval)

		select {
		case <-c.ctx.Done():
			timer.Stop()
			return

		case <-timer.C:
			c.updateMemsize()
		}
	}
}

func (c *Cache[K, T]) updateMemsize() {
	// handle potential panic (calculating size should not affect running app)
	defer func() {
		err := recover()
		if err != nil {
			c.log.Warn().
				Interface("err", err).
				Msg("panic occurred during cache size calculation")
		}
	}()

	// get list of entries using read lock
	c.mu.RLock()
	entries := make([]*cachedEntry[T], 0, len(c.data))
	for id := range c.data {
		entries = append(entries, c.data[id])
	}
	c.mu.RUnlock()

	// get memory size of each entry
	var size uint64
	for _, entry := range entries {
		value := entry.value.Load()
		if value == nil {
			continue
		}

		size += memsize.Entry(value)
	}

	c.memSizeValue.Store(size)
	c.metrics.MemoryUsage.Set(float64(size))
}
