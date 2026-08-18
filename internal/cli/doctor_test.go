package cli_test

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/exitcode"
)

// doctorChecks is every check one run reports, in the order the layers stack.
//
// The list is here rather than derived from the output, because the property
// under test is that all of them are always present: a diagnostic that prints
// only problems cannot tell a healthy configuration from a check that never
// ran, and a test that read the names out of the document could not tell the
// difference either.
var doctorChecks = []string{
	"config", "credential", "site", "transport",
	"deployment", "clock", "account", "limits",
}

// TestDoctorReportsEveryCheckWhenEverythingWorks is the healthy case, and it is
// half the point: every check is reported whether or not it found anything.
func TestDoctorReportsEveryCheckWhenEverythingWorks(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{limits: true})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	if doc.Status != "ok" {
		t.Errorf("status = %q, want ok:\n%s", doc.Status, doc.raw)
	}
	if doc.Failed != 0 || doc.Skipped != 0 {
		t.Errorf("failed = %d, skipped = %d, want 0 and 0:\n%s",
			doc.Failed, doc.Skipped, doc.raw)
	}
	for _, name := range doctorChecks {
		if got := doc.check(t, name).Status; got != "ok" {
			t.Errorf("the %s check is %q, want ok:\n%s", name, got, doc.raw)
		}
	}

	deployment := doc.check(t, "deployment")
	if got := deployment.attr("kind"); got != "cloud" {
		t.Errorf("deployment kind = %q, want cloud", got)
	}
	if got := deployment.attr("source"); got != "probe" {
		t.Errorf("deployment source = %q; a fresh cache directory has nothing "+
			"to read, so this run must have asked the site", got)
	}
	if got := doc.check(t, "account").attr("display"); got != "Ada Lovelace" {
		t.Errorf("account display = %q, want Ada Lovelace", got)
	}
	if got := doc.check(t, "site").attr("endpoint"); got != jira.URL+"/rest/api/2/serverInfo" {
		t.Errorf("site endpoint = %q, want the probe URL under %s", got, jira.URL)
	}
}

// TestDoctorNeverRevealsTheCredential is the rule `auth status` carries, on the
// one other command that reports a credential.
func TestDoctorNeverRevealsTheCredential(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	if strings.Contains(doc.raw, credentialToken) {
		t.Errorf("the token reached the document:\n%s", doc.raw)
	}
	if got := doc.check(t, "credential").attr("scheme"); got != "basic" {
		t.Errorf("credential scheme = %q, want basic", got)
	}
	// The account's email is deliberately absent: this document is what
	// somebody pastes into a bug report.
	if strings.Contains(doc.raw, "@example.com") &&
		!strings.Contains(doc.check(t, "credential").attr("user"), "@example.com") {
		t.Errorf("an email address reached the document outside the credential "+
			"user it was configured as:\n%s", doc.raw)
	}
}

// The failure paths. Each one makes a check fail, because a diagnostic whose
// failure path has never run is a diagnostic that reports healthy.

func TestDoctorReportsAConfigItCannotRead(t *testing.T) {
	env := isolate(t, nil)
	writeConfig(t, env, "this is not toml [[[")

	doc := runDoctor(t, env)

	failedCheck(t, doc, "config", "")
	// Everything downstream is waiting on it, and says so rather than
	// reporting the same cause eight times.
	for _, name := range doctorChecks[1:] {
		if got := doc.check(t, name).Status; got != "skipped" {
			t.Errorf("the %s check is %q, want skipped when the config did not "+
				"resolve:\n%s", name, got, doc.raw)
		}
	}
}

func TestDoctorReportsAnUnknownContext(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctorArgs(t, env, "--context", "nope")

	failedCheck(t, doc, "config", "UNKNOWN_CONTEXT")
	if !strings.Contains(doc.check(t, "config").Detail, "default") {
		t.Errorf("the refusal does not name the contexts that are defined:\n%s", doc.raw)
	}
}

func TestDoctorReportsAMissingCredentialAndProbesAnyway(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{})
	env := doctorEnv(t, jira.URL, withoutCredential)

	doc := runDoctor(t, env)

	failedCheck(t, doc, "credential", "NO_CREDENTIALS")
	if got := doc.check(t, "credential").attr("searched"); !strings.Contains(got, "environment") {
		t.Errorf("searched = %q, and it should name every provider consulted", got)
	}
	// The whole reason the client is built anonymously: "this site is reachable
	// and is Cloud" is what somebody who never logged in needs to be told.
	if got := doc.check(t, "deployment").Status; got != "ok" {
		t.Errorf("the deployment check is %q; a site that answers the probe "+
			"anonymously should still be probed:\n%s", got, doc.raw)
	}
	if got := doc.check(t, "account").Status; got != "skipped" {
		t.Errorf("the account check is %q, want skipped: there is no credential "+
			"to ask about:\n%s", got, doc.raw)
	}
}

// TestDoctorReportsACredentialStoreOtherUsersCanRead is the claim the exit
// sweep's exemption rests on: a store this tool refuses to read is a verdict
// here rather than an exit 4, and the refusal is the store's own.
func TestDoctorReportsACredentialStoreOtherUsersCanRead(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{})
	env := doctorEnv(t, jira.URL, withCredential)
	store := filepath.Join(env["XDG_STATE_HOME"], "jr", "credentials.toml")
	if err := os.Chmod(store, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	doc := runDoctor(t, env)

	failedCheck(t, doc, "credential", "STORE_PERMISSIONS")
	if !strings.Contains(doc.check(t, "credential").Remedy, "chmod") {
		t.Errorf("the remedy does not say how to fix it: %q",
			doc.check(t, "credential").Remedy)
	}
	if got := doc.check(t, "account").Status; got != "skipped" {
		t.Errorf("the account check is %q, want skipped:\n%s", got, doc.raw)
	}
}

func TestDoctorReportsNoSiteConfigured(t *testing.T) {
	env := isolate(t, nil)

	doc := runDoctor(t, env)

	failedCheck(t, doc, "site", "NO_SITE")
	if got := doc.check(t, "config").Status; got != "ok" {
		t.Errorf("the config check is %q; an empty configuration resolves, and "+
			"the missing site is the site check's finding:\n%s", got, doc.raw)
	}
	if got := doc.check(t, "transport").Status; got != "skipped" {
		t.Errorf("the transport check is %q, want skipped:\n%s", got, doc.raw)
	}
}

func TestDoctorReportsATLSBundleItCannotRead(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{})
	env := doctorEnv(t, jira.URL, withCredential)
	missing := filepath.Join(t.TempDir(), "corporate-root.pem")

	doc := runDoctorArgs(t, env, "--ca-bundle", missing)

	failedCheck(t, doc, "transport", "INVALID_CA_BUNDLE")
	// The path is reported on the failure, not only on success: it is the
	// thing somebody has to go and look at.
	if got := doc.check(t, "transport").attr("ca-bundle"); got != missing {
		t.Errorf("ca-bundle = %q, want %q", got, missing)
	}
	if got := doc.check(t, "transport").attr("ca-bundle-source"); got != "flag" {
		t.Errorf("ca-bundle-source = %q, want flag", got)
	}
	if got := doc.check(t, "deployment").Status; got != "skipped" {
		t.Errorf("the deployment check is %q, want skipped: there is no "+
			"connection to probe with:\n%s", got, doc.raw)
	}
}

func TestDoctorReportsADeploymentProbeThatFailed(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{probeStatus: http.StatusInternalServerError})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	if got := doc.check(t, "deployment").Status; got != "failed" {
		t.Errorf("the deployment check is %q, want failed:\n%s", got, doc.raw)
	}
	for _, name := range []string{"clock", "account", "limits"} {
		if got := doc.check(t, name).Status; got != "skipped" {
			t.Errorf("the %s check is %q, want skipped:\n%s", name, got, doc.raw)
		}
	}
}

func TestDoctorReportsAClockThatDisagrees(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{skew: -10 * time.Minute})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	failedCheck(t, doc, "clock", "CLOCK_SKEW")
	skew, err := strconv.Atoi(doc.check(t, "clock").attr("skew-seconds"))
	if err != nil {
		t.Fatalf("skew-seconds is not an integer: %v", err)
	}
	// Local minus server, so a site running ten minutes behind reads as this
	// machine being ahead. The window is wide because the two clocks are read a
	// few milliseconds apart.
	if skew < 590 || skew > 610 {
		t.Errorf("skew-seconds = %d, want about 600", skew)
	}
	if !strings.Contains(doc.check(t, "clock").Summary, "ahead") {
		t.Errorf("the summary does not name the direction: %q",
			doc.check(t, "clock").Summary)
	}
	// The other checks are unaffected: a skewed clock is not a broken site.
	if got := doc.check(t, "account").Status; got != "ok" {
		t.Errorf("the account check is %q, want ok:\n%s", got, doc.raw)
	}
}

// TestDoctorAcceptsAClockInsideTheLimit is the other side of the threshold. A
// check that failed for every input would pass this suite while reporting
// nothing.
func TestDoctorAcceptsAClockInsideTheLimit(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{skew: -30 * time.Second})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	if got := doc.check(t, "clock").Status; got != "ok" {
		t.Errorf("the clock check is %q for half a minute of skew, and the "+
			"limit is a minute:\n%s", got, doc.raw)
	}
}

func TestDoctorReportsAnAccountTheCredentialCannotReach(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{selfStatus: http.StatusUnauthorized})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	failedCheck(t, doc, "account", "UNAUTHORIZED")
	// The deployment answered anonymously, which is exactly the gap this check
	// exists to close: a reachable site proves nothing about the token.
	if got := doc.check(t, "deployment").Status; got != "ok" {
		t.Errorf("the deployment check is %q, want ok:\n%s", got, doc.raw)
	}
}

func TestDoctorReportsASiteThatAdvertisesNoRateLimit(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{limits: false})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	limits := doc.check(t, "limits")
	if limits.Status != "ok" {
		t.Errorf("the limits check is %q; a site that advertises nothing has "+
			"answered, and that is not a failure:\n%s", limits.Status, doc.raw)
	}
	if !strings.Contains(limits.Summary, "no rate-limit policy") {
		t.Errorf("the summary does not say the site advertises nothing: %q",
			limits.Summary)
	}
	if got := limits.attr("policy"); got != "" {
		t.Errorf("policy = %q, and the site sent no such header", got)
	}
}

func TestDoctorReportsTheRateLimitPolicyVerbatim(t *testing.T) {
	jira := newDoctorSite(t, doctorSite{limits: true})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	limits := doc.check(t, "limits")
	if got := limits.attr("policy"); got != ratelimitPolicy {
		t.Errorf("policy = %q, want the header verbatim, %q", got, ratelimitPolicy)
	}
	if got := limits.attr("remaining"); got != "348" {
		t.Errorf("remaining = %q, want 348", got)
	}
}

// TestDoctorSkipsTheLimitsItNeverSaw covers the one way the limits check does
// not report: the response that would have carried the headers never arrived.
func TestDoctorSkipsTheLimitsItNeverSaw(t *testing.T) {
	// The probe answers and the clock request, which is the second call to the
	// same endpoint, does not. That is the only way to fail one and not the
	// other, and it is what makes the two separate checks over one request.
	jira := newDoctorSite(t, doctorSite{limits: true, clockStatus: http.StatusBadGateway})
	env := doctorEnv(t, jira.URL, withCredential)

	doc := runDoctor(t, env)

	if got := doc.check(t, "clock").Status; got != "failed" {
		t.Errorf("the clock check is %q, want failed:\n%s", got, doc.raw)
	}
	if got := doc.check(t, "limits").Status; got != "skipped" {
		t.Errorf("the limits check is %q, want skipped: the response that "+
			"carries the headers never arrived:\n%s", got, doc.raw)
	}
}

// TestDoctorAlwaysExitsZeroAndReportsEveryCheck is the contract itself.
//
// Exit 0 whatever it finds, because a diagnostic that exited non-zero on a
// finding would make "did the diagnostic run" and "is this healthy" the same
// signal. And every check present in every run, with a status from the closed
// set, because the alternative is a document a consumer cannot tell apart from
// a partial one.
func TestDoctorAlwaysExitsZeroAndReportsEveryCheck(t *testing.T) {
	legal := []string{"ok", "failed", "skipped"}

	for _, tc := range []struct {
		name string
		env  func(t *testing.T) map[string]string
		args []string
	}{
		{name: "nothing configured", env: func(t *testing.T) map[string]string {
			t.Helper()
			return isolate(t, nil)
		}},
		{name: "an unreadable config", env: func(t *testing.T) map[string]string {
			t.Helper()
			env := isolate(t, nil)
			writeConfig(t, env, "[[[")
			return env
		}},
		{name: "a site that is not there", env: func(t *testing.T) map[string]string {
			t.Helper()
			// A reserved TLD, so a run that somehow dialled it reaches nothing.
			return doctorEnv(t, "https://nowhere.invalid", withCredential)
		}},
		{
			name: "an unknown context",
			env: func(t *testing.T) map[string]string {
				t.Helper()
				return doctorEnv(t, "https://nowhere.invalid", withCredential)
			},
			args: []string{"--context", "nope"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := runDoctorArgs(t, tc.env(t), tc.args...)

			for _, name := range doctorChecks {
				c := doc.check(t, name)
				if !slices.Contains(legal, c.Status) {
					t.Errorf("the %s check reports status %q, which is not one "+
						"of %v", name, c.Status, legal)
				}
				if strings.TrimSpace(c.Summary) == "" {
					t.Errorf("the %s check reports no summary, so a reader has "+
						"to open the source to learn what it looked at", name)
				}
			}
		})
	}
}

// The harness.

const (
	credentialToken = "not-a-real-token"
	ratelimitPolicy = `"jira-burst-based";q=100;w=1`
)

const (
	withCredential    = true
	withoutCredential = false
)

// doctorSite is what the site answers. The zero value is a healthy Cloud site
// that advertises no rate-limit policy, which is a real deployment: a default
// Data Center sends no such headers at all.
type doctorSite struct {
	// skew is added to the clock it reports, so a negative one is a site
	// running behind this machine.
	skew time.Duration
	// limits advertises a rate-limit policy on the serverInfo response.
	limits bool
	// probeStatus, clockStatus, and selfStatus refuse one request each. The
	// first two are the same endpoint: `jr doctor` calls serverInfo twice, once
	// for the deployment and once for the clock, because a cached clock is not
	// a clock.
	probeStatus int
	clockStatus int
	selfStatus  int
}

// newDoctorSite starts a site that answers the three requests doctor makes.
func newDoctorSite(t *testing.T, cfg doctorSite) *httptest.Server {
	t.Helper()

	var serverInfoCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/serverInfo"):
			status := cfg.probeStatus
			if serverInfoCalls.Add(1) > 1 && cfg.clockStatus != 0 {
				status = cfg.clockStatus
			}
			if status != 0 {
				refuse(w, status)
				return
			}
			if cfg.limits {
				w.Header().Set("Ratelimit", `"jira-burst-based";r=348;t=1`)
				w.Header().Set("Ratelimit-Policy", ratelimitPolicy)
				w.Header().Set("X-RateLimit-Limit", "350")
				w.Header().Set("X-RateLimit-Remaining", "348")
			}
			now := time.Now().UTC().Add(cfg.skew)
			answer(w, fmt.Sprintf(
				`{"baseUrl":"%s","version":"1001.0.0","versionNumbers":[1001,0,0],`+
					`"deploymentType":"Cloud","serverTime":"%s"}`,
				"https://fake.invalid", now.Format("2006-01-02T15:04:05.000-0700")))
		case strings.HasSuffix(r.URL.Path, "/myself"):
			if cfg.selfStatus != 0 {
				refuse(w, cfg.selfStatus)
				return
			}
			answer(w, `{"accountId":"5f8a1","displayName":"Ada Lovelace",`+
				`"emailAddress":"ada@example.com","active":true,`+
				`"timeZone":"America/Chicago"}`)
		default:
			refuse(w, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func answer(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"errorMessages":["no"],"errors":{}}`))
}

// doctorEnv is an XDG root pointing at one site, with or without a credential
// stored for it.
func doctorEnv(t *testing.T, siteURL string, credential bool) map[string]string {
	t.Helper()

	env := isolate(t, nil)
	writeConfig(t, env, fmt.Sprintf(
		"current = \"default\"\n\n[contexts.default]\nsite = %q\n", siteURL))
	if !credential {
		return env
	}

	dir := filepath.Join(env["XDG_STATE_HOME"], "jr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Keyed by host and port, which is what the store writes and what a lookup
	// for a full site URL resolves to.
	host := strings.TrimPrefix(strings.TrimPrefix(siteURL, "https://"), "http://")
	store := fmt.Sprintf("[credentials.%q]\nscheme = \"basic\"\n"+
		"user = \"ada@example.com\"\ntoken = %q\n", host, credentialToken)
	// 0600 or the store is refused on read, which would make every one of these
	// tests a test of the file mode.
	if err := os.WriteFile(filepath.Join(dir, "credentials.toml"), []byte(store), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	return env
}

func writeConfig(t *testing.T, env map[string]string, body string) {
	t.Helper()

	dir := filepath.Join(env["XDG_CONFIG_HOME"], "jr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func runDoctor(t *testing.T, env map[string]string) doctorDoc {
	t.Helper()
	return runDoctorArgs(t, env)
}

// runDoctorArgs runs the command and parses what it wrote, asserting the two
// things that hold for every invocation: exit 0, and nothing on stderr.
func runDoctorArgs(t *testing.T, env map[string]string, args ...string) doctorDoc {
	t.Helper()

	got := run(t, env, append([]string{"doctor"}, args...)...)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %d (%s), and this command exits 0 whatever it finds\n"+
			"stderr: %s", got.exit, got.exit.Name(), got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr is not empty:\n%s", got.stderr)
	}

	var doc doctorResult
	if err := xml.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, got.stdout)
	}
	doc.Doctor.raw = got.stdout
	return doc.Doctor
}

// failedCheck asserts a check failed, and with which code when one is named.
func failedCheck(t *testing.T, doc doctorDoc, name, code string) {
	t.Helper()

	c := doc.check(t, name)
	if c.Status != "failed" {
		t.Fatalf("the %s check is %q, want failed:\n%s", name, c.Status, doc.raw)
	}
	if code != "" && c.Code != code {
		t.Errorf("the %s check failed with code %q, want %q:\n%s",
			name, c.Code, code, doc.raw)
	}
	if strings.TrimSpace(c.Summary) == "" {
		t.Errorf("the %s check failed and said nothing about it:\n%s", name, doc.raw)
	}
}

type doctorResult struct {
	XMLName xml.Name  `xml:"result"`
	Doctor  doctorDoc `xml:"doctor"`
}

type doctorDoc struct {
	Status  string        `xml:"status,attr"`
	Count   int           `xml:"checks,attr"`
	Failed  int           `xml:"failed,attr"`
	Skipped int           `xml:"skipped,attr"`
	Checks  []doctorCheck `xml:",any"`

	// raw is the document as written, for a failure message that shows what was
	// actually produced rather than what this struct managed to decode.
	raw string
}

type doctorCheck struct {
	XMLName xml.Name
	Status  string     `xml:"status,attr"`
	Code    string     `xml:"code,attr"`
	Summary string     `xml:"summary"`
	Detail  string     `xml:"detail"`
	Remedy  string     `xml:"remedy"`
	Attrs   []xml.Attr `xml:",any,attr"`
}

func (d doctorDoc) check(t *testing.T, name string) doctorCheck {
	t.Helper()

	for _, c := range d.Checks {
		if c.XMLName.Local == name {
			return c
		}
	}
	t.Fatalf("no %s check in the document, and every check is reported in "+
		"every run:\n%s", name, d.raw)
	return doctorCheck{}
}

func (c doctorCheck) attr(name string) string {
	for _, a := range c.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
