package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/kmoneil/jr/internal/registry"
)

// progressInterval bounds how often the line is rewritten. A counter updated
// per row would spend more time formatting than fetching.
const progressInterval = 100 * time.Millisecond

// ttyProgress writes a single rewritten line to stderr.
//
// It exists only when stderr is a terminal. On a pipe it is never constructed,
// so a redirected run emits nothing — which is what lets a progress indicator
// coexist with the rule that stderr carries only structured diagnostics. There
// is no structured form of "42% done" worth defining, and a human watching a
// hundred-request run needs to know it is moving.
type ttyProgress struct {
	mu   sync.Mutex
	w    io.Writer
	last time.Time
	// noun is what is being counted, from the command's CollectionName.
	//
	// It was the literal "issues", on every streaming command in the tool, so
	// `project list` reported "12 issues" and `field list` reported "148
	// issues". Cosmetic, human-only, and never present on a pipe, which is how
	// it survived. It is fixed because the tool's whole argument is that it
	// does not say things that are not so, and the right value was already
	// declared: streamSpec reads the same field two functions from here.
	noun    string
	width   int
	started time.Time
	done    bool
}

func newTTYProgress(w io.Writer, noun string) *ttyProgress {
	if noun == "" {
		// A command that streams declares its collection —
		// TestStreamingCommandsDeclareTheirCollection holds them to it — so
		// this is for a caller that built one by hand rather than for a gap in
		// the registry.
		noun = "rows"
	}
	return &ttyProgress{w: w, noun: noun, started: time.Now()}
}

// Update implements registry.Progress.
func (p *ttyProgress) Update(done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.done {
		return
	}
	// Throttle, but never swallow the first report: seeing the count appear at
	// all is what answers "is this going to take all day".
	if !p.last.IsZero() && time.Since(p.last) < progressInterval {
		return
	}
	p.last = time.Now()

	var line string
	if total > 0 {
		line = fmt.Sprintf("%s / %s", humanCount(done), humanCount(total))
	} else {
		line = humanCount(done)
	}
	line += " " + p.noun + ", " + time.Since(p.started).Round(time.Second).String()

	p.write(line)
}

// Done implements registry.Progress. It clears the line so the progress report
// leaves no trace in a terminal's scrollback beside the actual output.
func (p *ttyProgress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.done {
		return
	}
	p.done = true
	if p.width > 0 {
		_, _ = fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.width))
	}
}

func (p *ttyProgress) write(line string) {
	// Pad to the previous width so a shorter line does not leave the tail of a
	// longer one behind it.
	padded := line
	if n := p.width - len(line); n > 0 {
		padded += strings.Repeat(" ", n)
	}
	p.width = len(line)
	_, _ = fmt.Fprintf(p.w, "\r%s", padded)
}

// humanCount groups thousands, because 5270 and 52700 are hard to tell apart at
// a glance and the difference is a minute of waiting.
func humanCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// progress returns a reporter for this invocation, counting whatever the
// command said it emits.
//
// Nothing is reported unless stderr is a terminal: a piped run must produce
// byte-identical stderr whether or not a human happens to be watching.
func (a *app) progress(noun string) registry.Progress {
	if !isTerminal(a.stderr) {
		return registry.NoProgress
	}
	return newTTYProgress(a.stderr, noun)
}
