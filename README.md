# Lazy cache

Lazy cache is an **on-demand**, per-entry cache: items are loaded when first requested and can be added, removed, or invalidated individually. Each entry has its own TTL and reload interval. Entries are evicted when their TTL expires; they can also be removed explicitly or invalidated for reload.

It supports optional preloading via a channel, automatic background reload of expired entries (all or only those accessed since last reload), and memory-size metrics.

Provided functions:

-   **Get(ID)** — returns `*T` for key `K`. If the entry is in cache and valid, it returns it; if expired or missing, it loads via `LoadOneFunc` (and may store not-found or error for `NotFoundTTL`/`ErrorTTL`). Returns `nil` when the entry is not found (or on certain errors, depending on `LoadOneFunc`).
-   **GetCached(ID)** — returns `(value *T, exists bool)` from cache only; no lazy load or reload. `exists` is false if the key is missing or the entry has expired.
-   **Remove(ID)** — deletes the entry from the cache and clears its TTL/reload watchers.
-   **Invalidate(ID)** — marks the entry as expired; the next `Get` will reload it, or it will be reloaded by the automatic reload watcher (if enabled and, for `AutomaticReloadAccessedEntries`, if it was accessed).
-   **ForceSet(ID, value, err)** — sets the value (or not-found/error) in the cache directly, bypassing `LoadOneFunc`. Uses normal TTL and `ReloadInterval` for the entry.
-   **Ready()** — blocks until preloading is done. If `PreloadChan` is nil, returns immediately.
-   **Lock() / Unlock()** — lock the cache mutex for exclusive access. While locked, `Get`, `GetCached`, `Remove`, `Invalidate`, and `ForceSet` block. Use for atomic multi-entry updates.

The cache uses Go generics: key type `K` must be `comparable`, value type `T` is arbitrary.

## Lifecycle

`New(...)` does not perform an initial full load. With `PreloadChan != nil`, a goroutine consumes `LoadedEntry[K,T]` from the channel and adds them to the cache; preloading ends when the channel is closed. `Ready()` blocks until that goroutine exits. With `PreloadChan == nil`, preloading is disabled and `Ready()` returns immediately.

A TTL watcher runs in the background and removes entries when their TTL has passed. If `AutomaticReload` is not `AutomaticReloadDisabled`, a reload watcher runs and reloads expired entries (respecting `AutomaticReloadAccessedEntries` vs `AutomaticReloadAllEntries`). If `Timeouts.MemsizeUpdate > 0` and `MetricsRegistry` is set, memory usage is periodically recalculated.

`LoadOneFunc` can return `lazy.ErrNotFound` to indicate “not found”; the entry is then cached for `NotFoundTTL` (or not stored if `NotFoundTTL == 0`). Other errors can be cached for `ErrorTTL` on the first load (or not stored if `ErrorTTL == 0`).

## AutomaticReload

-   **AutomaticReloadDisabled** — no background reload. Expired entries are reloaded only on `Get` or when explicitly set via `ForceSet`.
-   **AutomaticReloadAccessedEntries** — only entries that have been accessed (via `Get`/`GetCached`) since their last reload are automatically reloaded when expired.
-   **AutomaticReloadAllEntries** — all expired entries are automatically reloaded in the background.

## Caveats

-   **Do not mutate item data after it is inserted into the cache.** The cache stores pointers to values; modifying the underlying data outside the cache (e.g. in `LoadOneFunc` return values, `ForceSet`, or `PreloadChan` entries) affects what all readers see and can cause races. Treat cached values as read-only.
-   Cached data may not reflect the current state of the underlying storage.
-   `ReloadInterval` must be ≤ `TTL`. TTL should be at least about 2× `ReloadInterval` for smoother behavior.
-   `LoadMultipleFunc` is stored in params and is optional; the cache itself does not call it. It is intended for use by your preload (or other) logic.

## Timeouts

-   **TTL** — Time-to-live. After this duration from the last load (with renewal on access and successful reload), the entry is removed. Randomized by `Randomizer`. Required, must be > 0.
-   **NotFoundTTL** — TTL when the entry is “not found” (`ErrNotFound`) or when the first load of a new key fails with `ErrNotFound`. Randomized by `Randomizer`. If 0, not-found entries are not stored.
-   **ErrorTTL** — TTL when the first load of a new key fails with an error other than `ErrNotFound`. Randomized by `Randomizer`. If 0, such entries are not stored.
-   **ReloadInterval** — How long an entry is considered fresh before it is reloaded. Must be ≤ `TTL`. Randomized by `Randomizer`.
-   **Randomizer** — `[0, 1]`. 0 = no jitter; 0.1 = ±10%. Applied to TTL, NotFoundTTL, ErrorTTL, and ReloadInterval.
-   **MemsizeUpdate** — Interval for recomputing memory usage when metrics are enabled. If 0, memory usage is not updated.

## Params

-   **Context** — `context.Context` for shutdown and `LoadOneFunc` semantics. Required.
-   **Log** — `zerolog.Logger` for cache logs. Required.
-   **MetricsRegistry** — `*cadre_metrics.Registry`; if set, Prometheus metrics are registered. Optional.
-   **Name** — Cache name used in logs and metrics. Required.
-   **LoadOneFunc** — `func(ID K) (*T, error)`. Loads one entry. Return `ErrNotFound` for not found. Required.
-   **LoadMultipleFunc** — `func(IDs []K) []LoadedEntry[K, *T]`. Optional; for batch loading (e.g. in your preload). The cache does not call it.
-   **Timeouts** — TTL, NotFoundTTL, ErrorTTL, ReloadInterval, Randomizer, MemsizeUpdate. Required; `ReloadInterval` ≤ `TTL`, `TTL` > 0.
-   **PreloadChan** — `<-chan LoadedEntry[K,T]`. If non-nil, a goroutine consumes entries until the channel is closed. Optional.
-   **AutomaticReload** — `AutomaticReloadDisabled`, `AutomaticReloadAccessedEntries`, or `AutomaticReloadAllEntries`.

## Metrics

When `MetricsRegistry` is set, these Prometheus metrics are registered (subsystem `lazy_cache`, label `name`):

| Metric | Type | Description |
|--------|------|-------------|
| `items_count` | Gauge | Number of cached items |
| `automatic_loads` | Counter | Automatic reloads (reload watcher) |
| `lazy_loads` | Counter | Loads triggered by `Get` (miss or expired) |
| `force_sets` | Counter | `ForceSet` calls |
| `error_loads` | Counter | Loads that failed with an error other than `ErrNotFound` |
| `reads_count` | Counter | `Get` / `GetCached` calls |
| `received_nats_invalidations` | Counter | NATS invalidation messages received (if/when used) |
| `memory_usage` | Gauge | Estimated size of entries in bytes (when `MemsizeUpdate` > 0) |

---

## Usage examples

### Minimal example (no preload, no automatic reload)

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/moderntv/lazy-cache"
	"github.com/rs/zerolog"
)

func main() {
	ctx := context.Background()
	log := zerolog.Nop()

	c, err := lazy.New(lazy.Params[int, string]{
		Context: ctx,
		Log:     log,
		Name:    "my-lazy-cache",
		LoadOneFunc: func(id int) (*string, error) {
			if id == 0 {
				return nil, lazy.ErrNotFound
			}
			s := fmt.Sprintf("value_%d", id)
			return &s, nil
		},
		Timeouts: lazy.Timeouts{
			TTL:            10 * time.Minute,
			NotFoundTTL:    1 * time.Minute,
			ErrorTTL:       30 * time.Second,
			ReloadInterval: 5 * time.Minute,
			Randomizer:     0.1,
		},
		AutomaticReload: lazy.AutomaticReloadDisabled,
	})
	if err != nil {
		panic(err)
	}

	_ = c.Get(1)           // loads and caches
	_, ok := c.GetCached(1) // from cache, ok == true
	c.Invalidate(1)        // next Get(1) will reload
	c.Remove(1)            // remove from cache
}
```

### With preloading and automatic reload

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/moderntv/lazy-cache"
	"github.com/rs/zerolog"
)

func main() {
	ctx := context.Background()
	log := zerolog.Nop()

	preload := make(chan lazy.LoadedEntry[int, string], 10)
	go func() {
		for i := 1; i <= 100; i++ {
			s := fmt.Sprintf("item_%d", i)
			preload <- lazy.LoadedEntry[int, string]{ID: i, Value: &s, Err: nil}
		}
		close(preload)
	}()

	c, err := lazy.New(lazy.Params[int, string]{
		Context:      ctx,
		Log:          log,
		Name:         "preloaded",
		LoadOneFunc:  func(id int) (*string, error) { return nil, lazy.ErrNotFound },
		PreloadChan:  preload,
		Timeouts:     lazy.Timeouts{TTL: 5 * time.Minute, ReloadInterval: 2 * time.Minute, Randomizer: 0.1},
		AutomaticReload: lazy.AutomaticReloadAccessedEntries,
	})
	if err != nil {
		panic(err)
	}

	c.Ready() // wait for preload to finish
	_ = c.Get(1)
}
```

### Practical example: repository with DB

```go
package user

import (
	"context"
	"errors"
	"time"

	cadre_metrics "github.com/moderntv/cadre/metrics"
	"github.com/moderntv/lazy-cache"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type User struct {
	ID   string
	Name string
}

type Repository struct {
	cache *lazy.Cache[string, User]
}

func NewRepository(ctx context.Context, log zerolog.Logger, db *gorm.DB, metrics *cadre_metrics.Registry) (*Repository, error) {
	c, err := lazy.New(lazy.Params[string, User]{
		Context:         ctx,
		Log:             log,
		MetricsRegistry: metrics,
		Name:            "user",
		LoadOneFunc: func(id string) (*User, error) {
			var u User
			err := db.WithContext(ctx).Where("id = ?", id).First(&u).Error
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, lazy.ErrNotFound
				}
				return nil, err
			}
			return &u, nil
		},
		Timeouts: lazy.Timeouts{
			TTL:            10 * time.Minute,
			NotFoundTTL:    2 * time.Minute,
			ErrorTTL:       30 * time.Second,
			ReloadInterval: 5 * time.Minute,
			Randomizer:     0.1,
			MemsizeUpdate:  time.Minute,
		},
		AutomaticReload: lazy.AutomaticReloadAccessedEntries,
	})
	if err != nil {
		return nil, err
	}
	return &Repository{cache: c}, nil
}

func (r *Repository) User(id string) *User {
	return r.cache.Get(id)
}

func (r *Repository) Invalidate(id string) {
	r.cache.Invalidate(id)
}
```
