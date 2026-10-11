package lazy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

var errTestLoad = errors.New("database is down")

// scriptedLoader returns the configured result for every ID
type scriptedLoader struct {
	mu    sync.Mutex
	value *string
	err   error
	loads int
}

func (l *scriptedLoader) set(value *string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.value = value
	l.err = err
}

func (l *scriptedLoader) load(ID int) (*string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.loads++
	return l.value, l.err
}

func (l *scriptedLoader) loadCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.loads
}

func newScriptedCache(
	t *testing.T,
	loader *scriptedLoader,
	timeouts Timeouts,
) *Cache[int, string] {
	c, err := New(Params[int, string]{
		Context:         context.Background(),
		Log:             test_utils.Logger(),
		Name:            "test_get_with_error",
		LoadOneFunc:     loader.load,
		Timeouts:        timeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})
	require.NoError(t, err)

	return c
}

func TestGetWithErrorReturnsValue(t *testing.T) {
	loader := &scriptedLoader{value: test_utils.StringPointer("a")}
	c := newScriptedCache(t, loader, cacheTestTimeouts)

	value, err := c.GetWithError(1)

	require.NoError(t, err)
	require.NotNil(t, value)
	assert.Equal(t, "a", *value)
}

func TestGetWithErrorNotFound(t *testing.T) {
	loader := &scriptedLoader{err: ErrNotFound}
	c := newScriptedCache(t, loader, cacheTestTimeouts)

	value, err := c.GetWithError(1)

	assert.Nil(t, value)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGetWithErrorFailedFirstLoad(t *testing.T) {
	loader := &scriptedLoader{err: errTestLoad}
	c := newScriptedCache(t, loader, cacheTestTimeouts)

	value, err := c.GetWithError(1)
	assert.Nil(t, value)
	assert.ErrorIs(t, err, errTestLoad)

	// served from cache until ErrorTTL, the error is kept
	value, err = c.GetWithError(1)
	assert.Nil(t, value)
	assert.ErrorIs(t, err, errTestLoad)
	assert.Equal(t, 1, loader.loadCount())
	assert.Nil(t, c.Get(1))
}

func TestGetWithErrorFailedFirstLoadNotStored(t *testing.T) {
	timeouts := cacheTestTimeouts
	timeouts.ErrorTTL = 0
	loader := &scriptedLoader{err: errTestLoad}
	c := newScriptedCache(t, loader, timeouts)

	value, err := c.GetWithError(1)

	assert.Nil(t, value)
	assert.ErrorIs(t, err, errTestLoad)
	assert.Equal(t, 0, cacheSize(c))
}

func TestGetWithErrorFailedReloadKeepsValue(t *testing.T) {
	loader := &scriptedLoader{value: test_utils.StringPointer("a")}
	c := newScriptedCache(t, loader, cacheTestTimeouts)
	_, err := c.GetWithError(1)
	require.NoError(t, err)

	loader.set(nil, errTestLoad)
	c.Invalidate(1)
	value, err := c.GetWithError(1)

	require.NoError(t, err)
	require.NotNil(t, value)
	assert.Equal(t, "a", *value)
	assert.Equal(t, 2, loader.loadCount())
}

func TestGetWithErrorSuccessfulReloadClearsError(t *testing.T) {
	loader := &scriptedLoader{err: errTestLoad}
	c := newScriptedCache(t, loader, cacheTestTimeouts)
	_, err := c.GetWithError(1)
	require.ErrorIs(t, err, errTestLoad)

	loader.set(test_utils.StringPointer("a"), nil)
	c.Invalidate(1)
	value, err := c.GetWithError(1)
	require.NoError(t, err)
	require.NotNil(t, value)
	assert.Equal(t, "a", *value)

	loader.set(nil, ErrNotFound)
	c.Invalidate(1)
	value, err = c.GetWithError(1)
	assert.Nil(t, value)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGetWithErrorFailedReloadOfNotFound(t *testing.T) {
	loader := &scriptedLoader{err: ErrNotFound}
	c := newScriptedCache(t, loader, cacheTestTimeouts)
	_, err := c.GetWithError(1)
	require.ErrorIs(t, err, ErrNotFound)

	loader.set(nil, errTestLoad)
	c.Invalidate(1)
	value, err := c.GetWithError(1)

	assert.Nil(t, value)
	assert.ErrorIs(t, err, errTestLoad)
}

func TestGetWithErrorAfterFailedGetMultiple(t *testing.T) {
	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_get_with_error_multiple",
		LoadOneFunc: func(ID int) (*string, error) {
			return nil, errors.New("LoadOneFunc must not be called")
		},
		LoadMultipleFunc: func(ids []int) []LoadedEntry[int, string] {
			return []LoadedEntry[int, string]{{ID: 1, Err: errTestLoad}}
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})
	require.NoError(t, err)

	assert.Empty(t, c.GetMultiple([]int{1}))
	value, err := c.GetWithError(1)

	assert.Nil(t, value)
	assert.ErrorIs(t, err, errTestLoad)
}

// a value-less result always carries the error of the state it comes from,
// even when the reader races a reload that changes the state
func TestGetWithErrorConcurrentReloads(t *testing.T) {
	var failing atomic.Bool
	c, err := New(Params[int, string]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_get_with_error_concurrent",
		LoadOneFunc: func(ID int) (*string, error) {
			if failing.Load() {
				return nil, errTestLoad
			}

			return test_utils.StringPointer("a"), nil
		},
		Timeouts:        cacheTestTimeouts,
		AutomaticReload: AutomaticReloadDisabled,
	})
	require.NoError(t, err)

	deadline := time.Now().Add(200 * time.Millisecond)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				failing.Store(!failing.Load())
				c.Remove(1)
				c.Invalidate(1)
			}
		})
	}
	for range 8 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				value, err := c.GetWithError(1)
				if value != nil {
					assert.NoError(t, err)
					continue
				}

				assert.ErrorIs(t, err, errTestLoad)
			}
		})
	}
	wg.Wait()
}
