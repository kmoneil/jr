package render

import (
	"io"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// StreamSpec describes the collection a Stream will emit.
type StreamSpec struct {
	Kind    string
	Version int
	// Name is the container element, e.g. "issues".
	Name    string
	Columns []Column
}

// Stream emits a collection, writing rows as they arrive where the format
// allows it and buffering where it does not.
//
// TSV streams: it has a header and then rows, and §3.1 puts its completeness
// signal on stderr and in the exit code rather than in the payload — which is
// precisely what makes emitting a row before the result set is exhausted
// possible. XML, JSON, and YAML cannot: their envelopes carry count and
// complete, and neither is known until the last page lands.
//
// A caller writes to a Stream the same way regardless. That is the point: the
// resource does not branch on format, and the format decides for itself whether
// the caller waits.
type Stream struct {
	w      io.Writer
	format Format
	spec   StreamSpec

	// streaming is true when rows go out as they arrive.
	streaming bool
	// out is the sticky-error writer used while streaming.
	out *writer
	// headerWritten guards against emitting the header twice, and against
	// omitting it for an empty result.
	headerWritten bool

	// items buffers the collection for a format that cannot stream.
	items []*Node
	// firstName is the element name of the first row written, so a stream that
	// has already emitted its rows can still reject a mismatched later one.
	firstName string

	count  int
	closed bool
}

// Streams reports whether a format emits rows as they arrive.
func Streams(f Format) bool { return f == TSV }

// NewStream begins a collection.
//
// The column set is validated here, before a byte is written, so a malformed
// specification fails with nothing emitted. Individual rows are validated as
// they arrive, which is the trade streaming makes: a bad row cannot be caught
// before its predecessors have already been written.
func NewStream(w io.Writer, f Format, spec StreamSpec) (*Stream, error) {
	if !isKnownFormat(f) {
		return nil, errs.Usage("INVALID_FORMAT", "unknown output format %q", string(f))
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}

	s := &Stream{w: w, format: f, spec: spec, streaming: Streams(f)}
	if s.streaming {
		s.out = &writer{w: w}
	}
	return s, nil
}

func validateSpec(spec StreamSpec) error {
	switch {
	case spec.Kind == "":
		return errs.Runtime("INVALID_DOC", "stream has no kind")
	case spec.Version < 1:
		return errs.Runtime("INVALID_DOC", "stream kind %q has no schema version", spec.Kind)
	case spec.Name == "":
		return errs.Runtime("INVALID_DOC", "stream kind %q has no container name", spec.Kind)
	case len(spec.Columns) == 0:
		return errs.Runtime("INVALID_DOC",
			"stream kind %q declares no columns; TSV would have nothing to emit", spec.Kind)
	}
	for _, col := range spec.Columns {
		if col.Header == "" {
			return errs.Runtime("INVALID_DOC",
				"stream kind %q has a column with no header", spec.Kind)
		}
		if err := ValidatePath(col.Path); err != nil {
			return err
		}
	}
	return nil
}

// Write adds items to the collection.
func (s *Stream) Write(items ...*Node) error {
	if s.closed {
		return errs.Runtime("STREAM_CLOSED", "wrote to a closed stream")
	}

	for _, item := range items {
		if item == nil {
			return errs.Runtime("INVALID_DOC", "stream kind %q was given a nil item", s.spec.Kind)
		}
		if err := item.validate(s.spec.Kind + "/" + s.spec.Name); err != nil {
			return err
		}
		// A streamed row is checked here rather than at Close, because by then
		// it is already on stdout. This is the only place a collection's shape
		// can still be refused before a consumer has read it.
		if err := conformTo(s.spec.Kind, item); err != nil {
			return err
		}
		if s.count > 0 || len(s.items) > 0 {
			if name := s.itemName(); name != "" && item.Name != name {
				return errs.Runtime("INVALID_DOC",
					"stream kind %q mixes item elements %q and %q",
					s.spec.Kind, name, item.Name)
			}
		}
	}

	if !s.streaming {
		s.items = append(s.items, items...)
		s.count += len(items)
		return nil
	}

	s.writeHeader()
	for _, item := range items {
		if s.firstName == "" {
			s.firstName = item.Name
		}
		row := make([]string, 0, len(s.spec.Columns))
		for _, col := range s.spec.Columns {
			v, _ := item.Lookup(col.Path)
			row = append(row, v)
		}
		s.out.raw(joinTSV(row))
		s.count++
	}
	return s.out.err
}

func (s *Stream) itemName() string {
	switch {
	case len(s.items) > 0:
		return s.items[0].Name
	case s.firstName != "":
		return s.firstName
	default:
		return ""
	}
}

// Count returns how many items have been written.
func (s *Stream) Count() int { return s.count }

// Close finishes the collection.
//
// complete is the answer to the only question that matters about a result set,
// and it is the caller's to give: only the thing doing the paging knows whether
// it ran out or was cut short.
func (s *Stream) Close(complete bool, nextPageToken string) error {
	if s.closed {
		return nil
	}
	s.closed = true

	if complete && nextPageToken != "" {
		return errs.Runtime("INVALID_DOC",
			"stream kind %q is complete but carries a next-page token", s.spec.Kind)
	}

	if !s.streaming {
		// The envelope needs the count and the completeness, so everything was
		// held until now.
		doc := List(s.spec.Kind, s.spec.Version, &Collection{
			Name:          s.spec.Name,
			Items:         s.items,
			Complete:      complete,
			NextPageToken: nextPageToken,
			Columns:       s.spec.Columns,
		})
		return Write(s.w, doc, s.format)
	}

	// A collection with no rows still has a header: the caller asked for these
	// columns and an empty answer is still an answer.
	s.writeHeader()
	if s.out.err != nil {
		return s.out.err
	}
	return s.flush()
}

func (s *Stream) writeHeader() {
	if s.headerWritten {
		return
	}
	s.headerWritten = true

	headers := make([]string, 0, len(s.spec.Columns))
	for _, col := range s.spec.Columns {
		headers = append(headers, col.Header)
	}
	s.out.raw(joinTSV(headers))
}

func (s *Stream) flush() error {
	if f, ok := s.w.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return errs.Runtime("WRITE_FAILED", "cannot write output").Wrap(err)
		}
	}
	return nil
}

// WriteStreamTruncation emits the warning that accompanies exit 3 for a
// streamed collection.
//
// The rows have already gone to stdout, so this is the only place the caller
// learns the set was not exhausted — which is exactly the arrangement §3.1
// describes for TSV, and why streaming is possible at all.
func WriteStreamTruncation(w io.Writer, kind string, count int, token string, f Format) error {
	return writeDiagnostic(w, truncationNodeFor(kind, count, token), f)
}

// truncationNodeFor builds the warning without needing the document, since a
// streamed collection no longer has one by the time this is called.
func truncationNodeFor(kind string, count int, token string) *Node {
	return truncationNode(&Doc{
		Kind: kind,
		Collection: &Collection{
			Name:          "items",
			Items:         make([]*Node, count),
			NextPageToken: token,
		},
	})
}
