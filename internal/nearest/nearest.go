// Package nearest ranks candidates against what somebody typed.
//
// It exists because this tool refuses names in several places and had a
// different idea of "close" in each of them. A field name was ranked by edit
// distance against the site's catalogue; a command name was matched by
// substring, so `jr schema list` offered every command containing the word;
// a subcommand went through cobra's own suggester with cobra's own distance;
// and an unknown flag was offered nothing at all. Four refusals of the same
// mistake, four answers.
//
// The rule is one rule now, and it lives here rather than in any of them
// because none of those packages owns the idea. It is a leaf: it imports
// nothing outside the standard library and nothing may be added to it that
// does.
package nearest

import (
	"sort"
	"strings"
)

// Threshold is the edit distance within which a candidate is worth naming,
// scaled to the length of what was typed.
//
// Two edits on a short word is generous and on a long one is strict, which is
// the right way round: `stt` for `status` is a plausible typo and `pro` for
// `project` is not a typo at all, it is a different word somebody may have
// meant literally.
func Threshold(s string) int {
	if d := len([]rune(s)) / 4; d > 2 {
		return d
	}
	return 2
}

// Distance is the Levenshtein edit distance between two strings.
//
// Two rows rather than a full matrix, because a field catalogue can run to
// hundreds of entries and only the number matters.
func Distance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// Strings returns the candidates closest to input, nearest first and at most n
// of them, or nothing when nothing is close.
//
// Nothing is the important half. A refusal listing three unrelated candidates
// is worse than a refusal listing none: it reads as an answer, and the caller
// spends a turn on it before working out that the tool was guessing.
//
// Comparison is case-insensitive, because a caller typing `Status` has made no
// mistake worth ranking. Ties break lexically so two equally close candidates
// cannot swap places between invocations.
func Strings(input string, candidates []string, n int) []string {
	want := strings.ToLower(strings.TrimSpace(input))
	if want == "" || n <= 0 {
		return nil
	}

	type scored struct {
		value string
		dist  int
	}
	var out []scored
	limit := Threshold(want)
	for _, c := range candidates {
		if d := Distance(want, strings.ToLower(c)); d <= limit {
			out = append(out, scored{c, d})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].dist != out[j].dist {
			return out[i].dist < out[j].dist
		}
		return out[i].value < out[j].value
	})
	if len(out) > n {
		out = out[:n]
	}

	values := make([]string, 0, len(out))
	for _, s := range out {
		values = append(values, s.value)
	}
	return values
}
