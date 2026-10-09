package lazy

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moderntv/lazy-cache/internal/utils"
)

type cachedEntry[K comparable, T any] struct {
	nextReload    atomic.Int64      // timestamp of next reload in milliseconds
	accessed      atomic.Bool       // true if entry data was accessed since last (re)load
	value         atomic.Pointer[T] // nil when not found
	invalidations atomic.Uint64     // number of Invalidate calls (detects invalidation during load)
	mu            sync.Mutex

	// scheduling of TTL eviction and automatic reload (protected by Cache.mu)
	id        K
	expiresAt int64 // timestamp of TTL eviction in milliseconds (noDeadline until first load finishes)
	reloadAt  int64 // timestamp of next automatic reload in milliseconds (noDeadline when not planned)
	heapIdx   int   // position in Cache.schedule, -1 when not scheduled
}

func newCachedEntry[K comparable, T any](id K) *cachedEntry[K, T] {
	return &cachedEntry[K, T]{
		id:        id,
		expiresAt: noDeadline,
		reloadAt:  noDeadline,
		heapIdx:   -1,
	}
}

// deadline returns timestamp of the nearest scheduled event (TTL eviction or
// automatic reload) of the entry in milliseconds
func (e *cachedEntry[K, T]) deadline() int64 {
	return min(e.expiresAt, e.reloadAt)
}

// set sets value and nextReload (when it make sense) and returns new TTL
// If TTL has negative value, it should be ignored (was not affected by this set)
func (e *cachedEntry[K, T]) set(value *T, err error, nowMillis int64, timeouts *Timeouts, init bool) (ttl time.Duration) {
	ttl = -1

	if err != nil {
		// skip any error except NotFound
		if !errors.Is(err, ErrNotFound) {
			// in case of first load, set error TTL
			if init {
				ttl = utils.RandomizeDuration(timeouts.ErrorTTL, timeouts.Randomizer)
			}

			goto end
		}

		// when record is not found, we want to keep this information in cache for desired time
		ttl = utils.RandomizeDuration(timeouts.NotFoundTTL, timeouts.Randomizer)
		if e.value.Load() != nil {
			e.value.Store(nil)
		}

		goto end
	}

	ttl = utils.RandomizeDuration(timeouts.TTL, timeouts.Randomizer)
	e.value.Store(value)

	// set `accessed` and `nextReload` every time and AFTER value is stored
	// (if they are set before `value`, cache can in some circumstances read old value
	// because it seems that entry value was updated)
end:
	if e.accessed.Load() {
		e.accessed.Store(false)
	}
	e.nextReload.Store(nowMillis + utils.RandomizeDuration(timeouts.ReloadInterval, timeouts.Randomizer).Milliseconds())

	return
}

// expireIfInvalidated expires the entry again when it was invalidated since
// `invalidations` were read before the load - the loaded data may be outdated.
// Invalidate increments the counter before it expires the entry, so the
// invalidation is either detected here or Invalidate expires the entry after
// it has been set.
func (e *cachedEntry[K, T]) expireIfInvalidated(invalidations uint64) {
	if e.invalidations.Load() != invalidations {
		e.nextReload.Store(0)
	}
}

func (e *cachedEntry[K, T]) get() *T {
	if !e.accessed.Load() {
		e.accessed.Store(true)
	}

	return e.value.Load()
}

// func (e *cachedEntry[K, T]) memSize() uint64 {
// 	value := e.value.Load()
// 	if value == nil {
// 		return 0
// 	}

// 	return memsize.Entry(value)
// }
