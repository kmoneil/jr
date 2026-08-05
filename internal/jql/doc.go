// Package jql builds JQL. It is never concatenated.
//
// A typed builder produces an AST and a single renderer serializes it. The
// renderer is the only place a string is quoted, and it escapes backslash and
// double-quote per Jira's grammar. Raw user JQL is always parenthesized, so a
// user's OR cannot escape the project scope.
//
// JQL is never inspected with a regular expression. Deciding whether a query
// already constrains a field means tokenizing it, because a regex
// false-positives on a summary that merely contains the text.
package jql
