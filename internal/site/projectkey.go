package site

// ValidProjectKey reports whether s can be a Jira project key.
//
// Jira's default pattern is two or more uppercase letters. A site can widen it,
// and what widened patterns actually use is digits and underscores after a
// leading letter, so that is the grammar here. **Being stricter than the server
// would be worse than the round trip it saves**: refusing a key some site
// genuinely uses makes the tool unusable there, and the round trip only costs
// somebody who typed it wrong.
//
// Nothing outside that set is accepted, and that half is not about Jira at all.
// A project key becomes a URL path segment, and a request that can be steered
// by its own argument is a different request. `../etc` reaching a path is why
// this exists rather than being left to `url.PathEscape`, which every caller
// happened to remember — the one that concatenated instead is how
// `epic add "../../../rest/api/2/issue/ENG-1"` became a POST to another
// endpoint. Escaping stays the second layer; this is the first.
//
// It lives here rather than in either resource because two of them ask the same
// question — `issue.ParseKey` about the project half of an issue key, and the
// four `project` commands about their first argument — and a resource may not
// import another resource. Two spellings of "is this a project key" in one
// binary is the thing to avoid; one of them being "no spelling at all" is how
// that happens.
func ValidProjectKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}
