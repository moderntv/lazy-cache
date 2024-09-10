package lazy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/moderntv/lazy-cache/internal/test_utils"
)

type entryMemTestGeneric struct {
	intValue   int64
	intPointer *int64
	strValue   string
	strPointer *string
}

func testCacheMemsizeCalculated(t *testing.T) {
	t.Parallel()

	timeouts := cacheTestTimeouts
	timeouts.MemsizeUpdate = 1 * time.Second

	c, err := NewCache(Params[int, entryMemTestGeneric]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *entryMemTestGeneric, err error) {
			switch ID {
			case 1:
				entry = &entryMemTestGeneric{
					intValue:   135,
					intPointer: test_utils.Int64Pointer(89465),
					strValue:   "abcde",
					strPointer: test_utils.StringPointer("abcdefgh jkl"),
				}
				return

			case 2:
				entry = &entryMemTestGeneric{
					intValue:   468,
					intPointer: nil,
					strValue:   "abcdefgh ijklmnop qr",
					strPointer: nil,
				}
				return

			default:
				err = ErrNotFound
				return
			}
		},
		Timeouts:        timeouts,
		MetricsRegistry: test_utils.Metrics("metrics1"),
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	time.Sleep(500 * time.Millisecond)
	// 0.5s - no entries
	assert.Equal(t, uint64(0), c.memSizeValue.Load())
	_ = c.Get(1)
	time.Sleep(1000 * time.Millisecond)
	// 1.5s - entry #1
	size := c.memSizeValue.Load()
	t.Logf("1.5s size: %d", size)
	assert.Greater(t, size, uint64(72))
	assert.Less(t, size, uint64(100))
	_ = c.Get(0)
	time.Sleep(1000 * time.Millisecond)
	// 2.5s - entry #0, #1
	newSize := c.memSizeValue.Load()
	t.Logf("2.5s size: %d", newSize)
	assert.Equal(t, size, newSize)
	_ = c.Get(2)
	time.Sleep(1000 * time.Millisecond)
	// 3.5s - entry #0, #1, #2
	size = c.memSizeValue.Load()
	t.Logf("3.5s size: %d", size)
	assert.Greater(t, size, uint64(136))
	assert.Less(t, size, uint64(180))
}

type entryMemTestManual struct {
	value int
}

func (e *entryMemTestManual) MemSize() uint64 {
	switch e.value {
	case 1:
		return 100

	case 2:
		return 1000

	default:
		return 10000
	}
}

func testCacheMemsizeManual(t *testing.T) {
	t.Parallel()

	timeouts := cacheTestTimeouts
	timeouts.MemsizeUpdate = 1 * time.Second

	c, err := NewCache(Params[int, entryMemTestManual]{
		Context: context.Background(),
		Log:     test_utils.Logger(),
		Name:    "test_cache1",
		LoadOneFunc: func(ID int) (entry *entryMemTestManual, err error) {
			switch ID {
			case 1:
				entry = &entryMemTestManual{1}
				return

			case 2:
				entry = &entryMemTestManual{2}
				return

			default:
				err = ErrNotFound
				return
			}
		},
		Timeouts:        timeouts,
		MetricsRegistry: test_utils.Metrics("metrics1"),
		AutomaticReload: AutomaticReloadDisabled,
	})

	assert.Nil(t, err)

	time.Sleep(500 * time.Millisecond)
	// 0.5s - no entries
	assert.Equal(t, uint64(0), c.memSizeValue.Load())
	_ = c.Get(1) // expiration at 7.5s
	time.Sleep(1000 * time.Millisecond)
	// 1.5s - entry #1
	size := c.memSizeValue.Load()
	t.Logf("1.5s size: %d", size)
	assert.Equal(t, uint64(100), size)
	_ = c.Get(0) // expiration at 6.5s
	time.Sleep(1000 * time.Millisecond)
	// 2.5s - entry #0, #1
	size = c.memSizeValue.Load()
	t.Logf("2.5s size: %d", size)
	assert.Equal(t, uint64(100), size)
	_ = c.Get(2) // expiration at 9.5s
	time.Sleep(1000 * time.Millisecond)
	// 3.5s - entry #0, #1, #2
	size = c.memSizeValue.Load()
	t.Logf("3.5s size: %d", size)
	assert.Equal(t, uint64(1100), size)
	time.Sleep(3500 * time.Millisecond)
	// 7s - entry #1, #2
	size = c.memSizeValue.Load()
	t.Logf("7s size: %d", size)
	assert.Equal(t, uint64(1100), size)
	time.Sleep(1500 * time.Millisecond)
	// 8.5s - entry #2
	size = c.memSizeValue.Load()
	t.Logf("8.5s size: %d", size)
	assert.Equal(t, uint64(1000), size)
	time.Sleep(2000 * time.Millisecond)
	// 10.5s - no entries
	size = c.memSizeValue.Load()
	t.Logf("10.5s size: %d", size)
	assert.Equal(t, uint64(0), size)
}
