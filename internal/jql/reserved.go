package jql

import "strings"

// reserved is Atlassian's JQL reserved word list.
//
// A reserved word used as a field name must be quoted. Values do not need this
// list: the renderer quotes every string value unconditionally, which is why a
// value can never be mistaken for syntax no matter what a user types.
var reserved = map[string]struct{}{}

func init() {
	for w := range strings.FieldsSeq(`
		abort access add after alias all alter and any as asc audit avg
		before begin between boolean break by byte
		catch cf char character check checkpoint collate collation column commit
		connect continue count create current
		date decimal declare decrement default defaults define delete delimiter
		desc difference distinct divide do double drop
		else empty encoding end equals escape exclusive exec execute exists explain
		false fetch field file first float for from function
		go goto grant greater group
		having
		identified if immediate in increment index initial inner inout input insert
		int integer intersect intersection into is isempty isnull
		join
		last left less like limit lock long
		max min minus mode modify modulo more multiply
		next noaudit not notin nowait null number
		object of on option or order outer output
		power previous prior privileges public
		raise raw remainder rename resource return returns revoke right row rowid
		rownum rows
		select session set share size sqrt start strict string subtract sum synonym
		table then to trans transaction trigger true
		uid union unique update user
		validate values view
		when whenever where while with
	`) {
		reserved[w] = struct{}{}
	}
}

// IsReserved reports whether s is a JQL reserved word, case-insensitively.
func IsReserved(s string) bool {
	_, ok := reserved[strings.ToLower(s)]
	return ok
}
