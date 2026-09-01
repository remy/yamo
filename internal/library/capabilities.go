package library

import (
	"sort"

	"github.com/remy/yamo/internal/catalog"
	"github.com/remy/yamo/internal/tags"
)

// What this build can do.
//
// A client can find out what a track holds, but not what the server it is
// talking to is capable of. Track.writable says a particular file cannot be
// written; nothing says which formats this build writes, so a client cannot
// warn before an edit rather than after it. Nothing says whether the Discogs
// lookup is configured, so the only way to find out is to search and read the
// failure. Nothing says what the page limit is capped to, so the advice to
// read the response back is the only way to discover it.
//
// This is the answer to all of them, and it is deliberately free of anything
// about the library itself — no roots, no counts, nothing that says what music
// is here. That is what makes it safe to serve without a token, which in turn
// is what makes it useful: a client needs to know whether a token is required
// before it has one.

// Version is the build's version, set at link time on a release build and
// left at its default otherwise.
var Version = "dev"

// Capabilities describes what a server can do.
type Capabilities struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	// AuthRequired says whether a bearer token must be presented. A client
	// with no token can read this endpoint and find out, rather than having
	// to provoke a 401 to learn it.
	AuthRequired bool `json:"authRequired"`

	Formats  []FormatSupport `json:"formats"`
	Limits   Limits          `json:"limits"`
	Features Features        `json:"features"`

	// Fields, SortFields and JobKinds are the vocabularies the API accepts.
	// A client offering a field picker or a sort menu builds it from these
	// rather than from a copy of the documentation that will fall behind.
	Fields          []string `json:"editableFields"`
	SortFields      []string `json:"sortFields"`
	DuplicateFields []string `json:"duplicateFields"`
	JobKinds        []string `json:"jobKinds"`
	ArtworkSources  []string `json:"artworkSources"`

	// KeepTags is the default keep list a strip applies, which is what a
	// client should show as the starting point before somebody edits it.
	KeepTags []string `json:"defaultKeepTags"`
}

// FormatSupport is what this build can do with one container.
type FormatSupport struct {
	Format string `json:"format"`
	Read   bool   `json:"read"`
	Write  bool   `json:"write"`

	// Strip says whether the raw metadata can be enumerated and removed,
	// which needs a writer for the container and so tracks Write.
	Strip bool `json:"strip"`

	Extensions []string `json:"extensions,omitempty"`
	MIME       string   `json:"mime,omitempty"`
}

// Limits are the bounds the server applies to requests.
type Limits struct {
	DefaultPageSize int `json:"defaultPageSize"`
	MaxPageSize     int `json:"maxPageSize"`
	MaxImageBytes   int `json:"maxImageBytes"`
	MinThumbSize    int `json:"minThumbnailSize"`
	MaxThumbSize    int `json:"maxThumbnailSize"`

	// EventHistory is how many recent events a disconnected client can
	// resume through before it is told it missed some.
	EventHistory int `json:"eventHistory"`

	// JobRetentionMS is how long a finished job stays queryable.
	JobRetentionMS int64 `json:"jobRetentionMs"`
}

// Features says which optional parts of this API are turned on.
type Features struct {
	// Discogs is the cover and album lookup, off when the server is meant to
	// make no outbound requests. Tokened says whether it is authenticated,
	// which is the difference between 25 requests a minute and 60.
	Discogs        bool `json:"discogs"`
	DiscogsTokened bool `json:"discogsTokened"`

	// Backups says whether journals can be written, which needs somewhere to
	// put them. Without it a batch edit still works and simply cannot be
	// undone, so a client should say so before running one.
	Backups bool `json:"backups"`

	// Clipboard is the server-side artwork clipboard.
	Clipboard bool `json:"clipboard"`

	// Rescan says whether the server rescans its roots on a timer. When it is
	// false nothing watches the filesystem at all.
	Rescan bool `json:"rescan"`

	// CrossOrigin says whether a browser on another origin may call this API.
	CrossOrigin bool `json:"crossOrigin"`
}

// maxImageBytes is duplicated from the API layer's upload bound so that the
// number a client is told matches the one enforced. It lives here because the
// service is what the bound protects.
const MaxImageBytes = 32 << 20

// Capabilities reports what this build and this configuration can do.
//
// authRequired and crossOrigin are the server's business rather than the
// service's, so they are passed in: the service does not know how it is being
// served.
func (s *Service) Capabilities(authRequired, crossOrigin bool) Capabilities {
	every, _ := s.RescanSchedule()

	c := Capabilities{
		Name:            "yamo",
		Version:         Version,
		AuthRequired:    authRequired,
		Fields:          EditableFields(),
		SortFields:      append([]string(nil), SortFields...),
		DuplicateFields: append(EditableFields(), DupByDuration, DupBySize),
		JobKinds:        append([]string(nil), JobKinds...),
		ArtworkSources: []string{
			string(ArtFromClipboard), string(ArtFromUpload),
			string(ArtFromFolder), string(ArtRemove),
		},
		Limits: Limits{
			DefaultPageSize: DefaultLimit,
			MaxPageSize:     MaxLimit,
			MaxImageBytes:   MaxImageBytes,
			MinThumbSize:    MinThumbSize,
			MaxThumbSize:    MaxThumbSize,
			EventHistory:    eventHistory,
			JobRetentionMS:  jobRetention.Milliseconds(),
		},
		Features: Features{
			Discogs:        s.discogs != nil,
			DiscogsTokened: s.discogs != nil && s.opts.DiscogsToken != "",
			Backups:        s.opts.BackupDir != "",
			Clipboard:      s.opts.ClipboardDir != "",
			Rescan:         every > 0,
			CrossOrigin:    crossOrigin,
		},
	}

	for _, t := range tags.NewKeepSet(tags.DefaultKeepTags).Sorted() {
		c.KeepTags = append(c.KeepTags, t.Name())
	}
	sort.Strings(c.DuplicateFields)
	c.Formats = formatSupport()
	return c
}

// formatSupport describes every container this build knows about.
func formatSupport() []FormatSupport {
	var out []FormatSupport
	for f := tags.Format(0); f < 16; f++ {
		name := f.String()
		if name == "" || f == tags.FormatUnknown {
			continue
		}
		out = append(out, FormatSupport{
			Format: name, Read: true, Write: f.Writable(), Strip: f.Writable(),
			Extensions: formatExtensions(name), MIME: f.MIME(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Format < out[j].Format })
	return out
}

// formatExtensions names the filenames a format goes by, which a client
// uploading or filtering wants and cannot derive from the format name.
func formatExtensions(format string) []string {
	switch format {
	case "mp3":
		return []string{".mp3"}
	case "flac":
		return []string{".flac"}
	case "mp4":
		return []string{".m4a", ".m4b", ".mp4"}
	case "ogg":
		return []string{".ogg", ".oga"}
	case "opus":
		return []string{".opus"}
	case "wma":
		return []string{".wma"}
	case "wav":
		return []string{".wav"}
	case "aiff":
		return []string{".aif", ".aiff"}
	}
	return nil
}

// fieldNames is the full list of readable fields, for a client building a
// column picker rather than an edit form.
func fieldNames() []string {
	out := []string{}
	for f := catalog.Field(0); f < catalog.Field(len(catalog.FieldNames)); f++ {
		if catalog.FieldNames[f] != "" {
			out = append(out, catalog.FieldNames[f])
		}
	}
	sort.Strings(out)
	return out
}
