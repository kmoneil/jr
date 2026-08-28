package registry

// ScopeWatcher wraps a Session and remembers the context scope a command asked
// it for, so the envelope can report the scope the answer was actually
// computed over.
//
// The value has to come from the read rather than from a second look at the
// context, and the difference is not pedantry. `issue list --all-projects`
// never asks for a project, and a boundary that re-read the context would stamp
// one onto a document whose rows came from every project on the site — which is
// the same defect the attribute exists to fix, told backwards. Asking what the
// command read answers both cases with one rule and needs no knowledge of which
// flag lifts which scope.
//
// It is deliberately the narrowest possible wrapper. Everything but the four
// scope readers is passed straight through, and nothing here can fail.
type ScopeWatcher struct {
	Session
	project string
	board   string
}

// WatchScope wraps a session, or returns nil for a nil one.
func WatchScope(s Session) *ScopeWatcher {
	if s == nil {
		return nil
	}
	return &ScopeWatcher{Session: s}
}

// Project records the read and passes it through.
func (w *ScopeWatcher) Project() string {
	w.project = w.Session.Project()
	return w.project
}

// RequireProject records the read and passes it through.
//
// It records what the session answered even when the answer is an error, which
// is nothing, because a command that failed for want of a project has no scope
// to report and the empty string is the honest value.
func (w *ScopeWatcher) RequireProject() (string, error) {
	p, err := w.Session.RequireProject()
	w.project = p
	return p, err
}

// Board records the read and passes it through.
func (w *ScopeWatcher) Board() string {
	w.board = w.Session.Board()
	return w.board
}

// RequireBoard records the read and passes it through.
func (w *ScopeWatcher) RequireBoard() (string, error) {
	b, err := w.Session.RequireBoard()
	w.board = b
	return b, err
}

// Scope reports the context scope this command asked for, and whether it asked
// for any. A command that never consulted the context reports none, which is
// what an unscoped answer should say.
func (w *ScopeWatcher) Scope() (project, board string) {
	if w == nil {
		return "", ""
	}
	return w.project, w.board
}

// Compile-time proof that the wrapper still satisfies the interface it wraps.
//
// This one line is the whole check. Embedding supplies every method the
// overrides do not, so a signature change to any of the four below stops
// ScopeWatcher satisfying Session and fails here rather than at a call site.
//
// It replaced three further assertions that bound method values on a nil
// pointer — `(*ScopeWatcher)(nil).Connect` and friends. A method promoted from
// an embedded interface reads that interface to build the value, so all three
// panicked in this package's init and took every test in the tree with them.
// They also proved nothing this line does not.
var _ Session = (*ScopeWatcher)(nil)
