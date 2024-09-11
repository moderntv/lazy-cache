package lazy

import (
	"errors"
	"time"
)

type Timeouts struct {
	// Entry TTL (time to live). When time of last load of the entry exceeds
	// this value entry is removed from cache.
	// Each time entry is reloaded and there was at least one access to the entry
	// data (e.g. `Get` function was called) since last reload, the TTL value is renewed.
	// If reload fails or entry data was not accessed, the TTL stays the same.
	// The TTL duration is being randomized by `Randomizer`.
	// TTL value should be at least twice bigger than `ReloadInterval` for optimal
	// cache self-maintenance.
	TTL time.Duration

	// TTL for entry which was not found in data storage (e.g. SQL database) or
	// should act like not found.
	// Also when first try to load entry from data storage fails (entry was not in cache)
	// this `NotFoundTTL` is used instead of `TTL` attribute.
	// The duration is being randomized by `Randomizer`.
	// If set to 0, not-found entries are not stored in cache.
	NotFoundTTL time.Duration

	// TTL for entry which first time load failed with an error (except NotFound).
	// Does not apply for reloads.
	// The duration is being randomized by `Randomizer`.
	// If set to 0, these entries are not stored in cache.
	ErrorTTL time.Duration

	// ReloadInterval specifies how often the entry should be reloaded or how long its data
	// are valid in cache.
	// The duration is being randomized by `Randomizer`.
	// When entry is being invalidated (by `Invalidate` function call), immediate reload
	// is triggered only when entry was accessed (via `Get` function) since last reload. If
	// entry was not accessed, the reload is postponed until `ReloadInterval` duration passes
	// (if `AutomaticReload` is enabled) or until `Get` function is called on the entry.
	ReloadInterval time.Duration

	// Randomizer specifies how much the timeouts/durations should be randomized.
	// value 0 means no randomization, 0.1 means 10% randomization, etc. Any value above 1
	// is treated as 1.
	// e.g. real entry TTL duration is set as `TTL` +/- `TTL` * `Randomizer`.
	// All durations are being randomized each time they are set.
	Randomizer float64

	// MemsizeUpdate specifies how often the cache should update its memory size.
	// Due to the fact that entries in cache can be added, removed or reloaded very often,
	// the cache memory size is recalculated in specified intervals.
	// If set to 0, memory size is not updated.
	MemsizeUpdate time.Duration
}

func (t *Timeouts) check() error {
	if t.TTL == 0 {
		return errors.New("TTL cannot be 0")
	}

	if t.ReloadInterval > t.TTL {
		return errors.New("ReloadInterval must be less than or equal to TTL")
	}

	if t.Randomizer < 0 {
		return errors.New("Randomizer cannot be negative")
	}
	if t.Randomizer > 1 {
		return errors.New("Randomizer cannot be greater than 1")
	}

	return nil
}
