package catalog

import (
	"sort"
	"strings"
)

// blobOrder is the field layout inside each track's folded search blob.
// Everything up to and including Comment is covered by an unqualified search;
// Path sits last so it is reachable with path: but does not flood bare queries
// with directory-name matches.
// The sort fields sit past Path for the same reason Path sits past Comment:
// they are reachable with artistsort: and the rest, but a bare query for
// "presley" should not match every track whose sort tag happens to say
// "Presley, Elvis" when its artist does not. They cost almost nothing to
// carry — a track without them adds five NUL bytes to its blob — and leaving
// them out would make a qualified query for one silently match nothing.
var blobOrder = [...]Field{
	FieldTitle, FieldArtist, FieldAlbumArtist, FieldAlbum,
	FieldGenre, FieldComposer, FieldComment, FieldPath,
	FieldTitleSort, FieldArtistSort, FieldAlbumSort,
	FieldAlbumArtistSort, FieldComposerSort,
}

const (
	numBlob  = len(blobOrder)
	bareSlot = 6 // last slot an unqualified term searches (Comment)
)

// blobSlot maps a Field to its slot, or -1 for fields not stored as text.
var blobSlot = func() [numFields]int8 {
	var m [numFields]int8
	for i := range m {
		m[i] = -1
	}
	for slot, f := range blobOrder {
		m[f] = int8(slot)
	}
	return m
}()

// Index is the search structure. For each track it holds one folded string
// containing every searchable field, NUL-separated, plus the end offset of
// each field. A search is then a linear pass of substring tests over
// contiguous memory, which for a library of this size is faster than any
// inverted index would be once query parsing and posting-list merges are paid
// for — and it supports mid-word matching, which an inverted index does not.
type Index struct {
	cat   *Catalog
	blobs []string
	spans [][numBlob]uint32

	values      [numFields]*ValueSet
	valuesStale bool
}

func buildIndex(c *Catalog) *Index {
	ix := &Index{
		cat:         c,
		blobs:       make([]string, len(c.Tracks)),
		spans:       make([][numBlob]uint32, len(c.Tracks)),
		valuesStale: true,
	}
	var sb strings.Builder
	for i := range c.Tracks {
		ix.buildRow(i, &sb)
	}
	return ix
}

// buildRow folds one track into the blob layout. sb is reused across rows.
func (ix *Index) buildRow(i int, sb *strings.Builder) {
	t := &ix.cat.Tracks[i]
	sb.Reset()
	var span [numBlob]uint32
	for slot, f := range blobOrder {
		if slot > 0 {
			sb.WriteByte(0)
		}
		sb.WriteString(Fold(t.String(f)))
		span[slot] = uint32(sb.Len())
	}
	ix.blobs[i] = sb.String()
	ix.spans[i] = span
}

// update rebuilds one row after an edit.
func (ix *Index) update(i int) {
	if i < 0 || i >= len(ix.blobs) {
		return
	}
	var sb strings.Builder
	ix.buildRow(i, &sb)
	ix.valuesStale = true
}

// field returns the folded text of one field for track i.
func (ix *Index) field(i, slot int) string {
	blob := ix.blobs[i]
	end := int(ix.spans[i][slot])
	start := 0
	if slot > 0 {
		start = int(ix.spans[i][slot-1]) + 1
	}
	if start > end || end > len(blob) {
		return ""
	}
	return blob[start:end]
}

// Search returns the indices of every track matching q, in catalogue order.
// A nil or empty query returns everything.
func (ix *Index) Search(q *Query) []int32 {
	n := len(ix.blobs)
	if q == nil || q.Empty() {
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(i)
		}
		return out
	}
	out := make([]int32, 0, 256)
	for i := 0; i < n; i++ {
		if ix.matches(i, q) {
			out = append(out, int32(i))
		}
	}
	return out
}

func (ix *Index) matches(i int, q *Query) bool {
	for k := range q.terms {
		if ix.matchTerm(i, &q.terms[k]) == q.terms[k].negate {
			return false
		}
	}
	return true
}

func (ix *Index) matchTerm(i int, t *term) bool {
	// Numeric fields compare against the track directly.
	if t.op != cmpNone {
		tr := &ix.cat.Tracks[i]
		switch t.field {
		case FieldYear:
			return t.matchNumeric(tr.Year)
		case FieldTrackNo:
			return t.matchNumeric(tr.TrackNo)
		case FieldDisc:
			return t.matchNumeric(tr.Disc)
		case FieldCompilation:
			// A flag rides the numeric path so that compilation:1 and
			// compilation:0 both work, and so does the bare compilation:
			// form that finds tracks without it.
			var v int32
			if tr.Compilation {
				v = 1
			}
			return t.matchNumeric(v)
		}
		return false
	}

	if t.field == fieldAny {
		// Unqualified: scan the tag fields in one substring test.
		blob := ix.blobs[i]
		end := int(ix.spans[i][bareSlot])
		if end > len(blob) {
			end = len(blob)
		}
		return strings.Contains(blob[:end], t.value)
	}

	slot := blobSlot[t.field]
	if slot < 0 {
		return false
	}
	v := ix.field(i, int(slot))
	if t.value == "" {
		return v == "" // field:  matches tracks where the field is unset
	}
	return strings.Contains(v, t.value)
}

// ValueCount is one distinct field value and how many tracks carry it.
type ValueCount struct {
	Value  string
	Folded string
	Count  int
}

// ValueSet is the sorted set of distinct values for one field, used to drive
// autocomplete.
type ValueSet struct {
	Values []ValueCount // sorted by Folded
}

// Values returns the distinct values for a field, building the set on demand.
func (ix *Index) Values(f Field) *ValueSet {
	if ix.valuesStale {
		for i := range ix.values {
			ix.values[i] = nil
		}
		ix.valuesStale = false
	}
	if ix.values[f] == nil {
		ix.values[f] = ix.buildValues(f)
	}
	return ix.values[f]
}

func (ix *Index) buildValues(f Field) *ValueSet {
	counts := make(map[string]int, 1024)
	for i := range ix.cat.Tracks {
		if v := ix.cat.Tracks[i].String(f); v != "" {
			counts[v]++
		}
	}
	vs := &ValueSet{Values: make([]ValueCount, 0, len(counts))}
	for v, n := range counts {
		vs.Values = append(vs.Values, ValueCount{Value: v, Folded: Fold(v), Count: n})
	}
	sort.Slice(vs.Values, func(i, j int) bool {
		if vs.Values[i].Folded != vs.Values[j].Folded {
			return vs.Values[i].Folded < vs.Values[j].Folded
		}
		return vs.Values[i].Value < vs.Values[j].Value
	})
	return vs
}

// Complete returns candidate values for prefix, most-used first. Prefix
// matches are preferred; when there are none, it falls back to substring
// matches so a partly-remembered album still finds itself.
func (vs *ValueSet) Complete(prefix string, limit int) []ValueCount {
	if limit <= 0 {
		return nil
	}
	folded := Fold(strings.TrimSpace(prefix))
	if folded == "" {
		return topN(vs.Values, limit)
	}

	// The set is sorted by folded value, so every prefix match is contiguous.
	lo := sort.Search(len(vs.Values), func(i int) bool { return vs.Values[i].Folded >= folded })
	var hits []ValueCount
	for i := lo; i < len(vs.Values); i++ {
		if !strings.HasPrefix(vs.Values[i].Folded, folded) {
			break
		}
		hits = append(hits, vs.Values[i])
	}
	if len(hits) > 0 {
		return topN(hits, limit)
	}

	for i := range vs.Values {
		if strings.Contains(vs.Values[i].Folded, folded) {
			hits = append(hits, vs.Values[i])
			if len(hits) >= limit*4 {
				break
			}
		}
	}
	return topN(hits, limit)
}

// topN sorts by descending count and truncates. Ties keep alphabetical order.
func topN(in []ValueCount, limit int) []ValueCount {
	out := make([]ValueCount, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
