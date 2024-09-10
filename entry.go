package lazy

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moderntv/lazy-cache/internal/utils"
)

type cachedEntry[T any] struct {
	nextReload atomic.Int64      // timestamp of next reload in milliseconds
	accessed   atomic.Bool       // true if entry data was accessed since last (re)load
	value      atomic.Pointer[T] // nil when not found
	mu         sync.Mutex
}

// set sets value and nextReload (when it make sense) and returns new TTL
// If TTL has negative value, it should be ignored (was not affected by this set)
func (e *cachedEntry[T]) set(value *T, err error, nowMillis int64, timeouts *Timeouts, init bool) (ttl time.Duration) {
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

func (e *cachedEntry[T]) get() *T {
	if !e.accessed.Load() {
		e.accessed.Store(true)
	}

	return e.value.Load()
}

// func (e *cachedEntry[T]) memSize() uint64 {
// 	value := e.value.Load()
// 	if value == nil {
// 		return 0
// 	}

// 	return memsize.Entry(value)
// }
