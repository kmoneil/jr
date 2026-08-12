package render

import (
	"sort"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
)

// Format is an output encoding. All four are available on every command; the
// per-content defaults are a convenience, the --format flag is the contract.
type Format string

// The supported output formats.
const (
	// TSV is the default for collections: rectangular data is rectangular, and
	// TSV is the cheapest encoding of it by a wide margin.
	TSV Format = "tsv"
	// XML is the default for records and documents: mixed content costs no
	// escaping tax and the payload is self-describing.
	XML Format = "xml"
	// JSON is for downstream systems, jq, and HTTP handoff.
	JSON Format = "json"
	// YAML is for humans editing what they read.
	YAML Format = "yaml"
)

var formats = []Format{TSV, XML, JSON, YAML}

// Formats returns every supported format, in a stable order.
func Formats() []Format {
	out := make([]Format, len(formats))
	copy(out, formats)
	return out
}

// Presentational reports whether a format is presentation rather than contract.
//
// Four formats are versioned and will not change shape without a major bump.
// `markdown`, where the build has it, is explicitly outside that promise and may
// change in any release. A caller listing formats to a human needs to be able to
// say which is which, and asking here means the answer cannot drift from the
// registration: a build without the tag has no such format to report.
func Presentational(f Format) bool { return f == presentationalFormat }

// FormatNames returns every supported format as strings, for flag enums and
// schema output.
func FormatNames() []string {
	out := make([]string, 0, len(formats))
	for _, f := range formats {
		out = append(out, string(f))
	}
	return out
}

// ParseFormat resolves a --format or JIRA_FORMAT value. An unrecognized value
// is a usage error listing the valid ones, never a silent fallback to a default.
func ParseFormat(s string) (Format, error) {
	want := Format(strings.ToLower(strings.TrimSpace(s)))
	for _, f := range formats {
		if want == f {
			return f, nil
		}
	}
	valid := FormatNames()
	sort.Strings(valid)
	return "", errs.Usage("INVALID_FORMAT", "unknown output format %q", s).
		WithDetail("valid formats: %s", strings.Join(valid, ", ")).
		WithRemedy("pass --format with one of the listed values")
}

// DefaultFor returns the format a document uses when none was requested:
// TSV for collections, XML for records and documents.
func DefaultFor(d *Doc) Format {
	if d != nil && d.Collection != nil {
		return TSV
	}
	return XML
}
