package memsize

import (
	monkeysize "github.com/streamonkey/size"
)

type Meassurable interface {
	MemSize() uint64
}

func Entries[K comparable, T any](entries map[K]T) uint64 {
	var totalSize uint64
	for _, entry := range entries {
		totalSize += Entry(entry)
	}

	return totalSize
}

func Entry(entry any) uint64 {
	m, ok := entry.(Meassurable)
	if !ok {
		return uint64(monkeysize.Of(entry))
	}

	return m.MemSize()
}
