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

type Cache[K comparable, T any] struct {
	// static attributes (does not change its value after initialization)
	ctx              context.Context
	log              zerolog.Logger
	metrics          *metrics_pkg.Metrics
	name             string
	timeouts         Timeouts
	loadOneFunc      LoadOneFunc[K, T]
	loadMultipleFunc LoadMultipleFunc[K, T]
	automaticReload  AutomaticReload
	ttlWatcher       *deathrow.Prison[K]
	reloadWatcher    *deathrow.Prison[K]
	// dynamic attributes (not using mutex)
	memSizeValue atomic.Uint64
	// attributes protected by mutex
	mu   sync.RWMutex
	data map[K]*cachedEntry[T]
}

func NewCache[K comparable, T any](params Params[K, T]) (c *Cache[K, T], err error) {
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

	// TODO: params.MemorySize

	log := params.Log.With().Str("cache", params.Name).Logger()

	c = &Cache[K, T]{
		ctx:              params.Context,
		log:              log,
		metrics:          metrics,
		name:             params.Name,
		timeouts:         params.Timeouts,
		loadOneFunc:      params.LoadOneFunc,
		loadMultipleFunc: params.LoadMultipleFunc,
		automaticReload:  params.AutomaticReload,
		ttlWatcher:       deathrow.NewPrison[K](),
		reloadWatcher:    deathrow.NewPrison[K](),
		data:             make(map[K]*cachedEntry[T]),
	}

	if params.PreloadChan != nil {
		go c.startPreloading(params.PreloadChan)
	} else {
		c.log.Info().Msg("preloading disabled")
	}

	go c.startTTLWatcher()

	if c.automaticReload != AutomaticReloadDisabled {
		go c.startReloadWatcher()
	} else {
		c.log.Info().Msg("automatic reload disabled")
	}

	if c.metrics != nil && params.Timeouts.MemsizeUpdate > 0 {
		go c.startMemoryMeassurement(params.Timeouts.MemsizeUpdate)
	} else {
		c.log.Info().Msg("memory size calculation is disabled")
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

	if c.automaticReload != AutomaticReloadDisabled {
		c.reloadWatcher.Push(ID, 0)
	}
}

// func (c *Cache[K, T]) IsCached(ID K) bool {
// 	return false
// }

func (c *Cache[K, T]) startPreloading(preloadChan <-chan LoadedEntry[K, T]) {
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

// addLoadedEntry adds already loaded entry to cache (if it makes sense)
func (c *Cache[K, T]) addLoadedEntry(loadedEntry LoadedEntry[K, T], nowMillis int64) {
	entry := &cachedEntry[T]{}
	ttl := entry.set(loadedEntry.Value, loadedEntry.Err, nowMillis, &c.timeouts, true)

	ID := loadedEntry.ID

	c.mu.Lock()

	_, exists := c.data[ID]
	// do not override existing entry in case of error (except NotFound)
	if exists && loadedEntry.Err != nil && !errors.Is(loadedEntry.Err, ErrNotFound) {
		c.mu.Unlock()

		if c.metrics != nil {
			c.metrics.ErrorLoadCount.Inc()
		}

		return
	}

	c.data[ID] = entry

	c.mu.Unlock()

	// update TTL watcher
	c.setEntryWatchers(ID, ttl, entry, nowMillis)

	if c.metrics != nil {
		c.metrics.ItemsCount.Inc()
		// c.memSizeValue.Add(entry.memSize())
	}
}

func (c *Cache[K, T]) startTTLWatcher() {
	ch := c.ttlWatcher.Popper(c.ctx)

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
	ch := c.reloadWatcher.Popper(c.ctx)

	// read data from TTL watcher and remove expired entries from cache
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
		if c.automaticReload == AutomaticReloadAccessedEntries && !entry.accessed.Load() {
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

	if c.automaticReload == AutomaticReloadDisabled {
		return
	}

	c.reloadWatcher.Push(entryID, time.Duration(entry.nextReload.Load()-nowMillis)*time.Millisecond)
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
