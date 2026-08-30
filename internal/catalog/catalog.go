package catalog

import (
	"sort"
	"time"
)

// Catalog is the whole library: a flat slice of tracks plus the lookup and
// search structures built over it.
type Catalog struct {
	Tracks    []Track
	Roots     []string
	ScannedAt time.Time

	index *Index
}

// New returns an empty catalogue.
func New() *Catalog { return &Catalog{} }

// Len returns the number of tracks.
func (c *Catalog) Len() int { return len(c.Tracks) }

// SortByPath orders tracks by path, which groups albums together and makes the
// default view stable across rescans.
func (c *Catalog) SortByPath() {
	sort.Slice(c.Tracks, func(i, j int) bool { return c.Tracks[i].Path < c.Tracks[j].Path })
	c.index = nil
}

// Index returns the search index, building it on first use.
func (c *Catalog) Index() *Index {
	if c.index == nil {
		c.index = buildIndex(c)
	}
	return c.index
}

// touch refreshes the index entry for a track edited in place.
func (c *Catalog) touch(i int) {
	if c.index != nil {
		c.index.update(i)
	}
}

// Touch marks a track as changed so its index entry is rebuilt.
func (c *Catalog) Touch(i int) { c.touch(i) }

// Remove drops one track.
//
// Everything past it shifts down, so every index a caller holds — including
// the ones inside the search index — is invalidated. The index is dropped
// rather than patched: removals are rare, and rebuilding it is a single pass
// over a slice that was going to be walked anyway.
func (c *Catalog) Remove(i int) {
	if i < 0 || i >= len(c.Tracks) {
		return
	}
	c.Tracks = append(c.Tracks[:i], c.Tracks[i+1:]...)
	c.index = nil
}

// DirtyCount reports how many tracks have unsaved edits.
func (c *Catalog) DirtyCount() int {
	n := 0
	for i := range c.Tracks {
		if c.Tracks[i].Dirty() {
			n++
		}
	}
	return n
}
