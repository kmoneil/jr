package transport

import "io"

// This file exists so a test can reach one unexported constructor, and it is
// the first of its kind in this tree — which is worth a word, because the
// alternative was worse.
//
// boundedBody's whole contract is what it does at its limit, and that limit is
// 2 GiB in production. Driving it through a real response would mean moving two
// gigabytes through the suite. The other way to make it reachable is to turn
// maxUndeclaredStreamBytes into a var a test can lower, which mutates package
// state and makes the suite order-dependent — the objection this project
// already raises against tests that mutate the process environment.
//
// An export_test.go compiles only under `go test`, adds nothing to the
// package's API, and is what the standard library does for exactly this. It
// exposes the constructor and not the constant, so no test can change what
// production uses.
//
// Being in `package transport` rather than `transport_test` is what makes that
// possible; every other test file here is external, and should stay that way.

// NewBoundedBodyForTest wraps r with a limit, for a test that cannot afford the
// real one.
func NewBoundedBodyForTest(r io.ReadCloser, limit int64) io.ReadCloser {
	return &boundedBody{ReadCloser: r, left: limit}
}

// StreamBoundForTest reports the ceiling on a handed-over body, and whether it
// has one at all.
//
// It exists because removing the wrapper from handOver broke no test: the
// ceiling is 2 GiB, so nothing that drives a real response reaches it, and
// "the bound fails correctly" and "the bound is attached" are two claims. The
// first was covered and the second was not, which is a fix that could be
// deleted whole while the suite stayed green.
//
// Structural rather than behavioural, deliberately. The alternative is to make
// the ceiling a var a test can lower, and a package-level var swapped by tests
// is the order-dependence this project already refuses elsewhere. Paired with
// TestTheBoundedBodyFailsInsteadOfEnding, which drives what the bound does,
// this covers the whole property: attached where it belongs, absent where it
// does not, and correct when it fires.
func StreamBoundForTest(r io.ReadCloser) (limit int64, bounded bool) {
	cancelling, ok := r.(*cancelOnClose)
	if !ok {
		return 0, false
	}
	body, ok := cancelling.ReadCloser.(*boundedBody)
	if !ok {
		return 0, false
	}
	return body.left, true
}

// MaxUndeclaredStreamBytesForTest is the production ceiling, so a test asserts
// the real number rather than a copy of it that can drift.
const MaxUndeclaredStreamBytesForTest = maxUndeclaredStreamBytes
