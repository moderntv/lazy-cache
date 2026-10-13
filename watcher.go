package lazy

import (
	"container/heap"
	"math"
	"runtime"
	"time"
)

const (
	noDeadline int64 = math.MaxInt64

	// watcherResolution is the minimal delay between two runs of the watcher.
	// Events which become due in the meantime are handled together in one run.
	// The watcher is woken up earlier only when an entry needs to be handled
	// sooner (e.g. invalidated entry should be reloaded immediately).
	watcherResolution = 100 * time.Millisecond
	// watcherIdleWait is used when nothing is scheduled. Any newly scheduled
	// entry wakes the watcher up, so the value only limits idle wake-ups.
	watcherIdleWait = time.Hour
	// watcherBatchSize limits how many due entries are handled under one
	// cache lock, so a mass expiration does not block the cache for long
	watcherBatchSize = 1000
)

// entrySchedule is a min-heap of entries ordered by their nearest deadline
// (TTL eviction or automatic reload).
//
// Invariant: an entry is in the schedule only while it is the entry stored in
// Cache.data for its ID. All changes of Cache.data go through storeEntryLocked
// and deleteEntryLocked which keep the schedule in sync, and only stored
// entries can be scheduled (see scheduleEntryLocked).
type entrySchedule[K comparable, T any] []*cachedEntry[K, T]

func (s entrySchedule[K, T]) Len() int { return len(s) }

func (s entrySchedule[K, T]) Less(i, j int) bool {
	return s[i].deadline() < s[j].deadline()
}

func (s entrySchedule[K, T]) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
	s[i].heapIdx = i
	s[j].heapIdx = j
}

func (s *entrySchedule[K, T]) Push(x any) {
	entry := x.(*cachedEntry[K, T])
	entry.heapIdx = len(*s)
	*s = append(*s, entry)
}

func (s *entrySchedule[K, T]) Pop() any {
	old := *s
	n := len(old)

	entry := old[n-1]
	old[n-1] = nil // avoid memory leak
	entry.heapIdx = -1

	*s = old[:n-1]

	return entry
}

// storeEntryLocked stores the entry into cache. The replaced entry (if any) is
// unscheduled. Cache.mu must be locked for writing.
func (c *Cache[K, T]) storeEntryLocked(entry *cachedEntry[K, T]) {
	old, exists := c.data[entry.id]
	if exists {
		c.unscheduleLocked(old)
	}

	c.data[entry.id] = entry

	if c.metrics != nil && !exists {
		c.metrics.ItemsCount.Inc()
	}
}

// deleteEntryLocked removes the entry from cache together with its schedule.
// Nothing happens when the entry is not stored in cache (anymore).
// Cache.mu must be locked for writing.
func (c *Cache[K, T]) deleteEntryLocked(entry *cachedEntry[K, T]) {
	if c.data[entry.id] != entry {
		return
	}

	delete(c.data, entry.id)
	c.unscheduleLocked(entry)

	if c.metrics != nil {
		c.metrics.ItemsCount.Dec()
	}
}

// scheduleEntry is scheduleEntryLocked for callers not holding Cache.mu
func (c *Cache[K, T]) scheduleEntry(entry *cachedEntry[K, T], ttl time.Duration, nowMillis int64) {
	c.mu.Lock()
	c.scheduleEntryLocked(entry, ttl, nowMillis)
	c.mu.Unlock()
}

// scheduleEntryLocked plans TTL eviction (when ttl is not negative) and the
// next automatic reload (when enabled) of the entry loaded at `nowMillis`.
// Entries which are not stored in cache (removed or replaced while being
// loaded) are ignored, so they cannot affect their successors.
// Cache.mu must be locked for writing.
func (c *Cache[K, T]) scheduleEntryLocked(entry *cachedEntry[K, T], ttl time.Duration, nowMillis int64) {
	if c.data[entry.id] != entry {
		return
	}

	scheduledAt := c.now()

	if ttl >= 0 {
		entry.expiresAt = scheduledAt + ttl.Milliseconds()
	}

	if c.automaticReloadType != AutomaticReloadDisabled {
		reloadIn := time.Duration(float64(entry.nextReload.Load()-nowMillis)*automaticReloadIntervalFraction) * time.Millisecond
		reloadIn = max(reloadIn, minAutomaticReloadDuration)
		entry.reloadAt = scheduledAt + reloadIn.Milliseconds()
	}

	c.rescheduleLocked(entry)
}

// rescheduleLocked updates the entry position in the schedule after its
// deadlines have changed and wakes the watcher up if the entry is due before
// the watcher's planned run. Cache.mu must be locked for writing.
func (c *Cache[K, T]) rescheduleLocked(entry *cachedEntry[K, T]) {
	c.fixScheduleLocked(entry)

	deadline := entry.deadline()
	if deadline >= c.watcherWakeAt {
		return
	}

	// the watcher recomputes its wake-up on the next run, lowering the value
	// here only suppresses redundant signals until then
	c.watcherWakeAt = deadline
	select {
	case c.watcherWake <- struct{}{}:
	default:
	}
}

// fixScheduleLocked updates the entry position in the schedule according to
// its deadline. Cache.mu must be locked for writing.
func (c *Cache[K, T]) fixScheduleLocked(entry *cachedEntry[K, T]) {
	switch {
	case entry.deadline() == noDeadline:
		c.unscheduleLocked(entry)
	case entry.heapIdx < 0:
		heap.Push(&c.schedule, entry)
	default:
		heap.Fix(&c.schedule, entry.heapIdx)
	}
}

// unscheduleLocked removes the entry from the schedule.
// Cache.mu must be locked for writing.
func (c *Cache[K, T]) unscheduleLocked(entry *cachedEntry[K, T]) {
	if entry.heapIdx >= 0 {
		heap.Remove(&c.schedule, entry.heapIdx)
	}
}

// startWatcher evicts entries with expired TTL and hands entries due for
// automatic reload over to the reload worker
func (c *Cache[K, T]) startWatcher() {
	timer := time.NewTimer(watcherIdleWait)
	defer timer.Stop()

	for {
		wait := c.processDueEntries()

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
		case <-c.watcherWake:
		}
	}
}

// processDueEntries evicts entries with expired TTL and queues entries due for
// automatic reload. Due entries are processed in batches, the cache is unlocked
// between them. Returns how long to wait before the next run.
func (c *Cache[K, T]) processDueEntries() time.Duration {
	for {
		processed := 0
		queued := false

		c.mu.Lock()

		nowMillis := c.now()
		for ; processed < watcherBatchSize && len(c.schedule) > 0; processed++ {
			entry := c.schedule[0]
			if entry.deadline() > nowMillis {
				break
			}

			if entry.expiresAt <= nowMillis {
				c.deleteEntryLocked(entry)
				continue
			}

			// automatic reload is due, the entry stays scheduled for TTL eviction
			// (the reload worker schedules the next reload)
			entry.reloadAt = noDeadline
			c.fixScheduleLocked(entry)
			c.reloadQueue = append(c.reloadQueue, entry)
			queued = true
		}

		next := noDeadline
		if len(c.schedule) > 0 {
			next = c.schedule[0].deadline()
		}

		done := next > nowMillis
		if done {
			c.watcherWakeAt = noDeadline
			if next != noDeadline {
				c.watcherWakeAt = max(next, nowMillis+watcherResolution.Milliseconds())
			}
		}

		c.mu.Unlock()

		if queued {
			select {
			case c.reloadWake <- struct{}{}:
			default:
			}
		}

		if done {
			if next == noDeadline {
				return watcherIdleWait
			}

			return time.Duration(max(next-nowMillis, watcherResolution.Milliseconds())) * time.Millisecond
		}

		// more entries are due, let others use the cache before the next batch
		runtime.Gosched()
	}
}

// startReloadWorker reloads entries queued by the watcher one by one. Reloads
// run outside the watcher, so slow loads do not delay TTL evictions.
func (c *Cache[K, T]) startReloadWorker() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.reloadWake:
		}

		for c.ctx.Err() == nil {
			entry := c.nextQueuedReload()
			if entry == nil {
				break
			}

			c.reloadEntry(entry)
		}
	}
}

// nextQueuedReload pops the next entry which still needs automatic reload from
// the reload queue. Returns nil when the queue is empty.
func (c *Cache[K, T]) nextQueuedReload() *cachedEntry[K, T] {
	c.mu.Lock()
	defer c.mu.Unlock()

	for len(c.reloadQueue) > 0 {
		entry := c.reloadQueue[0]
		c.reloadQueue[0] = nil // avoid memory leak
		c.reloadQueue = c.reloadQueue[1:]

		// skip entries removed from cache and entries which have been reloaded
		// (and so rescheduled) since they were queued
		if c.data[entry.id] == entry && entry.reloadAt == noDeadline {
			return entry
		}
	}

	c.reloadQueue = nil

	return nil
}

func (c *Cache[K, T]) reloadEntry(entry *cachedEntry[K, T]) {
	// prevent unnecessary reloads of entries that are not used
	// if entry is later accessed, it is lazy-reloaded
	if c.automaticReloadType == AutomaticReloadAccessedEntries && !entry.accessed.Load() {
		return
	}

	entry.mu.Lock()

	nowMillis := c.now()
	ttl, accessed, err := c.loadEntry(entry, nowMillis, false)
	if !accessed {
		ttl = -1 // do not prolong TTL for not accessed entries
	}

	c.scheduleEntry(entry, ttl, nowMillis)
	c.countLoad(true, err)
}
