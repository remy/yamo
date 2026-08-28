package library

import "fmt"

// Selector names the tracks an operation applies to.
//
// Selecting by query rather than by identifier is the point: setting the
// artist on every Elvis track should not require a phone to upload two and a
// half thousand identifiers, and the set is defined by the same query language
// the user typed to find them in the first place.
type Selector struct {
	// IDs selects specific tracks. When set, Query is ignored.
	IDs []string `json:"ids,omitempty"`

	// Query selects every track matching a search expression.
	Query string `json:"query,omitempty"`

	// ExcludeIDs removes tracks from a query selection, which is how an
	// interface expresses "all of these except the three I unticked".
	ExcludeIDs []string `json:"excludeIds,omitempty"`

	// ExpectCount, when set, must equal the number of tracks selected.
	//
	// This is a safety rail for destructive work. The client states how many
	// matches it showed the user; if the library has changed since — a scan
	// finished, another client edited something — the operation is refused
	// rather than silently applied to a different set than the one approved.
	ExpectCount *int `json:"expectCount,omitempty"`

	// All must be set explicitly to select the entire library, so that an
	// empty selector cannot be mistaken for "everything".
	All bool `json:"all,omitempty"`
}

// ErrSelectorEmpty means the selector would not have matched anything on
// purpose, which is nearly always a client bug rather than an intent.
var ErrSelectorEmpty = fmt.Errorf("library: the selector names no tracks; set ids, query, or all")

// CountMismatchError reports that the selection changed under the client.
type CountMismatchError struct {
	Expected int
	Actual   int
}

func (e *CountMismatchError) Error() string {
	return fmt.Sprintf("library: the selection now matches %d tracks, not the %d expected; "+
		"something changed since the list was read", e.Actual, e.Expected)
}

// Resolve turns a selector into the ids it names.
func (s *Service) Resolve(sel Selector) ([]string, error) {
	var ids []string
	switch {
	case len(sel.IDs) > 0:
		ids = sel.IDs
	case sel.Query != "":
		ids = s.matchIDs(sel.Query)
	case sel.All:
		ids = s.matchIDs("")
	default:
		return nil, ErrSelectorEmpty
	}

	if len(sel.ExcludeIDs) > 0 {
		drop := make(map[string]struct{}, len(sel.ExcludeIDs))
		for _, id := range sel.ExcludeIDs {
			drop[id] = struct{}{}
		}
		kept := ids[:0:0]
		for _, id := range ids {
			if _, skip := drop[id]; !skip {
				kept = append(kept, id)
			}
		}
		ids = kept
	}

	if sel.ExpectCount != nil && *sel.ExpectCount != len(ids) {
		return nil, &CountMismatchError{Expected: *sel.ExpectCount, Actual: len(ids)}
	}
	return ids, nil
}
