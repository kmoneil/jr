package sprint

// In-package, because normalizeTime is not exported and exporting it to test it
// would widen the resource's surface for the benefit of a test.

import (
	"testing"
	"time"
)

// FuzzNormalizeTimeIsExactOrRefuses is the property the whole layout list exists
// for.
//
// Two deployments write time two ways and neither writes RFC 3339: Data Center
// sends an offset with no colon, Cloud sends Z. Whatever arrives, the value on
// stdout is RFC 3339 in UTC or the command fails — never a third thing, and
// never the server's own spelling passed through, which would make the output
// format depend on which Jira answered.
func FuzzNormalizeTimeIsExactOrRefuses(f *testing.F) {
	for _, seed := range []string{
		"",
		// What the two deployments actually send.
		"2026-07-01T09:00:00.000+0000",
		"2026-07-01T09:00:00.000Z",
		"2026-07-01T09:00:00.123456789Z",
		"2026-07-01T09:00:00-0700",
		"2026-07-01T09:00:00+05:30",
		// Shapes that are nearly right, which is the dangerous kind.
		"2026-07-01T09:00:00.000+00:00",
		"2026-07-01 09:00:00",
		"2026-07-01",
		"last Tuesday",
		"0000-01-01T00:00:00Z",
		"9999-12-31T23:59:59Z",
		"2026-02-30T00:00:00Z",
		"2026-07-01T09:00:00.000-2400",
		"\x00",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out, err := normalizeTime("startDate", in)
		if err != nil {
			if out != "" {
				t.Fatalf("normalizeTime(%q) failed and still returned %q", in, out)
			}
			return
		}

		if in == "" {
			if out != "" {
				t.Fatalf("an absent timestamp became %q; empty means the event has "+
					"not happened, and a zero time would compare as the year 1", out)
			}
			return
		}

		// Whatever came in, what goes out is one shape.
		parsed, perr := time.Parse(time.RFC3339, out)
		if perr != nil {
			t.Fatalf("normalizeTime(%q) = %q, which is not RFC 3339: %v", in, out, perr)
		}
		if _, offset := parsed.Zone(); offset != 0 {
			t.Fatalf("normalizeTime(%q) = %q, which is not UTC", in, out)
		}

		// Idempotent: feeding the output back gives the output. Without this,
		// a normalizer that emitted something merely RFC-3339-shaped but not
		// re-readable would satisfy everything above.
		again, aerr := normalizeTime("startDate", out)
		if aerr != nil {
			t.Fatalf("normalizeTime(%q) = %q, which normalizeTime then rejects: %v",
				in, out, aerr)
		}
		if again != out {
			t.Fatalf("normalizeTime is not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}
