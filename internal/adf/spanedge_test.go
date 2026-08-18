package adf

import "testing"

// FuzzSplitHelpersMatchJoined holds the two-piece span-edge helpers to the
// single-string functions they replaced.
//
// beforeOf and endsWithLiveOf exist so renderInline can ask what a span sits
// against without building the string it sits at the end of — the
// concatenation that made the converter quadratic. That is only a safe trade
// if the pair answers exactly what the joined form answered, and "exactly" is
// not a thing to establish by reading: the escape rule is a parity, and a
// parity is easy to state and easy to get one off.
//
// So the originals stay, and they are the specification. This is a
// differential target rather than a property one — it does not describe what
// the right answer is, it says the new code and the old code agree on every
// input — which is the strongest form available when a rewrite is meant to
// preserve behaviour and the previous implementation still compiles.
//
// This target lives in package adf rather than adf_test because the helpers are
// unexported, and in an untagged file so `make fuzz` can see it. A target
// behind a build tag is a target the sweep sweeps over.
func FuzzSplitHelpersMatchJoined(f *testing.F) {
	// The parity is the whole risk, so it is what the seeds are about: a
	// delimiter behind an even run of backslashes is live, behind an odd run is
	// escaped, and the run may span the join between the two pieces.
	for _, seed := range [][2]string{
		{"", ""},
		{"a", ""},
		{"", "a"},
		{"a", "*"},
		{"*", ""},
		{"*", "_"},
		{`\`, "*"},
		{`\\`, "*"},
		{`\\\`, "*"},
		{`\\\\`, "*"},
		{`a\`, "*"},
		{`a\\`, "*"},
		{`a\\\`, "*"},
		{`\*`, ""},
		{`\\*`, ""},
		{`\\\*`, ""},
		{"a_", ""},
		{"_", `\`},
		{"x", `\_`},
		{"", `\\_`},
		{`\\\\`, "_"},
		{"\n", ""},
		{"", "\n"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		joined := prefix + suffix
		if got, want := beforeOf(prefix, suffix), before(joined); got != want {
			t.Fatalf("beforeOf(%q, %q) = %q, but before(%q) = %q",
				prefix, suffix, got, joined, want)
		}
		if got, want := endsWithLiveOf(prefix, suffix), endsWithLive(joined); got != want {
			t.Fatalf("endsWithLiveOf(%q, %q) = %q, but endsWithLive(%q) = %q",
				prefix, suffix, got, joined, want)
		}
	})
}

// TestSplitHelpersMatchJoinedOnTheSeeds runs the differential comparison in the
// ordinary suite as well, so the agreement is checked by `make test` and not
// only by a fuzz sweep somebody has to remember to run.
func TestSplitHelpersMatchJoinedOnTheSeeds(t *testing.T) {
	for _, c := range [][2]string{
		{"", ""},
		{"a", "*"},
		{`\`, "*"},
		{`\\`, "*"},
		{`a\\\`, "*"},
		{`\*`, ""},
		{`\\*`, ""},
		{"_", `\`},
		{"x", `\_`},
		{"", `\\_`},
	} {
		joined := c[0] + c[1]
		if got, want := beforeOf(c[0], c[1]), before(joined); got != want {
			t.Errorf("beforeOf(%q, %q) = %q, want %q", c[0], c[1], got, want)
		}
		if got, want := endsWithLiveOf(c[0], c[1]), endsWithLive(joined); got != want {
			t.Errorf("endsWithLiveOf(%q, %q) = %q, want %q", c[0], c[1], got, want)
		}
	}
}

// TestInsideLiveReadsEscapesFromTheStart pins the third member of the
// "which delimiter is live" family, which is here because it is the same
// parity question the two above it answer and it got the answer wrong.
//
// insideLive reports a delimiter that would close a span early, and it excludes
// the two ends because a delimiter flush against either of them is the flush
// check's question instead. It used to start its scan at the first byte
// strictly inside as well, so a backslash at index 0 was never consumed and the
// character it escapes was counted as live. `\*0` is what an emphasised node
// holding `*0` renders to, and reading its asterisk as a delimiter cost that
// span both of its spellings. See the 2026-08-18 cases in
// TestADelimiterIsWrittenOnlyWhereItCanBeRead for what the writer did next.
func TestInsideLiveReadsEscapesFromTheStart(t *testing.T) {
	for _, c := range []struct {
		s    string
		char byte
		want bool
	}{
		// The find: an escape whose backslash is the first byte.
		{`\*0`, '*', false},
		{`\_0`, '_', false},
		{`\**`, '*', false},

		// The parity, which continues to hold from index 0. An escaped
		// backslash escapes nothing, so the delimiter behind it is live.
		{`\\*0`, '*', true},
		{`\\\*0`, '*', false},
		{`\\\\*0`, '*', true},

		// An escape anywhere else, which is what the scan already read.
		{`0\*0`, '*', false},
		{`0\\*0`, '*', true},

		// A live delimiter strictly inside is the whole point.
		{"0*0", '*', true},
		{"a_b", '_', true},

		// Neither end counts: both belong to the flush check.
		{"*0*", '*', false},
		{"*0", '*', false},
		{"0*", '*', false},
		{"**", '*', false},
		{"*", '*', false},
		{"", '*', false},
		{`\*`, '*', false},

		// The character asked about is the only one that answers.
		{"0_0", '*', false},
		{"0*0", '_', false},
	} {
		if got := insideLive(c.s, c.char); got != c.want {
			t.Errorf("insideLive(%q, %q) = %v, want %v", c.s, string(c.char), got, c.want)
		}
	}
}
