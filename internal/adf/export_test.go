package adf

// ContentKey is contentKey, for the tests in package adf_test.
//
// survival_test.go worked this definition out and held it as its own copy for
// two days. It is one function now, so the golden that records what the writer
// loses and the writer's own decision about whether it has lost anything can
// never drift apart.
var ContentKey = contentKey
