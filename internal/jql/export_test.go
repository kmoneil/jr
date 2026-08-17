package jql

// ToLowerASCII exposes the unit-letter fold to the external test package.
//
// It is exported for a test rather than tested through a query because the
// public path can only reach it with a unit letter, and `relativePattern`
// generates its character class from `dateValueUnits`, so `w`, `d`, `h`, and
// `m` are the only letters that ever arrive. The function's contract is wider
// than its one caller: it says it folds ASCII uppercase, and the two bounds
// that make that true are what a mutation sweep found nothing asserting.
var ToLowerASCII = toLowerASCII
