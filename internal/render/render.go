// Package render turns a command result into bytes.
//
// The output shape is a public API. It is versioned, and breaking it requires a
// major bump. Commands build a format-independent Doc; this package is the only
// thing that knows what TSV, XML, JSON, and YAML look like, so a change to the
// contract is a change to one package with golden files guarding it.
package render

import (
	"bufio"
	"io"
	"slices"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// Write encodes a result document to w. It validates the document first, so a
// malformed payload fails loudly rather than emitting output that looks fine
// and parses wrong.
//
// Write emits data only. Truncation warnings and errors go to stderr via
// WriteWarning and WriteError; nothing but the result ever reaches stdout.
func Write(w io.Writer, d *Doc, f Format) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return encode(w, f, func(bw *writer) {
		switch f {
		case XML:
			writeXML(bw, d)
		case TSV:
			writeTSV(bw, d)
		case JSON:
			writeJSONValue(bw, docValue(d))
		case YAML:
			writeYAMLValue(bw, docValue(d))
		default:
			extraDoc[f](bw, d)
		}
	})
}

// extraDoc and extraDiagnostic hold the writers for a format a build tag adds.
//
// The four above are in every build and are named in the switch, because they
// are the output contract and reading the dispatch should show them. A tagged
// format registers here instead, so the format list and the writer set cannot
// disagree — registerFormat is the only way in and it sets all three.
//
// Indexing without the comma-ok form is deliberate: encode has already refused
// anything not in `formats`, and registerFormat is the only thing that adds to
// it. A format in the list with no writer would be a nil map entry and a panic
// here, which is the right outcome for a registration bug and is what
// TestEveryFormatCanWriteEverything exists to catch before it ships.
// registerFormat, which fills them, lives in the build-tagged file that calls
// it: in a build with no tagged format it would have no caller, and
// `make lint-untagged` reports exactly that — the gate added after `echoMode`
// compiled into the reader profile and could never run there.
var (
	extraDoc        = map[Format]func(*writer, *Doc){}
	extraDiagnostic = map[Format]func(*writer, *Node){}
)

// WriteError encodes a structured error to w, which is always stderr.
func WriteError(w io.Writer, e *errs.Error, f Format) error {
	if e == nil {
		return nil
	}
	return writeDiagnostic(w, errorNode(e), f)
}

// WriteTruncationWarning encodes the warning that accompanies exit 3 to w,
// which is always stderr. It is a no-op for a result that is complete.
func WriteTruncationWarning(w io.Writer, d *Doc, f Format) error {
	if d == nil || d.IsComplete() {
		return nil
	}
	return writeDiagnostic(w, truncationNode(d), f)
}

func writeDiagnostic(w io.Writer, n *Node, f Format) error {
	return encode(w, f, func(bw *writer) {
		switch f {
		case XML:
			writeXMLDiagnostic(bw, n)
		case TSV:
			writeTSVRows(bw, []string{"field", "value"}, nodeRows(n))
		case JSON:
			writeJSONValue(bw, diagnosticValue(n))
		case YAML:
			writeYAMLValue(bw, diagnosticValue(n))
		default:
			extraDiagnostic[f](bw, n)
		}
	})
}

// encode runs body against a buffered writer and flushes it, collapsing the
// sticky write error and the flush error into one result.
func encode(w io.Writer, f Format, body func(*writer)) error {
	if !isKnownFormat(f) {
		return errs.Usage("INVALID_FORMAT", "unknown output format %q", string(f))
	}
	bw := bufio.NewWriter(w)
	out := &writer{w: bw}
	body(out)
	if out.err != nil {
		return out.err
	}
	if err := bw.Flush(); err != nil {
		return errs.Runtime("WRITE_FAILED", "cannot write output").Wrap(err)
	}
	return nil
}

func isKnownFormat(f Format) bool {
	return slices.Contains(formats, f)
}

// WriteWarning emits a structured warning, in the requested format.
//
// It goes to stderr, always, and a caller passing stdout would break the rule
// that stdout carries the result and nothing else. It is a warning rather than
// an error because the command continues: a possible duplicate is worth saying
// and is not worth refusing over.
func WriteWarning(w io.Writer, code, message string, f Format) error {
	return writeDiagnostic(w, warningNode(code, message), f)
}
