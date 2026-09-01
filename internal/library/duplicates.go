package library

import (
	"sort"
	"strconv"
	"strings"

	"github.com/remy/yamo/internal/catalog"
)

// Finding the same recording more than once.
//
// A library that has been merged from a few sources has duplicates, and they
// are invisible from a search: the two copies sort next to each other, look
// identical, and nothing counts them. They are also not simply deletable —
// one is usually a better rip than the other — so what this offers is the
// grouping and the evidence, and the deciding is the client's.
//
// What counts as the same recording is the caller's to say, because it depends
// on what went wrong. Two rips of one album duplicate on artist and title. A
// compilation that also appears as its own album duplicates on those but not
// on album. A file copied twice under different names duplicates on everything
// including size. So the key is a field list rather than a rule.

// DuplicateKeyFields are the extra keys accepted beyond the catalogue's own
// fields. Both describe the audio rather than the tags, which is what catches
// a duplicate whose metadata was edited on one copy and not the other.
const (
	DupByDuration = "duration"
	DupBySize     = "size"
)

// DefaultDuplicateKey is what to group on when the caller says nothing: the
// same performer and the same song, which is the duplicate people mean.
var DefaultDuplicateKey = []string{"artist", "title"}

// DuplicateParams asks for groups of tracks sharing a key.
type DuplicateParams struct {
	Query string

	// By names the fields that must match for two tracks to count as the
	// same. Empty uses DefaultDuplicateKey.
	By []string

	// Duration rounds durations into buckets of this many seconds before
	// comparing, when duration is part of the key. Two rips of one track
	// differ by a few hundred milliseconds, so comparing exactly would find
	// nothing; the default is 2.
	DurationSeconds int

	Limit  int
	Offset int
}

// DuplicateGroup is one set of tracks that appear to be the same recording.
type DuplicateGroup struct {
	// Key is the shared value, rendered for display.
	Key string `json:"key"`

	Tracks int `json:"tracks"`

	// Bytes is what the copies occupy in total, and Wasted is what would be
	// freed by keeping one of each — which is the number worth showing.
	Bytes  int64 `json:"bytes"`
	Wasted int64 `json:"wasted"`

	// Items are the tracks themselves, so a client can compare bitrates and
	// paths without a request per group.
	Items []Track `json:"items"`

	// Query reselects exactly this group, on the same principle as an album's.
	Query string `json:"query"`
}

// DuplicatePage is one page of duplicate groups.
type DuplicatePage struct {
	Items  []DuplicateGroup `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`

	// By echoes the key actually used, since an unrecognised field name is
	// dropped rather than rejected and the client should know which.
	By []string `json:"by"`

	// Tracks and Wasted total across every group, not just this page: the
	// summary line above a paged list is about the whole problem.
	Tracks int   `json:"duplicateTracks"`
	Wasted int64 `json:"wastedBytes"`
}

// maxDuplicateItems bounds the tracks listed per group. A file duplicated four
// hundred times is a broken scan rather than a library to tidy, and listing it
// in full would bury the groups worth looking at.
const maxDuplicateItems = 20

// defaultDurationBucket is how coarsely durations are compared, in seconds.
const defaultDurationBucket = 2

// Duplicates groups matching tracks by a key and returns the groups with more
// than one member.
func (s *Service) Duplicates(p DuplicateParams) DuplicatePage {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.DurationSeconds <= 0 {
		p.DurationSeconds = defaultDurationBucket
	}

	keys, names := parseDuplicateKey(p.By)
	if len(keys) == 0 {
		keys, names = parseDuplicateKey(DefaultDuplicateKey)
	}

	s.mu.RLock()
	hits := s.cat.Index().Search(catalog.ParseQuery(p.Query))
	type agg struct {
		label string
		idx   []int32
	}
	byKey := map[string]*agg{}
	for _, i := range hits {
		t := &s.cat.Tracks[i]
		key, label, ok := duplicateKey(t, keys, p.DurationSeconds)
		if !ok {
			// A track missing a value the key names cannot be said to match
			// anything on it. Grouping the gaps together would put every
			// untitled track in one enormous false group.
			continue
		}
		a := byKey[key]
		if a == nil {
			a = &agg{label: label}
			byKey[key] = a
		}
		a.idx = append(a.idx, i)
	}

	groups := make([]DuplicateGroup, 0, len(byKey))
	page := DuplicatePage{By: names, Items: []DuplicateGroup{}}
	for _, a := range byKey {
		if len(a.idx) < 2 {
			continue
		}
		g := DuplicateGroup{Key: a.label, Tracks: len(a.idx), Items: []Track{}}
		var largest int64
		for _, i := range a.idx {
			t := &s.cat.Tracks[i]
			g.Bytes += t.Size
			if t.Size > largest {
				largest = t.Size
			}
			if len(g.Items) < maxDuplicateItems {
				g.Items = append(g.Items, s.toTrack(t))
			}
		}
		// Keeping one copy means keeping the biggest, which is very nearly
		// always the better rip.
		g.Wasted = g.Bytes - largest
		g.Query = duplicateQuery(&s.cat.Tracks[a.idx[0]], keys)
		groups = append(groups, g)
		page.Tracks += g.Tracks
		page.Wasted += g.Wasted
	}
	s.mu.RUnlock()

	// Most wasteful first: the list is read top-down and stopped when it stops
	// being worth the time.
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Wasted != groups[j].Wasted {
			return groups[i].Wasted > groups[j].Wasted
		}
		return groups[i].Key < groups[j].Key
	})

	page.Total, page.Limit, page.Offset = len(groups), p.Limit, p.Offset
	if p.Offset >= len(groups) {
		return page
	}
	page.Items = groups[p.Offset:min(p.Offset+p.Limit, len(groups))]
	return page
}

// dupKey is one component of a duplicate key.
type dupKey struct {
	field    catalog.Field
	name     string
	duration bool
	size     bool
}

// parseDuplicateKey compiles the field list, ignoring names it does not
// recognise rather than failing — the same rule the sort parameter follows,
// for the same reason: a half-typed key should degrade, not error.
func parseDuplicateKey(by []string) ([]dupKey, []string) {
	var keys []dupKey
	var names []string
	seen := map[string]bool{}
	for _, raw := range by {
		for _, part := range strings.Split(raw, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" || seen[part] {
				continue
			}
			switch part {
			case DupByDuration, "time", "length":
				seen[part] = true
				keys = append(keys, dupKey{duration: true, name: DupByDuration})
				names = append(names, DupByDuration)
				continue
			case DupBySize:
				seen[part] = true
				keys = append(keys, dupKey{size: true, name: DupBySize})
				names = append(names, DupBySize)
				continue
			}
			f, ok := catalog.LookupField(part)
			if !ok {
				continue
			}
			name := catalog.FieldNames[f]
			if seen[name] {
				continue
			}
			seen[name] = true
			keys = append(keys, dupKey{field: f, name: name})
			names = append(names, name)
		}
	}
	return keys, names
}

// duplicateKey builds the comparison key for one track, and the label a client
// reads it by. It reports false when any component is empty.
func duplicateKey(t *catalog.Track, keys []dupKey, bucket int) (string, string, bool) {
	var key, label strings.Builder
	for i, k := range keys {
		var raw, shown string
		switch {
		case k.duration:
			if t.DurationMS <= 0 {
				return "", "", false
			}
			secs := int(t.DurationMS) / 1000
			raw = strconv.Itoa(secs / bucket)
			shown = strconv.Itoa(secs) + "s"
		case k.size:
			if t.Size <= 0 {
				return "", "", false
			}
			raw = strconv.FormatInt(t.Size, 10)
			shown = raw + " bytes"
		default:
			v := t.String(k.field)
			if strings.TrimSpace(v) == "" {
				return "", "", false
			}
			// Folded, so "Björk" and "Bjork" are one recording rather than
			// two — which is exactly the kind of duplicate a merged library
			// has.
			raw = catalog.Fold(v)
			shown = v
		}
		if i > 0 {
			key.WriteByte(0)
			label.WriteString(" — ")
		}
		key.WriteString(raw)
		label.WriteString(shown)
	}
	return key.String(), label.String(), true
}

// duplicateQuery builds a query selecting exactly one group, so a client can
// go from a group to an operation on it the way it does from an album.
//
// Only the text fields go in: duration and size are not searchable, so a key
// including them reselects a superset. That is the honest result — the query
// finds the group and possibly its neighbours, rather than claiming precision
// the language cannot express.
func duplicateQuery(t *catalog.Track, keys []dupKey) string {
	var b strings.Builder
	for _, k := range keys {
		if k.duration || k.size {
			continue
		}
		v := t.String(k.field)
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k.name + `:"^` + strings.ReplaceAll(v, `"`, "") + `$"`)
	}
	return b.String()
}
