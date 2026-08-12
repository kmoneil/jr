package render_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/render"
)

// TestExactlyTheContractFormatsAreNotPresentational pins the split the whole
// version promise rests on: four formats are versioned and will not change
// shape without a major bump, and anything else is presentation that may change
// in any release.
//
// Asserted from the registration rather than from a list here, so a fifth
// contract format cannot be added and quietly described to a human as
// presentation, or the reverse — which is the drift that would matter, because
// the flag's own help text is built from this answer.
func TestExactlyTheContractFormatsAreNotPresentational(t *testing.T) {
	contract := map[render.Format]bool{
		render.TSV: true, render.XML: true, render.JSON: true, render.YAML: true,
	}

	var presentational int
	for _, f := range render.Formats() {
		switch {
		case contract[f] && render.Presentational(f):
			t.Errorf("%s is a versioned format and reports itself as presentation", f)
		case !contract[f] && !render.Presentational(f):
			t.Errorf("%s is outside the contract and does not say so", f)
		case render.Presentational(f):
			presentational++
		}
	}
	// At most one, in any build. The help text says "X is for reading" in the
	// singular and would be wrong the moment there were two.
	if presentational > 1 {
		t.Errorf("%d presentational formats; the flag's help says one", presentational)
	}
}
