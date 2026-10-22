package lazy

import (
	"errors"
	"time"
)

type Timeouts struct {
	// Entry TTL (time to live). When time of last load of the entry exceeds
	// this value entry is removed from cache.
	// Each time entry is reloaded and there was at least one access to the
	// entry data (e.g. `Get` function was called) since last reload, the TTL
	// value is renewed.
	// If reload fails or entry data was not accessed, the TTL stays the same.
	// The TTL duration is being randomized by `Randomizer`.
	// TTL value should be at least twice bigger than `ReloadInterval` for
	// optimal cache self-maintenance. With automatic reload, accessed entries
	// can expire before their reload unless (1 - `Randomizer`) * `TTL` is at
	// least 0.9 * (1 + `Randomizer`) * `ReloadInterval` (`New` logs a warning
	// otherwise), e.g. `TTL` >= 2.7 * `ReloadInterval` for `Randomizer` 0.5.
	TTL time.Duration `mapstructure:"ttl"`

	// TTL for entry which was not found in data storage (e.g. SQL database)
	// or should act like not found.
	// Also when first try to load entry from data storage fails (entry was
	// not in cache) this `NotFoundTTL` is used instead of `TTL` attribute.
	// The duration is being randomized by `Randomizer`.
	// If set to 0, not-found entries are not stored in cache.
	NotFoundTTL time.Duration `mapstructure:"not_found_ttl"`

	// TTL for entry which first time load failed with an error (except
	// NotFound).
	// Does not apply for reloads.
	// The duration is being randomized by `Randomizer`.
	// If set to 0, these entries are not stored in cache.
	ErrorTTL time.Duration `mapstructure:"error_ttl"`

	// ReloadInterval specifies how often the entry should be reloaded or how
	// long its data are valid in cache.
	// The duration is being randomized by `Randomizer`. With automatic
	// reload, the first reload of the entry is spread, so data of the first
	// load can be kept up to
	// (1 + 2 * `Randomizer`) * `ReloadInterval` (see
	// `DisableSpreadInitialReload`).
	// With automatic reload, entries are reloaded at 0.9 of the planned time.
	// When entry is being invalidated (by `Invalidate` function call),
	// immediate reload is triggered (in background) only when entry was
	// accessed (via `Get` function) since last reload. If entry was not
	// accessed, the reload is postponed until `ReloadInterval` duration passes
	// (if `AutomaticReload` is enabled) or until `Get` function is called on
	// the entry.
	ReloadInterval time.Duration `mapstructure:"reload_interval"`

	// DisableSpreadInitialReload disables spreading of the first reloads.
	// Spreading applies only with automatic reload; with
	// `AutomaticReloadDisabled` it is always disabled, because entries are
	// reloaded on access and spreading would only make their first reloads
	// (synchronous in `Get`) come earlier.
	// By default, the first reload after the first successful load of an
	// entry (preloading, `Get` of an entry missing in cache, `GetMultiple`)
	// is planned after `Randomizer` * `ReloadInterval` plus the random time
	// to the next reload of an entry kept in cache for a long time.
	// Entries loaded at once (e.g. by preloading) are thus not reloaded for
	// 0.9 * `Randomizer` * `ReloadInterval` (automatic reload runs at 0.9 of
	// the planned time), so just loaded data are not reloaded right away,
	// and then at a constant rate, the same as in a long running cache,
	// instead of in waves (e.g. all of them waiting the whole
	// `ReloadInterval` and then being reloaded at once).
	// The first reload is planned uniformly up to `ReloadInterval`, the
	// remaining `Randomizer` part of entries less and less likely up to
	// (1 + 2 * `Randomizer`) * `ReloadInterval`, which is
	// `Randomizer` * `ReloadInterval` later than the latest reload without
	// spreading.
	// All following reloads are planned after `ReloadInterval` randomized by
	// `Randomizer`, so the average reload interval is not shortened.
	// TTL which would end before the spread first reload is extended to it,
	// so the entry is not evicted before being reloaded. First loads which
	// fail or end with NotFound are not spread.
	// Data of the first load are valid (e.g. for `GetCached`) only until
	// the spread reload time, which can be as soon as
	// `Randomizer` * `ReloadInterval` (or right away with zero `Randomizer`).
	// When set to true, also the first reload is planned after
	// `ReloadInterval` randomized by `Randomizer`.
	DisableSpreadInitialReload bool `mapstructure:"disable_spread_initial_reload"`

	// Randomizer specifies how much the timeouts/durations should be
	// randomized. Value 0 means no randomization, 0.1 means 10% randomization,
	// etc. Any value above 1 is treated as 1.
	// e.g. real entry TTL duration is set as `TTL` +/- `TTL` * `Randomizer`.
	// All durations are being randomized each time they are set.
	Randomizer float64 `mapstructure:"randomizer"`

	// MemsizeUpdate specifies how often the cache should update its memory
	// size.
	// Due to the fact that entries in cache can be added, removed or reloaded
	// very often, the cache memory size is recalculated in specified intervals.
	// If set to 0, memory size is not updated.
	MemsizeUpdate time.Duration `mapstructure:"memsize_update"`
}

func (t *Timeouts) check() error {
	if t.TTL == 0 {
		return errors.New("TTL cannot be 0")
	}

	if t.ReloadInterval > t.TTL {
		return errors.New("reloadInterval must be less than or equal to TTL")
	}

	if t.Randomizer < 0 {
		return errors.New("randomizer cannot be negative")
	}
	if t.Randomizer > 1 {
		return errors.New("randomizer cannot be greater than 1")
	}

	return nil
}
