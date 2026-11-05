//go:build !windows

package lint_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ghForCIWait stands in for gh as ci-wait reads one pull request's checks. It
// answers each read of the REST endpoint and of GraphQL with the next answer in
// GH_REST or GH_GRAPHQL, the last one repeating: "500", or a recorded body in
// testdata/ciwait, which it runs the read's --jq filter over with jq.
//
// It fails the way gh 2.103.3 does. --include writes the status line and the
// headers to stdout before the filter runs, and the filter runs over whatever
// body came back, so a 500 with an empty body fails in the filter, as
// "unexpected end of JSON input", and without --include nothing on either
// stream names the status. The empty body and that message were measured
// against GitHub on 2026-11-05; the headers' shape is a 200 read the same day.
//
// The bodies were recorded from kmoneil/jr that day, through both readers: the
// green one is PR 282's head, the running one a commit on main mid-run, and the
// red one a pull request whose vulnerability scan failed beside three skipped
// fuzz jobs. The REST ones are cut down to the three fields ci-wait reads.
const ghForCIWait = `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
include= filter= prev=
for a in "$@"; do
	[ "$a" = --include ] && include=1
	[ "$prev" = --jq ] && filter=$a
	prev=$a
done
case "$*" in
"repo view"*) echo example/jr; exit 0 ;;
"pr view"*headRefOid*) echo 0123456789abcdef0123456789abcdef01234567; exit 0 ;;
"api repos/"*check-runs*) kind=rest script=$GH_REST ;;
"api graphql"*) kind=graphql script=$GH_GRAPHQL ;;
*) echo "gh stub: nothing scripted for: $*" >&2; exit 2 ;;
esac
echo >> "$GH_LOG.$kind"
n=$(( $(wc -l < "$GH_LOG.$kind") ))
i=0
for answer in $script; do
	i=$((i + 1))
	[ "$i" -eq "$n" ] && break
done
if [ "$answer" = 500 ]; then
	[ -n "$include" ] && printf 'HTTP/2.0 500 Internal Server Error\nContent-Length: 0\r\n\r\n'
	echo "unexpected end of JSON input" >&2
	exit 1
fi
[ -n "$include" ] && printf 'HTTP/2.0 200 OK\nContent-Type: application/json; charset=utf-8\r\n\r\n'
jq -r "$filter" "$GH_BODIES/$kind-$answer.json" || exit 1
`

// ciWaitRun is one run of ci-wait against the stub, and what it was asked.
type ciWaitRun struct {
	code                    int
	stdout, stderr          string
	restReads, graphqlReads int
}

// runCIWait runs scripts/ci-wait on pull request 7 against the stub, answering
// the REST reads with rest and the GraphQL reads with graphql.
func runCIWait(t *testing.T, rest, graphql, timeout string) ciWaitRun {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("no jq on PATH, and the gh stub runs ci-wait's filters with it: %v", err)
	}
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "gh"), []byte(ghForCIWait), 0o755); err != nil { //nolint:gosec // a stub that must be executable.
		t.Fatalf("writing the gh stub: %v", err)
	}
	bodies, err := filepath.Abs(filepath.Join("testdata", "ciwait"))
	if err != nil {
		t.Fatalf("locating the recorded bodies: %v", err)
	}
	script, err := filepath.Abs(filepath.Join(repoRoot, "scripts/ci-wait"))
	if err != nil {
		t.Fatalf("locating ci-wait: %v", err)
	}
	log := filepath.Join(stubs, "gh.log")
	cmd := exec.Command("bash", script, "--pr", "7") //nolint:gosec // a script in this repository.
	cmd.Dir = stubs
	cmd.Env = append(os.Environ(),
		"PATH="+stubs+":"+os.Getenv("PATH"),
		"GH_LOG="+log, "GH_BODIES="+bodies, "GH_REST="+rest, "GH_GRAPHQL="+graphql,
		"POLL=0", "TIMEOUT="+timeout,
	)
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	var run ciWaitRun
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running ci-wait: %v", err)
		}
		run.code = exit.ExitCode()
	}
	run.stdout, run.stderr = out.String(), errs.String()
	run.restReads, run.graphqlReads = reads(t, log+".rest"), reads(t, log+".graphql")
	return run
}

// reads is how many times the stub answered one kind of read.
func reads(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // the stub's own log, in a temporary directory.
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading the stub's log: %v", err)
	}
	return strings.Count(string(data), "\n")
}

// TestCIWaitReadsAFailedRequestAsNoAnswer is 2026-11-05, landing PR 281.
// GitHub answered the check-runs read with a 500 and an empty body for over
// ten minutes, gh ran the filter over the empty body, and ci-wait exited 1,
// its code for a failed check, saying "unexpected end of JSON input" and
// nothing else. land gave up on a green pull request twice. A read GitHub
// fails now says nothing about the checks: ci-wait names it, asks GraphQL for
// the same runs, and waits for the next poll when that fails too.
func TestCIWaitReadsAFailedRequestAsNoAnswer(t *testing.T) {
	cases := []struct {
		name          string
		rest, graphql string
		code          int
		stdout        []string
		// Reads of each kind the answer took.
		restReads, graphqlReads int
	}{
		{
			name: "a 500 is read again through GraphQL", rest: "500", graphql: "green",
			code: 0, stdout: []string{"GREEN 0123456789abcdef"}, restReads: 1, graphqlReads: 1,
		},

		{
			name: "a check GraphQL reports running is waited for", rest: "500", graphql: "running green",
			code: 0, stdout: []string{"GREEN 0123456789abcdef"}, restReads: 2, graphqlReads: 2,
		},
		{
			name: "a poll neither read answers is waited past", rest: "500 running green", graphql: "500",
			code: 0, stdout: []string{"GREEN 0123456789abcdef"}, restReads: 3, graphqlReads: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runCIWait(t, tc.rest, tc.graphql, "30")
			if run.code != tc.code {
				t.Fatalf("ci-wait exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
					run.code, tc.code, run.stdout, run.stderr)
			}
			for _, want := range tc.stdout {
				if !strings.Contains(run.stdout, want) {
					t.Errorf("stdout does not say %q:\n%s", want, run.stdout)
				}
			}
			if run.restReads != tc.restReads || run.graphqlReads != tc.graphqlReads {
				t.Errorf("ci-wait read REST %d and GraphQL %d times, want %d and %d",
					run.restReads, run.graphqlReads, tc.restReads, tc.graphqlReads)
			}
			// The request is named, with the status GitHub answered, which is
			// what gh's own message leaves out.
			for _, want := range []string{
				"GET repos/example/jr/commits/0123456789abcdef0123456789abcdef01234567/check-runs?per_page=100",
				"HTTP/2.0 500 Internal Server Error",
				"unexpected end of JSON input",
			} {
				if !strings.Contains(run.stderr, want) {
					t.Errorf("stderr does not say %q:\n%s", want, run.stderr)
				}
			}
		})
	}
}

// TestCIWaitTimesOutWhenGitHubNeverAnswers keeps an outage apart from a red
// build. Neither read ever answers, so nothing is known about the checks, and
// the exit is the timeout's 2 rather than the 1 that says a check failed.
func TestCIWaitTimesOutWhenGitHubNeverAnswers(t *testing.T) {
	run := runCIWait(t, "500", "500", "2")
	if run.code != 2 {
		t.Fatalf("ci-wait exited %d over checks it never read, want 2\nstdout:\n%s\nstderr:\n%s",
			run.code, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "TIMEOUT") {
		t.Errorf("stdout does not say it timed out:\n%s", run.stdout)
	}
	if !strings.Contains(run.stderr, "POST graphql") {
		t.Errorf("the GraphQL read that failed is not named:\n%s", run.stderr)
	}
	if run.restReads < 2 {
		t.Errorf("ci-wait read the checks %d times in seconds of no answers; it stopped polling",
			run.restReads)
	}
}

// TestCIWaitReadsTheRecordedChecks is the reading itself, over a pull request
// with a failed check and one with every check passed or skipped, through the
// REST endpoint and through GraphQL, whose enums are upper case.
func TestCIWaitReadsTheRecordedChecks(t *testing.T) {
	cases := []struct {
		name, rest, graphql string
		code                int
		want                string
	}{
		{"red through REST", "red", "", 1, "vulnerability scan: failure\nRED "},
		{"green through REST", "green", "", 0, "GREEN "},
		{"red through GraphQL", "500", "red", 1, "vulnerability scan: failure\nRED "},
		{"green through GraphQL", "500", "green", 0, "GREEN "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runCIWait(t, tc.rest, tc.graphql, "30")
			if run.code != tc.code || !strings.Contains(run.stdout, tc.want) {
				t.Errorf("ci-wait exited %d saying\n%s\nwant %d and %q\nstderr:\n%s",
					run.code, run.stdout, tc.code, tc.want, run.stderr)
			}
			// Skipped is passed, so the three skipped fuzz jobs are not named.
			if strings.Contains(run.stdout, "skipped") {
				t.Errorf("a skipped check is reported as failing:\n%s", run.stdout)
			}
		})
	}
}
