package library

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/remy/yamo/internal/catalog"
)

// Splitting a title into the fields it actually contains.
//
// A compilation ripped one album per track usually says Various Artists in the
// artist tag, because that is what the album is, and then has nowhere to put
// the performer except the title: "Michael Jackson - Beat It". Every player
// then shows the artist twice over and none of them can group by it.
//
// The template describes the shape the title is in rather than a rule for
// finding artists, because there is no such rule — " - " separates the two on
// one compilation and " / " on the next, and a title may contain a dash of its
// own. Saying "$artist - $title" is exact, and a title that does not fit is
// left alone rather than guessed at.

// ErrBadTemplate means the template could not be understood. It is separate so
// the API can answer 400 rather than 500: the request is wrong, not the server.
var ErrBadTemplate = errors.New("library: the template cannot be used")

// maxSplitSamples bounds the worked examples a result carries. They exist so a
// dry run can show what a template does to real titles before it is applied;
// three is enough to see whether it is right and small enough to send.
const maxSplitSamples = 3

// SplitRequest asks for each matched track's title to be pulled apart.
type SplitRequest struct {
	Selector Selector `json:"selector"`

	// Template is the title's shape, naming fields with a leading $:
	// "$artist - $title". A literal dollar is written "$$".
	Template string `json:"template"`

	// DryRun reports what would happen without writing. Worth doing first: a
	// template that fits nothing writes nothing, and one that fits badly
	// writes badly across everything selected.
	DryRun bool `json:"dryRun,omitempty"`

	// Backup records the titles being rewritten so the job can be undone. It
	// defaults to true: a split writes a different value into every file, so
	// there is nothing a client could send to put them back by hand.
	Backup bool `json:"backup"`
}

// SplitSample is one title and what the template made of it.
type SplitSample struct {
	Title  string            `json:"title"`
	Fields map[string]string `json:"fields"`
}

// SplitResult reports what a split did or would do.
type SplitResult struct {
	BatchResult

	Template string `json:"template"`

	// Fields names what the template writes, in the order it names them.
	Fields []string `json:"fields"`

	// Unmatched counts titles the template did not fit. They are not failures
	// and not skips: nothing was wrong with the file, the template simply does
	// not describe it, and that is the number that says whether it is right.
	Unmatched int `json:"unmatched"`

	Samples []SplitSample `json:"samples,omitempty"`
}

// splitRule is a compiled template.
type splitRule struct {
	re     *regexp.Regexp
	fields []string // one per capture group, in order
}

// compileSplit turns a template into a matcher.
//
// Every capture but the last is non-greedy, which is what makes "$artist -
// $title" do the right thing with "Jay-Z - 99 Problems": the artist stops at
// the first separator that matches the literal in full, and the title keeps
// everything after it, dashes and all. The separators are matched exactly —
// making the spaces optional would let the dash inside "Jay-Z" serve as one.
func compileSplit(tmpl string) (*splitRule, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return nil, fmt.Errorf("%w: it is empty", ErrBadTemplate)
	}

	var (
		pattern strings.Builder
		lit     strings.Builder
		fields  []string
	)
	flush := func() {
		if lit.Len() > 0 {
			pattern.WriteString(regexp.QuoteMeta(lit.String()))
			lit.Reset()
		}
	}

	for i := 0; i < len(tmpl); {
		if tmpl[i] != '$' {
			lit.WriteByte(tmpl[i])
			i++
			continue
		}
		if i+1 < len(tmpl) && tmpl[i+1] == '$' {
			lit.WriteByte('$') // an escaped dollar, for a title that has one
			i += 2
			continue
		}

		j := i + 1
		for j < len(tmpl) && (tmpl[j] >= 'a' && tmpl[j] <= 'z') {
			j++
		}
		name := tmpl[i+1 : j]
		if name == "" {
			return nil, fmt.Errorf("%w: a $ with no field name after it", ErrBadTemplate)
		}
		f, ok := catalog.LookupField(name)
		if !ok || !f.Editable() {
			return nil, fmt.Errorf("%w: %q is not a field that can be written", ErrBadTemplate, name)
		}
		for _, have := range fields {
			if have == name {
				return nil, fmt.Errorf("%w: $%s appears twice", ErrBadTemplate, name)
			}
		}

		flush()
		pattern.WriteString("(.+)")
		fields = append(fields, catalog.FieldNames[f])
		i = j
	}
	flush()

	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: it names no fields, so there is nothing to split into", ErrBadTemplate)
	}
	// "$title" alone copies the title onto itself. Anything else with one
	// field is a real request: "$title (live)" trims a suffix, "$artist -
	// $title" is the usual two.
	if tmpl == "$title" {
		return nil, fmt.Errorf("%w: $title on its own would only copy the title onto itself", ErrBadTemplate)
	}

	// Every capture but the last gives up as early as it can, so the first
	// separator wins and the remainder falls to the field at the end.
	body := strings.Replace(pattern.String(), "(.+)", "(.+?)", len(fields)-1)

	re, err := regexp.Compile("^" + body + "$")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadTemplate, err)
	}
	return &splitRule{re: re, fields: fields}, nil
}

// apply pulls one title apart. It returns nil when the template does not fit,
// and drops any field whose value came out empty rather than writing a blank
// over something that was there.
func (r *splitRule) apply(title string) map[string]string {
	m := r.re.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(r.fields))
	for i, f := range r.fields {
		if v := strings.TrimSpace(m[i+1]); v != "" {
			out[f] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Split rewrites each matched track's title into the fields the template names.
//
// A job rather than a loop in the client, for the reason every bulk operation
// here is one: the selection may be a query matching thousands of tracks that
// the client has never seen, and each one needs a different set of values —
// which is exactly what the batch edit endpoint cannot express.
func (s *Service) Split(req SplitRequest) (*Job, error) {
	rule, err := compileSplit(req.Template)
	if err != nil {
		return nil, err
	}
	ids, err := s.Resolve(req.Selector)
	if err != nil {
		return nil, err
	}

	var jrn *journal
	if req.Backup && !req.DryRun {
		jrn = s.tryJournal(JournalEdit)
	}

	return s.jobs.StartWithJournal(JobSplit, jrn.ID(), func(ctx context.Context, j *Job) (any, error) {
		defer jrn.Close(j.ID)
		res := SplitResult{
			BatchResult: BatchResult{Matched: len(ids), DryRun: req.DryRun, BackupID: jrn.ID()},
			Template:    strings.TrimSpace(req.Template),
			Fields:      rule.fields,
		}
		j.SetProgress(Progress{Total: int64(len(ids))})

		var touched []string
		for n, id := range ids {
			if ctx.Err() != nil {
				break
			}
			t, err := s.Get(id)
			if err != nil {
				res.Skipped++
				continue
			}

			vals := rule.apply(t.Title)
			if vals == nil {
				res.Unmatched++
				continue
			}
			if len(res.Samples) < maxSplitSamples {
				res.Samples = append(res.Samples, SplitSample{Title: t.Title, Fields: vals})
			}

			ch := make(Changes, len(vals))
			for f, v := range vals {
				value := v
				ch[f] = &value
			}
			changed, err := s.applyOne(id, ch, "", req.DryRun, jrn)
			switch {
			case errors.Is(err, ErrNotFound):
				res.Skipped++
			case err != nil:
				path, _ := s.Path(id)
				res.fail(id, path, err)
			case changed:
				res.Changed++
				touched = append(touched, id)
			default:
				// The title already reads the way the template would leave it.
				res.Skipped++
			}
			if n%64 == 0 || n == len(ids)-1 {
				j.SetProgress(Progress{Done: int64(n + 1), Total: int64(len(ids))})
			}
		}

		if len(touched) > 0 {
			s.markDirty()
			s.events.publish(Event{Type: EventTracksChanged, TrackIDs: touched})
		}
		return res, ctx.Err()
	}), nil
}
