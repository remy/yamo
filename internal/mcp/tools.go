package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/remy/yamo/internal/auth"
	"github.com/remy/yamo/internal/library"
)

// The tool set.
//
// It is twenty operations over an API of forty-five, and the arithmetic is the
// design. Three kinds of endpoint are deliberately absent: the ones that move
// bytes a model cannot read (the audio stream, artwork images, the clipboard),
// the ones that exist only to let an interface avoid a round trip (per-track
// artwork, the folder tree, distinct-value autocomplete beyond list_values),
// and the ones a client uses to build its own concurrency (the event streams).
// None of them are reachable here, and none of them are missed: an assistant
// works by searching, reading a count, changing a selection and checking what
// it did, which is what these twenty do.
//
// Every writing tool takes a selection rather than a file, because that is how
// this API works and because it is what makes the safety rails possible:
// expectCount can only mean something when the server resolves the selection
// itself.

func tools() []*Tool {
	return []*Tool{
		searchTracks, getTrack, getRawTags,
		listAlbums, listArtists, listValues,
		findDuplicates, artworkSummary, libraryStats, lookupAlbum,
		editTracks, splitTitles, renameFiles, stripTags, setArtwork,
		scanLibrary, getJob, listBackups, undoJob, restoreBackup,
	}
}

// --- reading -------------------------------------------------------------

var searchTracks = &Tool{
	Name:  "search_tracks",
	Title: "Search tracks",
	Description: "Search the catalogue and return one page of tracks with the total number of " +
		"matches. The total counts every match, not the page, so it is the number to quote " +
		"before changing anything. Use it to find what is wrong before fixing it: " +
		"artist:~presly for near misses, album: for tracks with no album, -genre:christmas " +
		"to exclude.",
	ReadOnly: true,
	Input: schema(props{
		"query":  str("Search expression. Empty matches the whole library."),
		"sort":   str(`Comma-separated fields, "-" for descending, e.g. "albumartist,album,disc,track".`),
		"limit":  integer("Tracks to return. Default 20, maximum 200."),
		"offset": integer("How many matches to skip, for paging."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Query  string `json:"query,omitempty"`
			Sort   string `json:"sort,omitempty"`
			Limit  int    `json:"limit,omitempty"`
			Offset int    `json:"offset,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.List(library.ListParams{
			Query: a.Query, Sort: a.Sort,
			Limit: page(a.Limit, 20, 200), Offset: a.Offset,
		}), nil
	},
}

var getTrack = &Tool{
	Name:  "get_track",
	Title: "Get one track",
	Description: "Every field of one track, including the version string that identifies the " +
		"file's state on disk.",
	ReadOnly: true,
	Input:    schema(props{"id": str("The track id, as returned by search_tracks.")}, "id"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.Get(a.ID)
	},
}

var getRawTags = &Tool{
	Name:  "get_raw_tags",
	Title: "Read a file's raw metadata",
	Description: "Every tag actually present in one file — ID3 frames, MP4 atoms, Vorbis " +
		"comments — rather than the fields the catalogue keeps. This is how to find out what " +
		"a strip would remove, and why a player shows something the catalogue does not.",
	ReadOnly: true,
	Input:    schema(props{"id": str("The track id.")}, "id"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.RawTags(a.ID)
	},
}

var listAlbums = &Tool{
	Name:  "list_albums",
	Title: "List albums",
	Description: "Albums rather than tracks, each with its track count, how many of them carry " +
		"artwork, and a query that reselects exactly that album.",
	ReadOnly: true,
	Input: schema(props{
		"query":  str("Restrict to albums containing matching tracks."),
		"sort":   str(`Comma-separated, "-" for descending, e.g. "-year".`),
		"limit":  integer("Default 20, maximum 200."),
		"offset": integer("How many to skip."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		p, err := listArgs(raw)
		if err != nil {
			return nil, err
		}
		return svc.Albums(p), nil
	},
}

var listArtists = &Tool{
	Name:        "list_artists",
	Title:       "List artists",
	Description: "Artists with their track and album counts. Useful for spotting the same act spelled two ways.",
	ReadOnly:    true,
	Input: schema(props{
		"query":  str("Restrict to artists with matching tracks."),
		"sort":   str(`Comma-separated, "-" for descending.`),
		"limit":  integer("Default 20, maximum 200."),
		"offset": integer("How many to skip."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		p, err := listArgs(raw)
		if err != nil {
			return nil, err
		}
		return svc.Artists(p), nil
	},
}

var listValues = &Tool{
	Name:  "list_values",
	Title: "Distinct values of a field",
	Description: "Every distinct value of one field with the number of tracks holding it. This " +
		"is the fastest way to find inconsistent tagging: list the artists and the misspelling " +
		"with four tracks sits next to the correct one with four hundred.",
	ReadOnly: true,
	Input: schema(props{
		"field":  str("Field name: artist, albumartist, album, genre, composer, year, and the sort forms."),
		"prefix": str("Only values beginning with this."),
		"limit":  integer("Default 50, maximum 500."),
	}, "field"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Field  string `json:"field"`
			Prefix string `json:"prefix,omitempty"`
			Limit  int    `json:"limit,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		values, err := svc.Values(a.Field, a.Prefix, page(a.Limit, 50, 500))
		if err != nil {
			return nil, err
		}
		return map[string]any{"field": a.Field, "values": values}, nil
	},
}

var findDuplicates = &Tool{
	Name:  "find_duplicates",
	Title: "Find duplicate recordings",
	Description: "Groups of tracks that appear to be the same recording, with what the copies " +
		"occupy and what removing them would free. Each group carries a query that reselects it.",
	ReadOnly: true,
	Input: schema(props{
		"query":           str("Restrict the search to matching tracks."),
		"by":              strList(`Fields that must match. Default ["artist","title"]; "duration" and "size" are also accepted.`),
		"durationSeconds": integer("Bucket size when duration is part of the key. Default 2, because two rips of one track differ by a few hundred milliseconds."),
		"limit":           integer("Groups to return. Default 20, maximum 200."),
		"offset":          integer("How many to skip."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Query           string   `json:"query,omitempty"`
			By              []string `json:"by,omitempty"`
			DurationSeconds int      `json:"durationSeconds,omitempty"`
			Limit           int      `json:"limit,omitempty"`
			Offset          int      `json:"offset,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.Duplicates(library.DuplicateParams{
			Query: a.Query, By: a.By, DurationSeconds: a.DurationSeconds,
			Limit: page(a.Limit, 20, 200), Offset: a.Offset,
		}), nil
	},
}

var artworkSummary = &Tool{
	Name:  "artwork_summary",
	Title: "Summarise cover art",
	Description: "The distinct covers across a selection, grouped so that one image repeated " +
		"across an album counts once, plus how many tracks have no artwork at all. Read this " +
		"before set_artwork: it says what is there without sending any image bytes.",
	ReadOnly: true,
	Input:    schema(selectorProps("summarise")),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a selectorArgs
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.ArtworkSummary(a.selector())
	},
}

var libraryStats = &Tool{
	Name:  "library_stats",
	Title: "Library and server summary",
	Description: "What the library holds — track, artist and album counts, formats, total size " +
		"and duration, and how many tracks are missing each field — together with when it was " +
		"last scanned, what this build can do (editable field names, page limits, whether " +
		"undo journals and the Discogs lookup are available), and whether your token may " +
		"write. Worth reading first: the " +
		"missing-field counts are usually where the work is, and nothing watches the " +
		"filesystem, so the scan time says how current these numbers are.",
	ReadOnly: true,
	Input:    schema(props{}),
	Call: func(ctx context.Context, svc *library.Service, opts Options, raw json.RawMessage) (any, error) {
		var a struct{}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return map[string]any{
			"library": svc.Stats(),
			"scan":    svc.ScanStatus(),
			// What this caller may do, rather than what the server can. A
			// read-only token is shown only the read-only tools, and this is
			// where it can find out why.
			"access": auth.RoleOf(ctx),
			"server": svc.Capabilities(library.Serving{
				AuthRequired: opts.AuthRequired,
				CrossOrigin:  opts.CrossOrigin,
				MCP:          true,
			}),
		}, nil
	},
}

var lookupAlbum = &Tool{
	Name:  "lookup_album",
	Title: "Look an album up on Discogs",
	Description: "The year, genre and styles Discogs holds for one release, for filling gaps " +
		"rather than for overwriting what is already there. This is the only tool that reaches " +
		"outside this machine, and it is rate limited; it is unavailable on a server started " +
		"with -no-discogs.",
	ReadOnly:  true,
	OpenWorld: true,
	Input: schema(props{
		"artist": str("The album artist."),
		"album":  str("The album title."),
	}, "artist", "album"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Artist string `json:"artist"`
			Album  string `json:"album"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.DiscogsAlbum(ctx, a.Artist, a.Album)
	},
}

// --- writing -------------------------------------------------------------

var editTracks = &Tool{
	Name:  "edit_tracks",
	Title: "Edit fields across a selection",
	Description: "Write the same field values into every selected track. This is the main " +
		"writing tool and the reason the selection model exists: correcting a misspelled " +
		"artist across two thousand files is one call. A null value clears a field. " +
		"Records an undo journal unless told not to.",
	Destructive: true,
	Input: schema(merge(selectorProps("edit"), props{
		"set": mapOfNullableStrings("Field values to write, keyed by field name: " +
			"title, artist, albumartist, album, genre, composer, comment, year, track, " +
			"tracktotal, disc, disctotal, compilation, and the sort forms. " +
			"A null value clears the field."),
		"dryRun": flag("Report what would change without writing. Defaults to true — pass false to write."),
		"backup": flag("Record the overwritten values so the job can be undone. Defaults to true."),
	}), "set"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			selectorArgs
			Set    library.Changes `json:"set"`
			DryRun *bool           `json:"dryRun,omitempty"`
			Backup *bool           `json:"backup,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.BatchSet(library.BatchSetRequest{
			Selector: a.selector(), Set: a.Set,
			DryRun: yes(a.DryRun), Backup: yes(a.Backup),
		})
		return settle(ctx, svc, job, err, jobWait)
	},
}

var splitTitles = &Tool{
	Name:  "split_titles",
	Title: "Pull fields out of a title",
	Description: `Rewrite each selected track's tags from the shape of its title, naming ` +
		`fields with a leading $: "$artist - $title" turns "Elvis Presley - Hound Dog" into ` +
		`an artist and a title. A dry run reports how many titles the template did not fit ` +
		`("unmatched") and shows worked examples — read those before writing, because a ` +
		`template that fits badly writes badly across everything selected.`,
	Destructive: true,
	Input: schema(merge(selectorProps("split"), props{
		"template": str(`The title's shape, e.g. "$artist - $title". A literal dollar is "$$".`),
		"dryRun":   flag("Defaults to true. Pass false to write."),
		"backup":   flag("Record the titles being rewritten. Defaults to true."),
	}), "template"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			selectorArgs
			Template string `json:"template"`
			DryRun   *bool  `json:"dryRun,omitempty"`
			Backup   *bool  `json:"backup,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.Split(library.SplitRequest{
			Selector: a.selector(), Template: a.Template,
			DryRun: yes(a.DryRun), Backup: yes(a.Backup),
		})
		return settle(ctx, svc, job, err, jobWait)
	},
}

var renameFiles = &Tool{
	Name:  "rename_files",
	Title: "Rename files from their tags",
	Description: `Move each selected file to a path built from its own tags: ` +
		`"$albumartist/$album/$track $title" files a library. The extension is never part of ` +
		`the template — a rename may not change a file's container — and the destination is ` +
		`relative to the file's root. This moves files on disk, so run it dry first and read ` +
		`the samples.`,
	Destructive: true,
	Input: schema(merge(selectorProps("rename"), props{
		"template": str(`The destination path, e.g. "$albumartist/$album/$track $title".`),
		"dryRun":   flag("Defaults to true. Pass false to move the files."),
		"backup":   flag("Record where each file came from. Defaults to true."),
	}), "template"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			selectorArgs
			Template string `json:"template"`
			DryRun   *bool  `json:"dryRun,omitempty"`
			Backup   *bool  `json:"backup,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.RenameTracks(library.RenameTracksRequest{
			Selector: a.selector(), Template: a.Template,
			DryRun: yes(a.DryRun), Backup: yes(a.Backup),
		})
		return settle(ctx, svc, job, err, jobWait)
	},
}

var stripTags = &Tool{
	Name:  "strip_tags",
	Title: "Remove every tag but a keep list",
	Description: "Delete all metadata a file carries except a keep list — the way to clear " +
		"embedded lyrics, ratings, player junk and stray artwork out of a library. A dry run " +
		"reports what would go, grouped by tag, with the bytes it occupies. This permanently " +
		"discards data, so leave backup on unless you are certain.",
	Destructive: true,
	Input: schema(merge(selectorProps("strip"), props{
		"keep":      strList("Replace the default keep list entirely. Names may be canonical (\"albumartist\") or native to a format (\"TPE2\", \"aART\")."),
		"also":      strList("Add to the default keep list rather than replacing it."),
		"dryRun":    flag("Defaults to true. Pass false to remove the tags."),
		"backup":    flag("Record what was removed so it can be restored. Defaults to true."),
		"normalize": flag("Also rewrite kept fields the file holds in an unusual form. Off by default: rewriting a field nobody asked to change is what field-level edits exist to avoid."),
	})),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			selectorArgs
			Keep      []string `json:"keep,omitempty"`
			Also      []string `json:"also,omitempty"`
			DryRun    *bool    `json:"dryRun,omitempty"`
			Backup    *bool    `json:"backup,omitempty"`
			Normalize *bool    `json:"normalize,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.Strip(library.StripRequest{
			Selector: a.selector(), Keep: a.Keep, Also: a.Also,
			DryRun: yes(a.DryRun), Backup: yes(a.Backup), Normalize: no(a.Normalize),
		})
		return settle(ctx, svc, job, err, jobWait)
	},
}

var setArtwork = &Tool{
	Name:  "set_artwork",
	Title: "Set or clear cover art",
	Description: "Embed a cover across a selection, or take it off. \"folder\" uses the " +
		"cover.jpg or folder.jpg beside each track, which is how a downloaded library " +
		"usually stores art and the usual reason none of it shows on a phone; " +
		"\"clipboard\" uses the image on the server's clipboard; \"remove\" clears it. " +
		"Uploading an image is not offered here — that needs the HTTP API.",
	Destructive: true,
	Input: schema(merge(selectorProps("change the artwork of"), props{
		"source": enum("Where the cover comes from.", "folder", "clipboard", "remove"),
		"dryRun": flag("Defaults to true. Pass false to write."),
		"backup": flag("Record the covers being replaced. Off by default: a journal of whole images is large, so ask for it only when the paste is one you might take back."),
	}), "source"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			selectorArgs
			Source string `json:"source"`
			DryRun *bool  `json:"dryRun,omitempty"`
			Backup *bool  `json:"backup,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.BatchArtwork(library.BatchArtworkRequest{
			Selector: a.selector(), Source: library.ArtworkSource(a.Source),
			DryRun: yes(a.DryRun), Backup: no(a.Backup),
		})
		return settle(ctx, svc, job, err, jobWait)
	},
}

// --- jobs, scanning and recovery -----------------------------------------

var scanLibrary = &Tool{
	Name:  "scan_library",
	Title: "Bring the catalogue up to date",
	Description: "Walk the music directories and refresh the catalogue. Nothing watches the " +
		"filesystem, so music added or edited elsewhere is invisible until this runs. It is " +
		"incremental — an unchanged library costs one stat per file — and it is the one job " +
		"here that usually outlives the call, so expect a job id and poll get_job.",
	Idempotent: true,
	Input: schema(props{
		"roots": strList("Directories to walk. Empty refreshes whatever the catalogue already covers, which is the usual case."),
		"full":  flag("Re-read every file instead of reusing entries whose size and modification time are unchanged."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Roots []string `json:"roots,omitempty"`
			Full  *bool    `json:"full,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.Scan(library.ScanRequest{Roots: a.Roots, Full: no(a.Full)})
		return settle(ctx, svc, job, err, 3*time.Second)
	},
}

var getJob = &Tool{
	Name:  "get_job",
	Title: "Poll a job",
	Description: "The state, progress and result of a job started earlier. Finished jobs stay " +
		"queryable for an hour; after that use list_backups to find what an operation wrote.",
	ReadOnly: true,
	Input: schema(props{
		"id":          str("The job id."),
		"waitSeconds": integer("Wait up to this many seconds for the job to finish before answering. Default 0, maximum 60."),
	}, "id"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			ID          string `json:"id"`
			WaitSeconds int    `json:"waitSeconds,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.JobRegistry().Get(a.ID)
		wait := time.Duration(min(max(a.WaitSeconds, 0), 60)) * time.Second
		return settle(ctx, svc, job, err, wait)
	},
}

var listBackups = &Tool{
	Name:  "list_backups",
	Title: "List undo journals",
	Description: "The journals written by earlier operations, newest first, each naming the job " +
		"it belongs to and how many tracks it covers. These outlive the hour a job stays " +
		"queryable, so this is how yesterday's edit is found.",
	ReadOnly: true,
	Input: schema(props{
		"limit":  integer("Default 20, maximum 200."),
		"offset": integer("How many to skip."),
	}),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			Limit  int `json:"limit,omitempty"`
			Offset int `json:"offset,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		return svc.Backups(page(a.Limit, 20, 200), a.Offset)
	},
}

var undoJob = &Tool{
	Name:  "undo_job",
	Title: "Undo a job",
	Description: "Reverse what a job did, using the journal it recorded. Works on any job that " +
		"wrote one, including one that was cancelled halfway.",
	Destructive: true,
	Input:       schema(props{"jobId": str("The id of the job to reverse.")}, "jobId"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			JobID string `json:"jobId"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.Undo(a.JobID)
		return settle(ctx, svc, job, err, jobWait)
	},
}

var restoreBackup = &Tool{
	Name:  "restore_backup",
	Title: "Restore from a journal",
	Description: "Put back what one journal holds, found through list_backups. The same work as " +
		"undo_job and a different question: this is the one to use when the job is older than " +
		"the hour jobs are kept for.",
	Destructive: true,
	Input: schema(props{
		"backupId": str("The journal id, from list_backups."),
		"dryRun":   flag("Report what would be put back without writing. Defaults to true."),
	}, "backupId"),
	Call: func(ctx context.Context, svc *library.Service, _ Options, raw json.RawMessage) (any, error) {
		var a struct {
			BackupID string `json:"backupId"`
			DryRun   *bool  `json:"dryRun,omitempty"`
		}
		if err := decodeArgs(raw, &a); err != nil {
			return nil, err
		}
		job, err := svc.Restore(library.RestoreRequest{BackupID: a.BackupID, DryRun: yes(a.DryRun)})
		return settle(ctx, svc, job, err, jobWait)
	},
}

// --- shared argument handling --------------------------------------------

// listArgs decodes the query/sort/limit/offset shape the browse tools share.
func listArgs(raw json.RawMessage) (library.ListParams, error) {
	var a struct {
		Query  string `json:"query,omitempty"`
		Sort   string `json:"sort,omitempty"`
		Limit  int    `json:"limit,omitempty"`
		Offset int    `json:"offset,omitempty"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return library.ListParams{}, err
	}
	return library.ListParams{
		Query: a.Query, Sort: a.Sort,
		Limit: page(a.Limit, 20, 200), Offset: a.Offset,
	}, nil
}

// page bounds a requested page size.
//
// The ceilings here are lower than the API's own, on purpose. A page of a
// thousand tracks is reasonable for a client that renders a list and useless
// to one that has to read every row: it costs a great deal of context to say
// what the total already said.
func page(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// jobWait is how long a tool waits for the job it started before giving up and
// handing back an id.
//
// It exists because almost every one of these jobs finishes at once — a batch
// edit over a few hundred tracks is done in well under a second — and making a
// model poll for a result that is already there wastes a round trip and
// invites it to report work it has not seen finish. A scan is the exception,
// and has its own shorter wait, because it genuinely does run for minutes.
const jobWait = 15 * time.Second

// jobReply is a job plus the one thing a caller needs when the wait ran out.
type jobReply struct {
	*library.Job
	Note string `json:"note,omitempty"`
}

// settle waits for a job to reach a terminal state and reports it.
//
// It takes the error from starting the job so that every writing tool can end
// with one line. A job that is still running when the wait expires is returned
// as it stands, with the note that says what to do about it.
func settle(ctx context.Context, svc *library.Service, j *library.Job, err error, wait time.Duration) (any, error) {
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, errNoJob
	}
	deadline := time.Now().Add(wait)
	for interval := 10 * time.Millisecond; j.State == library.JobRunning; {
		if !time.Now().Before(deadline) {
			return jobReply{j, "still running; poll get_job with this id"}, nil
		}
		select {
		case <-ctx.Done():
			return jobReply{j, "still running; poll get_job with this id"}, nil
		case <-time.After(interval):
		}
		if interval < 250*time.Millisecond {
			interval *= 2
		}
		next, getErr := svc.JobRegistry().Get(j.ID)
		if getErr != nil {
			return jobReply{j, "the job is no longer queryable"}, nil
		}
		j = next
	}
	return jobReply{Job: j}, nil
}
