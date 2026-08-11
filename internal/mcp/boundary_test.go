//go:build mcp

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/mcp"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// TestAnOversizedFrameIsRefusedAndTheSessionSurvives is the whole point of the
// change: the frame is refused, not the session.
//
// A bufio.Scanner cannot resume — once a line exceeds its buffer, ErrTooLong is
// set and Scan returns false for good — so the loop exited and Serve returned.
// The peer got no error frame for the request it sent; it saw the stream close
// underneath it, which dispatch's own doc comment describes as
// "indistinguishable from a crash it caused and gives an agent nothing to
// report". The comment above the buffer said a frame was "refused rather than
// silently truncated", and the code hung up instead.
//
// The trailing ping is the assertion. An error reply alone would pass against a
// version that answers and then dies, which is the shape the defect actually
// had.
func TestAnOversizedFrameIsRefusedAndTheSessionSurvives(t *testing.T) {
	huge := strings.Repeat("x", mcp.MaxFrameBytes+1024)
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"thing_get","arguments":{"key":"`+huge+`"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2 — the session did not survive: %v",
			len(replies), replies)
	}

	rpcErr, ok := replies[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("the oversized frame was not refused: %v", replies[0])
	}
	// The limit is in the message because a peer told only "too large" has to
	// guess how much to cut.
	if msg, _ := rpcErr["message"].(string); !strings.Contains(msg, "8388608") {
		t.Errorf("the refusal does not name the limit: %q", msg)
	}
	if replies[1]["id"] != float64(2) {
		t.Errorf("the frame after the refusal was not answered: %v", replies[1])
	}
}

// TestTheFrameAfterAnOversizedOneIsNotReadAsGarbage covers the half a
// size check alone would miss.
//
// Refusing without draining leaves the reader part-way through the frame just
// rejected, so its tail is read as fresh input and one bad frame becomes a run
// of parse errors. Three good frames follow the oversized one here, and all
// three have to be answered by id — a count alone would pass if the tail
// produced exactly the right number of junk replies.
func TestTheFrameAfterAnOversizedOneIsNotReadAsGarbage(t *testing.T) {
	huge := strings.Repeat("y", mcp.MaxFrameBytes+1024)
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+
			`{"name":"thing_get","arguments":{"key":"`+huge+`"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`)

	var ids []any
	for _, r := range replies {
		ids = append(ids, r["id"])
	}
	want := []any{float64(1), nil, float64(3), float64(4), float64(5)}
	if len(ids) != len(want) {
		t.Fatalf("ids answered = %v, want %v", ids, want)
	}
	for i, id := range ids {
		// The refusal carries a null id: the frame was never parsed, so there
		// is nothing to answer it with.
		if want[i] == nil {
			continue
		}
		if id != want[i] {
			t.Errorf("reply %d has id %v, want %v (full: %v)", i, id, want[i], ids)
		}
	}
}

// TestAFrameManyTimesTheLimitIsDrainedInChunks exercises the path a
// just-over-the-cap frame does not.
//
// The reader's buffer is 64KiB, so a frame eight times the cap is hundreds of
// ErrBufferFull chunks and the drain loop runs for most of them. A frame barely
// over the limit trips the check on a chunk that already carried the newline,
// which is the other branch entirely — and the branch that was wrong first.
//
// What this does *not* prove is the memory bound, and that is worth saying
// plainly rather than implying. ReadSlice is used precisely so an enormous
// frame is never held whole — ReadString grows until it finds the delimiter, so
// a size check after it returns has already buffered what it is rejecting — but
// both spellings pass this test. The bound is argued in readFrame's comment and
// asserted by nothing; measuring peak allocation in a unit test is flaky enough
// to be worse than the claim it would make.
//
// The frame is generated rather than built as a string, so the test does not
// itself hold 64MiB to check that the server does not.
func TestAFrameManyTimesTheLimitIsDrainedInChunks(t *testing.T) {
	oversized := io.MultiReader(
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","pad":"`),
		&repeatReader{b: 'x', n: mcp.MaxFrameBytes * 8},
		strings.NewReader("\"}\n"),
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n"),
	)

	var out strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: testRegistry(t),
		In:       oversized,
		Out:      &out,
		Log:      io.Discard,
		Name:     "jr",
		Version:  "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(out.String(), `"id":2`) {
		t.Errorf("the frame after a much-oversized one was not answered:\n%s", out.String())
	}
}

// repeatReader yields n copies of one byte without materialising them.
type repeatReader struct {
	b byte
	n int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.n)
	for i := range n {
		p[i] = r.b
	}
	r.n -= n
	return n, nil
}

// TestAFrameAtTheLimitIsAccepted is the boundary on the other side, so the cap
// cannot be satisfied by refusing everything large.
func TestAFrameAtTheLimitIsAccepted(t *testing.T) {
	// A frame just under the cap, padded in the one place a long string is
	// legal without changing what the call means.
	pad := strings.Repeat("z", mcp.MaxFrameBytes-2048)
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"thing_get","arguments":{"key":"`+pad+`"}}}`)

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(replies), replies)
	}
	if _, isErr := replies[0]["error"]; isErr {
		t.Errorf("a frame under the limit was refused: %v", replies[0])
	}
}

// TestAFinalFrameWithoutANewlineIsAnswered covers what changed underneath.
//
// bufio.Scanner yields a final unterminated line; a Reader returns it with
// io.EOF, which is easy to treat as "nothing left" and drop. A peer that wrote
// a frame and closed the stream asked a complete question.
func TestAFinalFrameWithoutANewlineIsAnswered(t *testing.T) {
	var out strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: testRegistry(t),
		In:       strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
		Out:      &out,
		Log:      io.Discard,
		Name:     "jr",
		Version:  "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(out.String(), `"id":1`) {
		t.Errorf("a final frame with no newline was dropped: %q", out.String())
	}
}

// TestAnIntArgumentIsCheckedBeforeItIsNarrowed pins the conversion.
//
// JSON has one numeric type and Go decodes it as float64, so `int(f)` was doing
// three things silently: an out-of-range value is implementation-defined by the
// Go spec, NaN is too, and a fraction was truncated without a word.
//
// Nothing reachable turns that into a defect today — `--page-size` is the only
// registry int flag and resolvePageSize range-checks it — which is the reason
// to fix it rather than the reason not to. The check that currently saves it
// belongs to another flag's validation, and the next int flag inherits the
// narrowing without inheriting the rescue. So this drives a flag of its own.
func TestAnIntArgumentIsCheckedBeforeItIsNarrowed(t *testing.T) {
	for name, tc := range map[string]struct {
		json    string
		refused bool
	}{
		"in range":      {`7`, false},
		"zero":          {`0`, false},
		"negative":      {`-3`, false},
		"fractional":    {`2.7`, true},
		"far too big":   {`1e30`, true},
		"far too small": {`-1e30`, true},
		// 2^53 exactly. The last integer a float64 can hold without a
		// neighbour rounding onto it, so it is the largest value that can be
		// accepted honestly.
		//
		// 2^53+1 is deliberately absent. It is not refused and cannot be:
		// json.Unmarshal has already rounded it to 2^53 by the time any of this
		// runs, so the value wholeNumber sees is one the peer could legitimately
		// have sent. Catching it would mean decoding with json.Number
		// everywhere — see the note in wholeNumber.
		"at the exact limit": {`9007199254740992`, false},
	} {
		t.Run(name, func(t *testing.T) {
			var ran bool
			reg := intFlagRegistry(&ran)

			replies := session(t, reg,
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
					`{"name":"thing_count","arguments":{"depth":`+tc.json+`}}}`)
			result, _ := replies[0]["result"].(map[string]any)
			isError, _ := result["isError"].(bool)

			if isError != tc.refused {
				t.Errorf("isError = %v, want %v\n%s",
					isError, tc.refused, toolText(t, result))
			}
			if ran == tc.refused {
				t.Errorf("the command ran = %v, but refused = %v", ran, tc.refused)
			}
		})
	}
}

// intFlagRegistry registers a command with an int flag and no validation of its
// own, so the conversion is the only thing standing between the peer and the
// value.
func intFlagRegistry(ran *bool) *registry.Registry {
	r := registry.New()
	r.Register(&registry.Command{
		Path:    []string{"thing", "count"},
		Summary: "Count things",
		Example: "jr thing count",
		Flags: []registry.Flag{
			{Name: "depth", Type: registry.TypeInt, Usage: "how deep"},
		},
		Outputs: []registry.Output{{Kind: "thing.count", Version: 1}},
		Run: func(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
			*ran = true
			return render.Record("thing.count", 1,
				render.El("thing").Attr("depth", itoa(inv.Flags.Int("depth")))), nil
		},
	})
	return r
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
