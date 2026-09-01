package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/remy/yamo/internal/tags"
)

// Writing embedded artwork back out to the folder.
//
// The batch artwork endpoint has always been able to read a cover.jpg into the
// files beside it, which is what a downloaded library needs. This is the other
// direction, and it is what a library edited here needs: the covers are in the
// files, correct and identical across the album, and every program that reads
// a folder rather than a tag — a media server's poster, a file browser's
// thumbnail, the next tool along — sees nothing.
//
// It is one write per directory rather than one per track. An album's tracks
// carry the same cover, so writing it once is not an optimisation but the
// correct behaviour: the alternative would rewrite the same file ten times.

// ExportArtworkRequest writes each selected track's cover out beside it.
type ExportArtworkRequest struct {
	Selector Selector `json:"selector"`

	// Filename is what the image is called in each directory. It must be a
	// bare name — this writes beside the music, not anywhere a client names —
	// and its extension is replaced with the image's own, since a PNG cover
	// written as cover.jpg would be a lie the same way a renamed FLAC is.
	Filename string `json:"filename,omitempty"`

	// Overwrite replaces an image already there. Off by default: a folder
	// that already has a cover.jpg usually has the one somebody chose, and
	// the embedded art may be the worse of the two.
	Overwrite bool `json:"overwrite,omitempty"`

	// DryRun reports what would be written without writing it.
	DryRun bool `json:"dryRun,omitempty"`
}

// ExportResult reports what an export did or would do.
type ExportResult struct {
	BatchResult

	// Directories counts the folders considered, which is the unit of work
	// here — Changed counts the files written, and the two differ by the
	// folders that already had a cover.
	Directories int `json:"directories"`

	// Filename is the name actually used, after the extension was corrected
	// to match the images being written.
	Filename string `json:"filename"`

	// Written names a few of the files, for a dry run to show what it would
	// do rather than only how much.
	Written []string `json:"written,omitempty"`

	// NoArtwork counts directories whose tracks carry no embedded cover, so
	// there was nothing to write out.
	NoArtwork int `json:"withoutArtwork"`

	// Existing counts directories skipped because an image was already there
	// and Overwrite was not set.
	Existing int `json:"existing"`
}

// maxExportSamples bounds the example paths a result carries.
const maxExportSamples = 10

// defaultCoverName is what almost everything looks for first.
const defaultCoverName = "cover.jpg"

// ExportArtwork starts a job that writes each selection's cover into its folder.
func (s *Service) ExportArtwork(req ExportArtworkRequest) (*Job, error) {
	name := strings.TrimSpace(req.Filename)
	if name == "" {
		name = defaultCoverName
	}
	if err := validCoverName(name); err != nil {
		return nil, err
	}
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}

	return s.jobs.Start(JobExport, func(ctx context.Context, j *Job) (any, error) {
		res := ExportResult{
			BatchResult: BatchResult{Matched: len(ids), DryRun: req.DryRun},
			Filename:    name,
		}

		// Group first: an album is one write, and grouping before reading any
		// cover means one file is opened per directory rather than one per
		// track.
		dirs := map[string][]string{}
		var order []string
		for _, id := range ids {
			path, err := s.Path(id)
			if err != nil {
				res.Skipped++
				continue
			}
			dir := filepath.Dir(path)
			if _, seen := dirs[dir]; !seen {
				order = append(order, dir)
			}
			dirs[dir] = append(dirs[dir], id)
		}
		sort.Strings(order)
		res.Directories = len(order)
		j.SetProgress(Progress{Total: int64(len(order))})

		for n, dir := range order {
			if ctx.Err() != nil {
				break
			}
			written, outcome, err := s.exportOne(dir, dirs[dir], name, req.Overwrite, req.DryRun)
			switch {
			case err != nil:
				res.fail("", dir, err)
			case outcome == exportWrote:
				res.Changed++
				if len(res.Written) < maxExportSamples {
					res.Written = append(res.Written, written)
				}
				// The name is settled by the first image actually written:
				// the extension follows the picture rather than the request.
				res.Filename = filepath.Base(written)
			case outcome == exportExists:
				res.Existing++
				res.Skipped++
			default:
				res.NoArtwork++
				res.Skipped++
			}
			if n%16 == 0 || n == len(order)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(order))})
			}
		}
		return res, ctx.Err()
	}), nil
}

type exportOutcome int

const (
	exportNone exportOutcome = iota
	exportWrote
	exportExists
)

// exportOne writes one directory's cover, taking it from the first track in
// that directory that has one.
func (s *Service) exportOne(dir string, ids []string, name string, overwrite, dryRun bool) (string, exportOutcome, error) {
	var pic *tags.Picture
	for _, id := range ids {
		path, err := s.Path(id)
		if err != nil {
			continue
		}
		if p, err := tags.ReadCover(path); err == nil && p != nil && len(p.Data) > 0 {
			pic = p
			break
		}
	}
	if pic == nil {
		return "", exportNone, nil
	}

	// The extension follows the image, not the request: writing a PNG as
	// cover.jpg would leave every reader to sniff it or get it wrong.
	target := filepath.Join(dir, strings.TrimSuffix(name, filepath.Ext(name))+pic.Ext())

	if _, err := os.Lstat(target); err == nil {
		if !overwrite {
			return target, exportExists, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", exportNone, err
	}
	if dryRun {
		return target, exportWrote, nil
	}

	// Written through a temporary file and renamed into place, so an
	// interrupted write leaves the old cover rather than a truncated one.
	err := s.locks.withPath(target, func() error {
		tmp, err := os.CreateTemp(dir, ".yamo-cover-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)

		if _, err := tmp.Write(pic.Data); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmpName, 0o644); err != nil {
			return err
		}
		return os.Rename(tmpName, target)
	})
	if err != nil {
		return "", exportNone, err
	}
	return target, exportWrote, nil
}

// validCoverName refuses a filename that would write anywhere but beside the
// music. The image goes next to the tracks; a name with a path in it would
// make this a way to write arbitrary files anywhere the server can reach.
func validCoverName(name string) error {
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("%w: the filename must be a bare name, not a path", ErrBadPath)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%w: a hidden filename would not be found by anything looking for a cover", ErrBadPath)
	}
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" && !containsString(coverExts, ext) {
		return fmt.Errorf("%w: %s is not an image extension", ErrBadPath, ext)
	}
	return nil
}
