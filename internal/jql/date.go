package jql

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// dateLayouts are the absolute forms Jira accepts, most specific first.
//
// Each is parsed with time.Parse, which validates ranges: 2020-13-45 is
// rejected here rather than being passed through as a literal that quietly
// matches nothing.
var dateLayouts = []string{
	"2006-01-02 15:04",
	"2006/01/02 15:04",
	"2006-01-02",
	"2006/01/02",
}

// relativePattern matches Jira's relative date syntax: an optional sign, a
// number, and a unit. `-7d`, `2w`, `+30m`.
var relativePattern = regexp.MustCompile(`^[-+]?\d+[mhdwM]$`)

// dateFunctions are the JQL functions valid in a date comparison.
var dateFunctions = map[string]bool{
	"now":                  true,
	"currentlogin":         true,
	"lastlogin":            true,
	"startofday":           true,
	"endofday":             true,
	"startofweek":          true,
	"endofweek":            true,
	"startofmonth":         true,
	"endofmonth":           true,
	"startofyear":          true,
	"endofyear":            true,
	"currentuser":          true,
	"membersof":            true,
	"componentsleadbyuser": true,
	"projectsleadbyuser":   true,
}

// ParseDate resolves a user-supplied date into a JQL value.
//
// It accepts an absolute date, a relative offset, or a date function. Anything
// else is a usage error naming what was wrong — never a literal passed through
// to Jira, which is how the incumbent turns a typo into an empty result set.
func ParseDate(input string) (Value, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, errs.Usage("INVALID_DATE", "date is empty").
			WithRemedy("use YYYY-MM-DD, a relative offset like -7d, or a function like startOfWeek()")
	}

	// A function call: startOfWeek(), endOfDay(-1).
	if strings.HasSuffix(s, ")") {
		return parseDateFunction(s)
	}

	if relativePattern.MatchString(s) {
		return Text(s), nil
	}

	for _, layout := range dateLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return Text(s), nil
		}
	}

	e := errs.Usage("INVALID_DATE", "%q is not a date", input).
		WithRemedy("use YYYY-MM-DD, YYYY-MM-DD HH:MM, a relative offset like -7d, " +
			"or a function like startOfWeek()")
	if hint := dateHint(s); hint != "" {
		return nil, e.WithDetail("%s", hint)
	}
	return nil, e
}

// dateHint explains a near-miss, so an off-by-one month is not reported the
// same way as a word.
func dateHint(s string) string {
	digitsAndSeps := regexp.MustCompile(`^\d{4}[-/]\d{1,2}[-/]\d{1,2}$`)
	if !digitsAndSeps.MatchString(s) {
		return ""
	}
	parts := regexp.MustCompile(`[-/]`).Split(s, 3)
	month, _ := strconv.Atoi(parts[1])
	day, _ := strconv.Atoi(parts[2])
	switch {
	case month < 1 || month > 12:
		return "month " + parts[1] + " is out of range"
	case day < 1 || day > 31:
		return "day " + parts[2] + " is out of range"
	default:
		return "that day does not exist in that month"
	}
}

// parseDateFunction validates a function call and rebuilds it as a Func node,
// so the rendered form comes from the renderer rather than from user text.
func parseDateFunction(s string) (Value, error) {
	open := strings.Index(s, "(")
	if open < 0 {
		return nil, errs.Usage("INVALID_DATE", "%q is not a date function", s).
			WithRemedy("a function call looks like startOfWeek() or endOfDay(-1)")
	}

	name := strings.TrimSpace(s[:open])
	if !isSimpleIdent(name) || !dateFunctions[strings.ToLower(name)] {
		return nil, errs.Usage("INVALID_DATE", "%q is not a known date function", name).
			WithDetail("input: %s", s).
			WithRemedy("valid functions include now(), startOfDay(), startOfWeek(), " +
				"endOfMonth(), and currentLogin()")
	}

	inner := strings.TrimSpace(s[open+1 : len(s)-1])
	f := &Func{Name: name}
	if inner == "" {
		return f, nil
	}

	for raw := range strings.SplitSeq(inner, ",") {
		arg := strings.TrimSpace(raw)
		arg = strings.Trim(arg, `"'`)
		switch {
		case arg == "":
			return nil, errs.Usage("INVALID_DATE", "%s has an empty argument", name).
				WithDetail("input: %s", s)
		case relativePattern.MatchString(arg), isNumeric(arg):
			f.Args = append(f.Args, Text(arg))
		default:
			return nil, errs.Usage("INVALID_DATE",
				"%q is not a valid argument to %s", arg, name).
				WithDetail("input: %s", s).
				WithRemedy("an offset argument looks like -1, -1d, or 2w")
		}
	}
	return f, nil
}

// Since builds `field >= value` from a user-supplied date.
func Since(field, date string) (Expr, error) {
	v, err := ParseDate(date)
	if err != nil {
		return nil, err
	}
	return &Clause{Field: field, Op: OpGte, Value: v}, nil
}

// Until builds `field <= value` from a user-supplied date.
func Until(field, date string) (Expr, error) {
	v, err := ParseDate(date)
	if err != nil {
		return nil, err
	}
	return &Clause{Field: field, Op: OpLte, Value: v}, nil
}
