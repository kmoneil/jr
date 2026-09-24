//go:build !windows

// The scripts under test are the development toolchain, which runs on macOS
// and Linux. Nothing under scripts/ ships, and under Git Bash on Windows MSYS
// rewrites their arguments before they run, so what these would measure there
// is the emulation.
package lint_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	rigToken       = "rig-secret-token"
	sandboxSecret  = "c2FuZGJveC1zZWNyZXQ="
	sandboxSuffix  = ".atlassian.test"
	sandboxSiteURL = "https://sandbox" + sandboxSuffix
)

// curlRecorder passes every call through to the real curl, after writing its
// arguments down, which is how a test sees what `ps` would have shown.
const curlRecorder = `#!/bin/sh
printf '%s\n' "$*" >> "$CURL_ARGV_LOG"
exec "$REAL_CURL" "$@"
`

// curlFake answers without a network, for the sandbox, whose host a test may
// not reach: it records the arguments and the headers curl was handed on
// stdin, writes a body where -o says, and prints the status -w asks for.
const curlFake = `#!/bin/sh
printf '%s\n' "$*" >> "$CURL_ARGV_LOG"
cat > "$CURL_STDIN_LOG"
out=
while [ $# -gt 0 ]; do
	case $1 in -o) out=$2; shift ;; esac
	shift
done
printf '{"accountId":"a-1"}' > "$out"
printf '200'
`

// jrStub answers the three things probe asks jr: whether it runs, which site
// a context names, and the header for it.
const jrStub = `#!/bin/sh
case "$*" in
*"context show"*) printf 'field\tvalue\n@name\tsandbox\n@site\t%s\n' "$JR_STUB_SITE" ;;
*"auth token --header"*) printf 'Authorization: Basic %s\n' "$JR_STUB_SECRET" ;;
*version*) printf 'field\tvalue\n' ;;
esac
`

// fakeRig is a Data Center rig on loopback that remembers what reached it.
type fakeRig struct {
	mu       sync.Mutex
	requests []rigRequest
	srv      *httptest.Server
}

type rigRequest struct {
	method, path, auth, contentType, body string
}

func newFakeRig(t *testing.T) *fakeRig {
	t.Helper()
	rig := &fakeRig{}
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = io.WriteString(w, `{"state":"RUNNING"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		rig.mu.Lock()
		rig.requests = append(rig.requests, rigRequest{
			method: r.Method, path: r.URL.RequestURI(), auth: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), body: string(body),
		})
		rig.mu.Unlock()
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/ENG-1"):
			_, _ = io.WriteString(w, `{"key":"ENG-1","fields":{"status":{"name":"Resolved"},`+
				`"resolution":{"name":"Won't Do"},"comment":{"total":1,"comments":[]},`+
				`"updated":"2026-09-23T10:00:00.000+0000"}}`)
		default:
			_, _ = io.WriteString(w, `{"name":"ada"}`)
		}
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func (r *fakeRig) seen() []rigRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rigRequest(nil), r.requests...)
}

// probeTree lays out scripts/probe and scripts/dc/common.sh the way the
// repository does, with a rig profile holding a token and a .env pointing the
// rig's loopback port at the fake.
func probeTree(t *testing.T, rig *fakeRig) string {
	t.Helper()
	dir := t.TempDir()
	port := "1"
	if rig != nil {
		port = rig.srv.URL[strings.LastIndex(rig.srv.URL, ":")+1:]
	}
	writeTree(t, dir, map[string]string{
		"scripts/probe":            readFile(t, filepath.Join(repoRoot, "scripts/probe")),
		"scripts/dc/common.sh":     readFile(t, rigCommon),
		"scripts/dc/.env":          "JIRA_PORT=" + port + "\nCONTEXT_PATH=\n",
		"scripts/dc/profile/token": rigToken,
	})
	return dir
}

// probeRun runs the probe with curl replaced by the recorder or the fake, and
// returns its exit, its two streams, and what curl was handed.
func probeRun(t *testing.T, dir string, fakeCurl bool, env []string, args ...string) (code int, stdout, stderr, argv, stdin string) {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("no %s on PATH, and scripts/probe needs it: %v", tool, err)
		}
	}
	realCurl, _ := exec.LookPath("curl")
	stubs := t.TempDir()
	stub := curlRecorder
	if fakeCurl {
		stub = curlFake
	}
	writeTree(t, stubs, map[string]string{"curl": stub, "jr": jrStub})
	for _, name := range []string{"curl", "jr"} {
		if err := os.Chmod(filepath.Join(stubs, name), 0o755); err != nil { //nolint:gosec // stubs that must be executable.
			t.Fatalf("chmod: %v", err)
		}
	}
	argvLog, stdinLog := filepath.Join(stubs, "argv"), filepath.Join(stubs, "stdin")

	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "scripts/probe")}, args...)...) //nolint:gosec // a copy of a script in this repository.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+stubs+":"+os.Getenv("PATH"),
		"REAL_CURL="+realCurl, "CURL_ARGV_LOG="+argvLog, "CURL_STDIN_LOG="+stdinLog,
		"JR="+filepath.Join(stubs, "jr"), "PROBE_SANDBOX=", "PROBE_LOG=",
	)
	cmd.Env = append(cmd.Env, env...)
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running probe: %v", err)
		}
		code = exit.ExitCode()
	}
	a, _ := os.ReadFile(argvLog)  //nolint:gosec // the stub's own log.
	s, _ := os.ReadFile(stdinLog) //nolint:gosec // the stub's own log.
	return code, out.String(), errs.String(), string(a), string(s)
}

// TestAProbeOfTheRigSendsItsTokenOnlyToTheRig is the reason the probe exists
// rather than a curl line: the credential reaches the server and nothing else.
// Not curl's arguments, where ps shows it to anyone on the machine, which is
// exactly where the hand-built wrapper this replaces put the sandbox's. Not
// the output, and not the log.
func TestAProbeOfTheRigSendsItsTokenOnlyToTheRig(t *testing.T) {
	rig := newFakeRig(t)
	dir := probeTree(t, rig)
	logPath := filepath.Join(t.TempDir(), "probe.jsonl")

	code, stdout, stderr, argv, _ := probeRun(t, dir, false, []string{"PROBE_LOG=" + logPath},
		"rig", "post", "/rest/api/2/issue/ENG-1/transitions", `{"transition":{"id":"5"}}`)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, stderr)
	}
	if stdout != "HTTP 204\n" {
		t.Errorf("stdout = %q, want the status line and no body", stdout)
	}
	got := rig.seen()
	if len(got) != 1 {
		t.Fatalf("the rig saw %d requests, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.method != http.MethodPost || r.path != "/rest/api/2/issue/ENG-1/transitions" {
		t.Errorf("the rig saw %s %s", r.method, r.path)
	}
	if r.auth != "Bearer "+rigToken {
		t.Errorf("Authorization = %q, want the profile's token as a bearer", r.auth)
	}
	if r.body != `{"transition":{"id":"5"}}` || r.contentType != "application/json" {
		t.Errorf("body %q as %q, want the JSON as given", r.body, r.contentType)
	}

	logged, err := os.ReadFile(logPath) //nolint:gosec // the test's own temporary file.
	if err != nil {
		t.Fatalf("PROBE_LOG was not written: %v", err)
	}
	var entry struct {
		Target, Method, Path, Request string
		Status                        int
	}
	if err := json.Unmarshal(logged, &entry); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, logged)
	}
	if entry.Target != "rig" || entry.Method != "POST" || entry.Status != 204 ||
		entry.Request != `{"transition":{"id":"5"}}` {
		t.Errorf("log entry = %+v", entry)
	}

	for where, text := range map[string]string{
		"curl's arguments": argv, "stdout": stdout, "stderr": stderr, "the log": string(logged),
	} {
		if strings.Contains(text, rigToken) {
			t.Errorf("the token is in %s:\n%s", where, text)
		}
	}
	if !strings.Contains(argv, "-H @-") {
		t.Errorf("curl was not handed its headers on stdin: %s", argv)
	}
}

// TestAProbeRefusesAPathThatLeavesTheSite: a scheme or a leading // names a
// host, and curl would carry the Authorization header there. Refused before
// curl is ever called.
func TestAProbeRefusesAPathThatLeavesTheSite(t *testing.T) {
	rig := newFakeRig(t)
	dir := probeTree(t, rig)
	for _, path := range []string{
		"https://elsewhere.example/rest/api/2/myself",
		"//elsewhere.example/rest/api/2/myself",
		"rest/api/2/myself",
		"/rest/api/2/search?jql=a b",
		"/rest/api/2/myself\r\nX-Injected: 1",
	} {
		code, _, stderr, argv, _ := probeRun(t, dir, false, nil, "rig", "GET", path)
		if code != 2 {
			t.Errorf("%q: exit %d, want 2:\n%s", path, code, stderr)
		}
		if argv != "" {
			t.Errorf("%q: curl was called: %s", path, argv)
		}
	}
	if n := len(rig.seen()); n != 0 {
		t.Errorf("the rig saw %d requests from refused paths", n)
	}
}

// TestAProbeReadsAnIssueBackInOneLine is the read-back every measurement
// ended with, written out by hand six times in one session.
func TestAProbeReadsAnIssueBackInOneLine(t *testing.T) {
	rig := newFakeRig(t)
	dir := probeTree(t, rig)
	code, stdout, stderr, _, _ := probeRun(t, dir, false, nil, "issue", "rig", "ENG-1")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, stderr)
	}
	want := `{"key":"ENG-1","status":"Resolved","resolution":"Won't Do","comments":1,` +
		`"updated":"2026-09-23T10:00:00.000+0000"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got := rig.seen(); len(got) != 1 || !strings.Contains(got[0].path, "fields=status,resolution,comment,updated") {
		t.Errorf("the rig saw %+v", got)
	}
	if code, _, _, _, _ := probeRun(t, dir, false, nil, "issue", "rig", "not a key"); code != 2 {
		t.Errorf("a malformed key exited %d, want 2", code)
	}
}

// TestTheSandboxIsOnlyEverACloudSite: the sandbox is a named jr context whose
// host has to be a Cloud one. A context naming anything else, production
// included, is refused before curl is called, and one that passes hands curl
// jr's header on stdin.
func TestTheSandboxIsOnlyEverACloudSite(t *testing.T) {
	dir := probeTree(t, nil)
	env := func(site string) []string {
		return []string{
			"PROBE_SANDBOX=sandbox", "PROBE_SANDBOX_SUFFIX=" + sandboxSuffix,
			"JR_STUB_SITE=" + site, "JR_STUB_SECRET=" + sandboxSecret,
		}
	}

	if code, _, stderr, argv, _ := probeRun(t, dir, true, []string{"PROBE_SANDBOX_SUFFIX=" + sandboxSuffix},
		"sandbox", "GET", "/rest/api/3/myself"); code != 2 || argv != "" ||
		!strings.Contains(stderr, "PROBE_SANDBOX") {
		t.Errorf("no context named: exit %d, curl %q:\n%s", code, argv, stderr)
	}

	for _, site := range []string{
		"https://jira.example.test",
		"https://elsewhere.example/" + sandboxSuffix + "/x",
		"https://user@sandbox" + sandboxSuffix,
		"https://sandbox" + sandboxSuffix + ":8443",
		"http://sandbox" + sandboxSuffix,
	} {
		code, _, stderr, argv, _ := probeRun(t, dir, true, env(site), "sandbox", "GET", "/rest/api/3/myself")
		if code != 2 || argv != "" {
			t.Errorf("site %q: exit %d, curl %q, want a refusal before curl:\n%s", site, code, argv, stderr)
		}
	}

	code, stdout, stderr, argv, stdin := probeRun(t, dir, true, env(sandboxSiteURL), "sandbox", "GET", "/rest/api/3/myself")
	if code != 0 {
		t.Fatalf("the sandbox itself was refused, exit %d:\n%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "HTTP 200\n") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(argv, sandboxSiteURL+"/rest/api/3/myself") {
		t.Errorf("curl was not pointed at the sandbox: %s", argv)
	}
	if strings.Contains(argv, sandboxSecret) {
		t.Errorf("the credential is in curl's arguments: %s", argv)
	}
	if !strings.Contains(stdin, "Authorization: Basic "+sandboxSecret) {
		t.Errorf("curl's stdin did not carry jr's header: %q", stdin)
	}
}
