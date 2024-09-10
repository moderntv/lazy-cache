package memsize

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type nonMeassurableEntry struct {
	a     int
	b     string
	c     bool
	d     []float64
	m     map[string]string
	entry *nonMeassurableEntry
}

const expectedNonMeassurableEntrySize = 600

func newNonMeassurableEntry() *nonMeassurableEntry {
	return &nonMeassurableEntry{
		a: 1,
		b: "test 468wad46aw8d46aw4dsa6 4r s84 6as84 ac6s46ax46 ",
		c: true,
		d: []float64{1.0, 2.0, 3.0, 4.0, 5.0},
		entry: &nonMeassurableEntry{
			a: 2,
			b: "test2 4as68d 4as684 as4x3s1 32c1as3ef sas54fg54te6h84z6 84gas68dg4t6rhj84r",
			c: false,
			d: []float64{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0},
			m: map[string]string{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
				"key4": "value4",
			},
		},
	}
}

func TestNonMeassurableEntry(t *testing.T) {
	e := newNonMeassurableEntry()
	size := Entry(e)
	assert.Less(t, uint64(expectedNonMeassurableEntrySize*0.8), size, "Entry size should be greater")
	assert.Greater(t, uint64(expectedNonMeassurableEntrySize*1.2), size, "Entry size should be less")
}

func TestNonMeassurableEntries(t *testing.T) {
	count := 100
	m := make(map[int]*nonMeassurableEntry, count)
	for i := 0; i < count; i++ {
		m[i] = newNonMeassurableEntry()
	}

	size := Entries(m)
	assert.Less(t, uint64(float64(expectedNonMeassurableEntrySize*count)*0.8), size, "Entries size should be greater")
	assert.Greater(t, uint64(float64(expectedNonMeassurableEntrySize*count)*1.2), size, "Entries size should be less")
}

type meassurableEntry struct{}

const meassurableEntrySize = 100

func (e meassurableEntry) MemSize() uint64 {
	return meassurableEntrySize
}

func TestMeassurableEntry(t *testing.T) {
	e := meassurableEntry{}

	size := Entry(e)
	assert.Equal(t, uint64(100), size, "Entry size should does not match")
}

func TestMeassurableEntries(t *testing.T) {
	count := 50
	m := make(map[int]*meassurableEntry, count)
	for i := 0; i < count; i++ {
		m[i] = &meassurableEntry{}
	}

	size := Entries(m)
	assert.Equal(t, uint64(meassurableEntrySize*count), size, "Entries size does not match")
}
