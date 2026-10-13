package cli_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
)

// zoneSite is a Cloud site with one issue on it, whose account keeps the zone
// a test gives it. It counts what it was asked, because two of the claims
// under test are about requests that must and must not be made.
type zoneSite struct {
	url string
	// accountZone is the zone on the account's profile. Empty is an account
	// the site names no zone for.
	accountZone string
	myself      atomic.Int64
	comments    atomic.Int64
}

// The issue was created at 23:30 UTC on the 11th and updated at 09:10 UTC on
// the 12th. In Berlin, two hours ahead in May, the first of those is the next
// day, which is the conversion a person gets wrong by hand.
const zoneIssue = `{"id":"1","key":"ENG-1","fields":{` +
	`"summary":"an issue with two instants on it",` +
	`"status":{"name":"Open","statusCategory":{"key":"new","name":"To Do"}},` +
	`"created":"2026-05-11T23:30:00.000+0000",` +
	`"updated":"2026-05-12T09:10:00.000+0000"}}`

func newZoneSite(t *testing.T, accountZone string) *zoneSite {
	t.Helper()
	s := &zoneSite{accountZone: accountZone}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/myself"):
			s.myself.Add(1)
			_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Ada Lovelace",` +
				`"timeZone":"` + s.accountZone + `"}`))
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			_, _ = w.Write([]byte(`{"version":"1001.0.0","deploymentType":"Cloud",` +
				`"serverTime":"2026-05-12T12:00:00.000+0000"}`))
		case strings.HasSuffix(r.URL.Path, "/field"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/comment") && r.Method == http.MethodPost:
			s.comments.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"10001",` +
				`"author":{"accountId":"acc-1","displayName":"Ada Lovelace"},` +
				`"body":{"type":"doc","version":1,"content":[{"type":"paragraph",` +
				`"content":[{"type":"text","text":"noted"}]}]},` +
				`"created":"2026-05-12T09:10:00.000+0000",` +
				`"updated":"2026-05-12T09:10:00.000+0000"}`))
		case strings.Contains(r.URL.Path, "/search"):
			_, _ = w.Write([]byte(`{"issues":[` + zoneIssue + `],"isLast":true}`))
		default:
			_, _ = w.Write([]byte(zoneIssue))
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// in is a session with a credential and a context named work on this site.
func (s *zoneSite) in(t *testing.T, contextFlags ...string) map[string]string {
	t.Helper()
	env := credentialed(t)
	mustRun(t, env, append([]string{
		"context", "create", "work", "--site", s.url, "--project", "ENG",
	}, contextFlags...)...)
	return env
}

// The same two instants, as each zone spells them.
const (
	createdUTC    = "2026-05-11T23:30:00Z"
	updatedUTC    = "2026-05-12T09:10:00Z"
	createdBerlin = "2026-05-12T01:30:00+02:00"
	updatedBerlin = "2026-05-12T11:10:00+02:00"
	updatedTokyo  = "2026-05-12T18:10:00+09:00"
	// Chicago is five hours behind UTC in May.
	createdChicago = "2026-05-11T18:30:00-05:00"
	updatedChicago = "2026-05-12T04:10:00-05:00"
)

// TestTimestampsAreWrittenInTheZoneAskedFor is issue 218 from the command
// line.
//
// Every timestamp this tool printed was UTC, and somebody writing up an
// incident converted a dozen of them by hand, where a conversion wrong by an
// hour looks exactly like a right one. --tz writes them in a zone, with the
// offset in the value, so each still names one instant and still parses.
//
// It is asked for in three places, and the nearest one wins: the flag, then
// JIRA_TZ, then the context. Asking for nothing is UTC, byte for byte what it
// was.
func TestTimestampsAreWrittenInTheZoneAskedFor(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")

	get := func(t *testing.T, env map[string]string, args ...string) string {
		t.Helper()
		got := run(t, env, append([]string{"issue", "get", "ENG-1"}, args...)...)
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
		}
		return got.stdout
	}
	holds := func(t *testing.T, doc, created, updated string) {
		t.Helper()
		for _, want := range []string{
			"<created>" + created + "</created>", "<updated>" + updated + "</updated>",
		} {
			if !strings.Contains(doc, want) {
				t.Errorf("no %s in:\n%s", want, doc)
			}
		}
	}

	t.Run("nothing asked is UTC", func(t *testing.T) {
		holds(t, get(t, site.in(t)), createdUTC, updatedUTC)
	})

	t.Run("utc by name is the same bytes", func(t *testing.T) {
		env := site.in(t)
		if plain, named := get(t, env), get(t, env, "--tz", "utc"); plain != named {
			t.Errorf("--tz utc is not what no --tz writes:\n%s\nwant\n%s", named, plain)
		}
	})

	t.Run("the flag", func(t *testing.T) {
		holds(t, get(t, site.in(t), "--tz", "Europe/Berlin"), createdBerlin, updatedBerlin)
	})

	t.Run("the environment", func(t *testing.T) {
		env := site.in(t)
		env["JIRA_TZ"] = "Europe/Berlin"
		holds(t, get(t, env), createdBerlin, updatedBerlin)
	})

	t.Run("the context", func(t *testing.T) {
		holds(t, get(t, site.in(t, "--tz", "Europe/Berlin")), createdBerlin, updatedBerlin)
	})

	t.Run("the environment over the context", func(t *testing.T) {
		env := site.in(t, "--tz", "America/Chicago")
		env["JIRA_TZ"] = "Europe/Berlin"
		holds(t, get(t, env), createdBerlin, updatedBerlin)
	})

	t.Run("the flag over both", func(t *testing.T) {
		env := site.in(t, "--tz", "America/Chicago")
		env["JIRA_TZ"] = "Asia/Tokyo"
		holds(t, get(t, env, "--tz", "Europe/Berlin"), createdBerlin, updatedBerlin)
	})

	t.Run("utc typed over a context that names a zone", func(t *testing.T) {
		holds(t, get(t, site.in(t, "--tz", "Europe/Berlin"), "--tz", "utc"),
			createdUTC, updatedUTC)
	})

	t.Run("the account's zone, for one request", func(t *testing.T) {
		env := site.in(t)
		before := site.myself.Load()
		holds(t, get(t, env, "--tz", "account"), createdChicago, updatedChicago)
		if n := site.myself.Load() - before; n != 1 {
			t.Errorf("the account was asked for %d time(s), want once", n)
		}
		// And a zone that needs nobody asked costs nothing.
		before = site.myself.Load()
		get(t, env, "--tz", "Europe/Berlin")
		if n := site.myself.Load() - before; n != 0 {
			t.Errorf("a named zone asked the account %d time(s)", n)
		}
	})
}

// TestACollectionIsWrittenInItsZoneInEveryFormat covers the rows, which reach
// stdout by two routes. TSV writes each as it arrives and every other format
// writes the document built at the end, and a zone that reached one route and
// not the other would be the output contract splitting along --format.
func TestACollectionIsWrittenInItsZoneInEveryFormat(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")
	env := site.in(t)

	for _, format := range []string{"tsv", "xml", "json", "yaml"} {
		got := run(t, env, "issue", "list", "--format", format, "--tz", "Asia/Tokyo")
		if got.exit != exitcode.OK {
			t.Fatalf("%s: exit = %v\nstderr: %s", format, got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, updatedTokyo) {
			t.Errorf("%s: the row's updated is not in the zone:\n%s", format, got.stdout)
		}
		if strings.Contains(got.stdout, updatedUTC) {
			t.Errorf("%s: a timestamp was left in UTC:\n%s", format, got.stdout)
		}
	}
}

// TestAWriteIsAnsweredInTheZone drives a command that changes Jira and then
// reports what it made, which is the result somebody is likeliest to paste
// into a write-up.
//
// The second half is why the zone is settled before the command runs. Asking
// for the account's zone can fail, and here it does: the site names none. A
// zone resolved when the result is written would have posted the comment and
// then failed to say so, and the caller, told the command failed, would post
// it again.
func TestAWriteIsAnsweredInTheZone(t *testing.T) {
	if _, built := registry.Lookup("issue.comment.add"); !built {
		t.Skip("this build has no write commands")
	}

	t.Run("the result is in the zone", func(t *testing.T) {
		site := newZoneSite(t, "America/Chicago")
		got := run(t, site.in(t), "issue", "comment", "add", "ENG-1", "noted",
			"--tz", "Europe/Berlin")
		if got.exit != exitcode.OK {
			t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
		}
		if !strings.Contains(got.stdout, updatedBerlin) {
			t.Errorf("the comment's timestamps are not in the zone:\n%s", got.stdout)
		}
		if n := site.comments.Load(); n != 1 {
			t.Errorf("%d comments were posted, want 1", n)
		}
	})

	t.Run("a zone that cannot be settled refuses before the write", func(t *testing.T) {
		site := newZoneSite(t, "")
		got := run(t, site.in(t), "issue", "comment", "add", "ENG-1", "noted",
			"--tz", "account")
		if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "NO_ACCOUNT_TIMEZONE") {
			t.Fatalf("exit = %v, want NO_ACCOUNT_TIMEZONE at %v\nstderr: %s",
				got.exit, exitcode.Usage, got.stderr)
		}
		if n := site.comments.Load(); n != 0 {
			t.Errorf("the comment was posted %d time(s) by a command that was "+
				"refused, so a caller who retries posts it twice", n)
		}
		if got.stdout != "" {
			t.Errorf("a refused command wrote to stdout:\n%s", got.stdout)
		}
	})
}

// TestAZoneThatNamesNothingIsRefused covers each place a zone is asked for,
// and that the refusal says which one.
func TestAZoneThatNamesNothingIsRefused(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")

	refused := func(t *testing.T, got result, says string) {
		t.Helper()
		if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "INVALID_TZ") {
			t.Fatalf("exit = %v, want INVALID_TZ at %v\nstderr: %s",
				got.exit, exitcode.Usage, got.stderr)
		}
		if !strings.Contains(got.stderr, says) {
			t.Errorf("the refusal does not say %q:\n%s", says, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("a refused command wrote to stdout:\n%s", got.stdout)
		}
	}

	t.Run("the flag", func(t *testing.T) {
		refused(t, run(t, site.in(t), "issue", "get", "ENG-1", "--tz", "Mars/Olympus"),
			`--tz names "Mars/Olympus"`)
	})

	// Empty does not mean UTC. It would fall back to the context below, and a
	// caller who typed it to mean "no zone" would be answered in Berlin.
	t.Run("an empty flag", func(t *testing.T) {
		refused(t, run(t, site.in(t, "--tz", "Europe/Berlin"), "issue", "get", "ENG-1", "--tz", ""),
			"empty value")
	})

	// On every command, as JIRA_FORMAT is: the first command in the shell
	// says so, and not the first one that happens to print a timestamp.
	t.Run("the environment, on a command with no timestamp", func(t *testing.T) {
		env := site.in(t)
		env["JIRA_TZ"] = "Mars/Olympus"
		refused(t, run(t, env, "version"), "JIRA_TZ names")
	})

	t.Run("a context is not given one to keep", func(t *testing.T) {
		env := credentialed(t)
		got := run(t, env, "context", "create", "work", "--site", site.url, "--tz", "Mars/Olympus")
		refused(t, got, "--tz names")
		if list := mustRun(t, env, "context", "list"); strings.Contains(list.stdout, "work") {
			t.Errorf("a context with a zone that is no zone was stored:\n%s", list.stdout)
		}
	})
}

// TestTheZoneFlagExistsOnlyWhereThereIsATimestamp is the other half of a flag
// that either affects the output or does not exist.
//
// `version` writes no timestamp a schema declares and `doctor` keeps its own in
// UTC, so neither has the flag, and typing it is an unknown flag and not a
// silence. A zone set for the whole shell or the whole context is different:
// it was not typed at this command, and both run under it untouched. `doctor`
// has to, because it is what somebody runs when the context is the problem.
func TestTheZoneFlagExistsOnlyWhereThereIsATimestamp(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")

	for _, command := range []string{"version", "doctor"} {
		got := run(t, site.in(t), command, "--tz", "Europe/Berlin")
		if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "unknown flag: --tz") {
			t.Errorf("%s --tz: exit = %v, want an unknown flag at %v\n%s",
				command, got.exit, exitcode.Usage, got.stderr)
		}
	}

	env := site.in(t, "--tz", "account")
	env["JIRA_TZ"] = "Europe/Berlin"
	if got := run(t, env, "version"); got.exit != exitcode.OK {
		t.Errorf("version under a zone set for the shell: exit = %v\n%s", got.exit, got.stderr)
	}

	before := site.myself.Load()
	got := run(t, site.in(t, "--tz", "account"), "doctor", "--format", "xml")
	if got.exit != exitcode.OK {
		t.Fatalf("doctor under a context whose zone is the account's: exit = %v\n%s",
			got.exit, got.stderr)
	}
	stamps := regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[^"<]*`).
		FindAllString(got.stdout, -1)
	if len(stamps) == 0 {
		t.Fatalf("doctor printed no timestamp, so this is checking nothing:\n%s", got.stdout)
	}
	for _, stamp := range stamps {
		if !strings.HasSuffix(stamp, "Z") {
			t.Errorf("doctor wrote %s, which is not UTC", stamp)
		}
	}
	// Its own account check asks once. A zone it had settled would be a
	// second request, and a second way for it to fail.
	if n := site.myself.Load() - before; n > 1 {
		t.Errorf("doctor asked for the account %d times; one of them was for a zone", n)
	}
}

// TestAContextKeepsAZone walks the setting through the commands that store
// it and the one that reports it.
func TestAContextKeepsAZone(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")
	env := credentialed(t)

	created := mustRun(t, env, "context", "create", "work", "--site", site.url,
		"--tz", "account")
	if !strings.Contains(created.stdout, ` tz="account"`) {
		t.Errorf("the created context does not report its zone:\n%s", created.stdout)
	}

	// `account` is kept as the word. What it resolves to is the site's to say,
	// and this command reaches no site.
	shown := mustRun(t, env, "context", "show")
	if !strings.Contains(shown.stdout, ` tz="account"`) {
		t.Errorf("the effective settings do not report the zone:\n%s", shown.stdout)
	}

	edited := mustRun(t, env, "context", "edit", "work", "--tz", "Europe/Berlin")
	if !strings.Contains(edited.stdout, ` tz="Europe/Berlin"`) {
		t.Errorf("the edit did not change the zone:\n%s", edited.stdout)
	}

	env["JIRA_TZ"] = "Asia/Tokyo"
	if over := mustRun(t, env, "context", "show"); !strings.Contains(over.stdout, ` tz="Asia/Tokyo"`) {
		t.Errorf("the effective settings do not show the environment "+
			"winning:\n%s", over.stdout)
	}
	delete(env, "JIRA_TZ")

	if got := run(t, env, "context", "edit", "work", "--tz", "Mars/Olympus"); got.exit != exitcode.Usage ||
		!strings.Contains(got.stderr, "INVALID_TZ") {
		t.Errorf("an edit to a zone that is no zone: exit = %v\n%s", got.exit, got.stderr)
	}

	cleared := mustRun(t, env, "context", "edit", "work", "--unset", "tz")
	if strings.Contains(cleared.stdout, " tz=") {
		t.Errorf("--unset tz left the zone:\n%s", cleared.stdout)
	}
	// A context with no zone says nothing about one, which is every context
	// made before the setting existed.
	if list := mustRun(t, env, "context", "list", "--format", "xml"); strings.Contains(list.stdout, " tz=") {
		t.Errorf("a context with no zone reports one:\n%s", list.stdout)
	}
}
