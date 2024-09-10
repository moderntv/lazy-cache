package lazy

import (
	"context"
	"errors"

	cadre_metrics "github.com/moderntv/cadre/metrics"
	"github.com/rs/zerolog"
)

type AutomaticReload int

const (
	AutomaticReloadDisabled AutomaticReload = iota
	AutomaticReloadAccessedEntries
	AutomaticReloadAllEntries
)

type LoadedEntry[K comparable, T any] struct {
	ID    K
	Value *T
	Err   error
}

type LoadOneFunc[K comparable, T any] func(ID K) (entry *T, err error)
type LoadMultipleFunc[K comparable, T any] func(IDs []K) (entries []LoadedEntry[K, *T])

type Params[K comparable, T any] struct {
	Context         context.Context
	Log             zerolog.Logger
	MetricsRegistry *cadre_metrics.Registry
	// Invalidations    *Invalidations
	Name string
	// LoadOneFunc server to load one entry by its ID
	LoadOneFunc LoadOneFunc[K, T]
	// LoadMultipleFunc server to load in batch multiple entries by their IDs
	// (which should be more efficient than calling LoadOneFunc multiple times)
	LoadMultipleFunc LoadMultipleFunc[K, T]
	Timeouts         Timeouts
	// PreloadChan serves to preload entries into cache, usually right after cache
	// initialization. Preloading finishes when the channel is closed.
	PreloadChan     <-chan LoadedEntry[K, T]
	AutomaticReload AutomaticReload
}

func (p *Params[K, T]) check() error {
	if p.Context == nil {
		return errors.New("context must be set")
	}

	if p.Name == "" {
		return errors.New("name must be set")
	}

	if p.LoadOneFunc == nil {
		return errors.New("LoadOneFunc must be provided")
	}

	err := p.Timeouts.check()
	if err != nil {
		return err
	}

	// if p.Invalidations != nil {
	// 	return p.Invalidations.check()
	// }

	return nil
}
