package lazy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/moderntv/lazy-cache/internal/memsize"
	metrics_pkg "github.com/moderntv/lazy-cache/internal/metrics"
)

const (
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
	epoch               time.Time     // start of the cache time (see now)
	watcherWake         chan struct{} // wakes the watcher up before its planned run
	reloadWake          chan struct{} // notifies the reload worker about queued entries
	// dynamic attributes (not using mutex)
	memSizeValue atomic.Uint64
	// attributes protected by mutex
	mu            sync.RWMutex
	data          map[K]*cachedEntry[K, T]
	schedule      entrySchedule[K, T]  // TTL evictions and automatic reloads of entries in data
	reloadQueue   []*cachedEntry[K, T] // entries waiting for automatic reload
	watcherWakeAt int64                // planned run of the watcher (cache time in milliseconds)
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
		epoch:               time.Now(),
		watcherWake:         make(chan struct{}, 1),
		reloadWake:          make(chan struct{}, 1),
		data:                make(map[K]*cachedEntry[K, T]),
		watcherWakeAt:       noDeadline,
	}

	if params.PreloadChan != nil {
		c.preloadWG = &sync.WaitGroup{}
		c.preloadWG.Add(1)
		go c.startPreloading(params.PreloadChan)
	} else {
		c.log.Debug().Msg("preloading disabled")
	}

	go c.startWatcher()

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

		go c.startReloadWorker()

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

// now returns the cache time in milliseconds. It is measured by the monotonic
// clock since the cache was created, so unlike the wall clock it is not
// affected by system time changes. All timestamps of entries use this time.
func (c *Cache[K, T]) now() int64 {
	return time.Since(c.epoch).Milliseconds()
}

// Get returns nil when the entry is not found or its load has failed
func (c *Cache[K, T]) Get(ID K) *T {
	return c.lookup(ID).get()
}

// GetWithError is Get that tells why there is no value: ErrNotFound, or the
// error of the failed load (kept for ErrorTTL). A value kept after a failed
// reload comes without an error.
func (c *Cache[K, T]) GetWithError(ID K) (*T, error) {
	return c.lookup(ID).getWithError()
}

// lookup returns the entry of ID, loaded or reloaded when needed; with zero
// TTL of the load result the entry is not stored in cache. Concurrent lookups
// of one ID load (or reload) it only once, the others wait for that load.
func (c *Cache[K, T]) lookup(ID K) *cachedEntry[K, T] {
	c.mu.RLock()
	entry, exists := c.data[ID]
	c.mu.RUnlock()

	if c.metrics != nil {
		c.metrics.ReadsCount.Inc()
	}

	nowMillis := c.now()

	// entry found in cache
	if exists {
		return c.reloadIfExpired(entry, nowMillis)
	}

	// not found in cache
	c.mu.Lock()
	// other routine may have stored the entry since the read lock was released,
	// then wait for its load instead of loading the entry again
	entry, exists = c.data[ID]
	if exists {
		c.mu.Unlock()

		return c.reloadIfExpired(entry, nowMillis)
	}
	// the entry is locked before it is visible to other routines, so they wait
	// for its load
	entry = newCachedEntry[K, T](ID)
	entry.mu.Lock()
	c.storeEntryLocked(entry)
	c.mu.Unlock()

	ttl, _, err := c.loadEntry(entry, nowMillis, true)

	// do not store into cache when TTL is 0
	if ttl == 0 {
		c.mu.Lock()
		c.deleteEntryLocked(entry)
		c.mu.Unlock()

		return entry
	}

	c.scheduleEntry(entry, ttl, nowMillis)
	c.countLoad(false, err)

	return entry
}

// reloadIfExpired returns the entry found in cache, reloaded when its data are
// expired. Concurrent callers wait for one reload (or the first load still in
// progress) instead of reloading the entry again.
func (c *Cache[K, T]) reloadIfExpired(entry *cachedEntry[K, T], nowMillis int64) *cachedEntry[K, T] {
	// valid value
	if nowMillis < entry.nextReload.Load() {
		return entry
	}

	// data are expired, check if entry is being reloaded
	entry.mu.Lock()

	// check if entry was loaded by other routine during waiting for lock
	if nowMillis < entry.nextReload.Load() {
		entry.mu.Unlock()

		return entry
	}

	// reload entry
	ttl, _, err := c.loadEntry(entry, nowMillis, false)

	c.scheduleEntry(entry, ttl, nowMillis)
	c.countLoad(false, err)

	return entry
}

// loadEntry loads data of the entry locked by the caller and unlocks it.
// Returns TTL of the loaded data (see cachedEntry.set), whether the entry data
// were accessed since the previous load and the load error.
//
// When LoadOneFunc panics, the entry is unlocked too, so routines waiting for
// it do not block forever, and the panic is passed on. The entry of the first
// load has no data then, it is removed from cache (it would stay there without
// TTL otherwise).
func (c *Cache[K, T]) loadEntry(entry *cachedEntry[K, T], nowMillis int64, init bool) (ttl time.Duration, accessed bool, err error) {
	loaded := false
	defer func() {
		if !loaded && init {
			c.mu.Lock()
			c.deleteEntryLocked(entry)
			c.mu.Unlock()
		}

		entry.mu.Unlock()
	}()

	invalidations := entry.invalidations.Load()
	loadedValue, err := c.loadOneFunc(entry.id)
	loaded = true
	accessed = entry.accessed.Load()
	ttl = entry.set(loadedValue, err, nowMillis, &c.timeouts, init)
	entry.expireIfInvalidated(invalidations)

	return ttl, accessed, err
}

// countLoad updates metrics after a load of one entry, lazy (by Get) or
// automatic (by the reload worker)
func (c *Cache[K, T]) countLoad(automatic bool, err error) {
	if c.metrics == nil {
		return
	}

	if automatic {
		c.metrics.AutomaticLoadCount.Inc()
	} else {
		c.metrics.LazyLoadCount.Inc()
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.metrics.ErrorLoadCount.Inc()
	}
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

	nowMillis := c.now()

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
	nowMillis := c.now()
	if nowMillis >= entry.nextReload.Load() {
		return nil, false
	}

	return entry.get(), true
}

func (c *Cache[K, T]) Remove(ID K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.data[ID]
	if exists {
		c.deleteEntryLocked(entry)
	}
}

func (c *Cache[K, T]) Invalidate(ID K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.data[ID]
	if !exists {
		return
	}

	// the counter has to be incremented before the entry is expired (see
	// cachedEntry.expireIfInvalidated)
	entry.invalidations.Add(1)
	entry.nextReload.Store(0)

	if c.automaticReloadType != AutomaticReloadDisabled {
		// reload in background as soon as possible
		entry.reloadAt = c.now()
		c.rescheduleLocked(entry)
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

			c.addLoadedEntry(loadedEntry, c.now())

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
	entry := newCachedEntry[K, T](loadedEntry.ID)
	ttl := entry.set(loadedEntry.Value, loadedEntry.Err, nowMillis, &c.timeouts, true)

	loadFailed := loadedEntry.Err != nil && !errors.Is(loadedEntry.Err, ErrNotFound)
	if c.metrics != nil && loadFailed {
		c.metrics.ErrorLoadCount.Inc()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	_, exists := c.data[entry.id]
	// do not override existing entry in case of error (except NotFound)
	if exists && loadFailed {
		return
	}

	c.storeEntryLocked(entry)
	c.scheduleEntryLocked(entry, ttl, nowMillis)
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
	entries := make([]*cachedEntry[K, T], 0, len(c.data))
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
