package cli

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/jctx"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// The kind `jr doctor` owns.
//
// v2 added the observed connection to the transport check: `tls`,
// `tls-version`, and `verified-against`, present once a response has arrived.
// Every addition is optional, so a consumer reading v1 reads v2 unchanged.
const (
	kindDoctor    = "doctor"
	versionDoctor = 2
)

// The verdict one check carries.
const (
	// statusOK means the check ran and found nothing wrong.
	statusOK = "ok"
	// statusFailed means the check ran and found something that will break
	// commands.
	statusFailed = "failed"
	// statusSkipped means the check could not run because something it depends
	// on did not pass. It names what it was waiting on.
	statusSkipped = "skipped"
)

// clockSkewLimit is where a difference between two clocks stops being a
// curiosity and starts changing answers.
//
// A minute, because a minute is JQL's finest bound: no operator this tool can
// send bisects one, which docs/output-contract.md states and the worklog
// measurement of 2026-08-14 established on both deployments. A machine whose
// clock is a minute out therefore asks Jira for a different window than the one
// it computed, and below that the two agree on every query this tool can build.
const clockSkewLimit = time.Minute

func (a *app) doctorCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"doctor"},
		Summary: "Explain, layer by layer, why this tool will not work here",
		Description: strings.TrimSpace(`
Runs every check between this binary and an answer from Jira and reports all of
them: the configuration, the credential, the site URL and its context path, the
proxy and TLS settings, the deployment probe, the clock, the account, and
whatever the site discloses about rate limits.

The transport check reports the TLS settings that were configured and, once a
response has arrived, the connection that carried it: whether it was encrypted,
at which version, and whether the chain verified against the system trust store
or needed the bundle. A bundle the chain never needed is a setting doing
nothing, and a version below what the site speaks is a middlebox nobody
mentioned; neither is knowable from configuration.

It exists because the interesting failures are the ones where the first request
fails and the reason is three layers below it. A 401 from a Data Center under a
context path is indistinguishable, from the error alone, from a 401 because the
token expired, a 401 because a proxy stripped the header, and a 401 because the
site URL lost its context path and the request reached a different application.

**It exits 0 whenever the checks ran, whatever they found.** A diagnostic that
exited non-zero on a finding would make "did the diagnostic run" and "is this
configuration healthy" the same signal, and separating those two is the whole
point. The verdicts are in the document: every check carries ok, failed, or
skipped, and a failed one carries the same code, detail, and remedy the failing
layer would have produced on its own.

Every check is reported, including the ones that passed. A diagnostic that
prints only problems cannot tell a healthy configuration from a check that
never ran.

It needs no credential and no reachable site, and it never refuses to run: what
it cannot check it reports as skipped, naming the check it was waiting on. A
site that answers anonymously is still probed when no credential is stored, so
"this site is reachable and is Data Center 10.4" is reported to somebody whose
real problem is that they never logged in.

The deployment probe is read from its cache when a valid entry exists, and says
which. Pass --refresh to force it. The clock is never cached, because a cached
clock is not a clock.`),
		Example: strings.Join([]string{
			buildinfo.App + " doctor",
			buildinfo.App + " doctor --format json",
			buildinfo.App + " doctor --context work --refresh",
		}, "\n"),
		Outputs: []registry.Output{{Kind: kindDoctor, Version: versionDoctor}},
		// No exit codes beyond the universal 0, 1, and 2. That is the contract
		// rather than an omission: every failure this command is about is a
		// verdict inside its document. See runDoctor.
		Run: a.runDoctor,
	}
}

// runDoctor runs every check and reports all of them.
//
// It returns an error for none of what the checks are about. A configuration
// this tool cannot use, a site that does not answer, a credential nobody
// stored: each is a verdict here rather than a refusal, because a command that
// refused to describe a broken environment would be refusing at the one moment
// it is the only useful command left.
//
// The command declares NeedsJira: false for the same reason. The CLI builds a
// session before a command body runs, so a command that declared it and could
// not build one would never execute, which is precisely the case this exists
// for.
func (a *app) runDoctor(ctx context.Context, _ *registry.Invocation) (*render.Doc, error) {
	d := &doctorRun{app: a}

	config := d.checkConfig()
	credential := d.checkCredential()
	d.connect()
	reachable := d.checkSite()
	deployment := d.checkDeployment(ctx)
	clock, limits := d.checkClockAndLimits(ctx)
	account := d.checkAccount(ctx)
	// Built last, in its place in the document: it reports the connection
	// the three checks above made, and it depends on none of their verdicts.
	// The deployment probe may come from the cache, and the clock never
	// does, so by here a response has arrived if one was going to.
	wire := d.checkTransport()

	return render.Record(kindDoctor, versionDoctor, tally([]*render.Node{
		config, credential, reachable, wire, deployment, clock, account, limits,
	})), nil
}

// doctorRun carries what one check learned to the checks that depend on it.
//
// The dependencies are the layering itself: nothing probes a deployment without
// a connection, and nothing builds a connection without a site. A check whose
// input is missing is skipped and names what it was waiting on, rather than
// reporting a failure of its own that would be one cause counted twice.
type doctorRun struct {
	app *app

	// session is this invocation's session, built from the resolved
	// configuration. It is here for its probe: the deployment check has to read
	// the same cache, honour the same --api-version, and write the same entry
	// as every other command, and that logic lives on the session already.
	session *session

	cred     auth.Credential
	haveCred bool

	// recorder is set only when JIRA_RECORD names a file. `jr doctor` makes
	// exactly the three requests a fixture of this layer would want, which is
	// the reason it is wired up here at all.
	recorder *transport.Recorder

	client     *transport.Client
	connectErr error

	info     site.Info
	haveInfo bool
}

// checkConfig resolves the configuration every other check reads.
func (d *doctorRun) checkConfig() *render.Node {
	cfg, err := d.app.config()
	if err != nil {
		return checkFailed("config", err)
	}
	resolved, err := d.app.resolve(cfg)
	if err != nil {
		return checkFailed("config", err).Attr("path", cfg.Path())
	}
	d.session = &session{app: d.app, resolved: resolved}

	n := checkOK("config", configSummary(resolved, cfg.Path())).
		Attr("path", cfg.Path()).
		Attr("contexts", strconv.Itoa(len(cfg.Names()))).
		Attr("context", resolved.Name).
		Attr("site-source", string(resolved.SiteSource)).
		Attr("readonly", strconv.FormatBool(resolved.ReadOnly))
	if resolved.ReadOnly {
		// Which source latched it. Read-only is a one-way latch from three
		// sources, so "why can I not write" has three possible answers and only
		// one of them is the file somebody would think to look in.
		n.Attr("readonly-source", string(resolved.ReadOnlySource))
	}
	if resolved.APIVersion != 0 {
		n.Attr("api-version", strconv.Itoa(resolved.APIVersion))
	}
	return n
}

// configSummary is the sentence: which context, and which file it came from.
func configSummary(r *jctx.Resolved, path string) string {
	if r.Name == "" {
		return "no context selected, config at " + path
	}
	return "context " + strconv.Quote(r.Name) + ", from " + path
}

// checkCredential reports which credential a request would carry, and from
// where, without revealing it.
func (d *doctorRun) checkCredential() *render.Node {
	if d.session == nil {
		return checkSkipped("credential", waitingOn("config"))
	}
	chain := d.app.chain()
	searched := strings.Join(chain.Sources(), ", ")
	siteURL := d.session.resolved.Site

	cred, found, err := chain.Lookup(siteURL)
	switch {
	case err != nil:
		return checkFailed("credential", err).Attr("searched", searched)
	case !found:
		// Not chain.Resolve, whose message interpolates the site: with no site
		// configured that reads "no credentials for ", and this is the command
		// somebody runs when nothing is configured.
		return checkFailed("credential", errs.Auth("NO_CREDENTIALS",
			"no credential is stored for this site").
			WithDetail("looked in: %s", searched).
			WithRemedy("run `%s auth login --site <host>`, or set %s (with %s on Cloud)",
				buildinfo.App, auth.EnvToken, auth.EnvEmail)).
			Attr("searched", searched)
	}
	if err := cred.Validate(); err != nil {
		return checkFailed("credential", err).
			Attr("source", cred.Source).
			Attr("searched", searched)
	}
	d.cred, d.haveCred = cred, true

	return checkOK("credential", "a "+string(cred.Scheme)+" credential from "+cred.Source).
		Attr("source", cred.Source).
		Attr("scheme", string(cred.Scheme)).
		Attr("user", cred.User).
		Attr("site-scoped", strconv.FormatBool(cred.SiteScoped)).
		Attr("searched", searched)
}

// connect builds the client the network checks share.
//
// Deliberately not session.Connect. That resolves a credential, builds a
// client, and probes the deployment behind a single error, which is right for a
// command that wants an answer and wrong for this one: the subject here is
// which of those three failed, and one error cannot say. What it costs is this
// function, which is session.connect without the parts that decide.
func (d *doctorRun) connect() {
	if d.session == nil || d.session.resolved.Site == "" {
		return
	}
	siteURL := d.session.resolved.Site

	opts := transport.Options{
		BaseURL:     siteURL,
		Retries:     d.app.retries,
		MaxRequests: d.app.maxRequests,
		UserAgent:   userAgent(),
		Tracer:      d.app.tracer(),
		TLS: transport.TLSOptions{
			CABundle:   d.session.resolved.CABundle,
			ClientCert: d.session.resolved.ClientCert,
			ClientKey:  d.session.resolved.ClientKey,
		},
	}
	// Anonymous rather than not at all when no credential was found. The
	// deployment probe answers without one on most instances, and "the site is
	// reachable and is Data Center 10.4" is worth reporting to somebody whose
	// actual problem is that they never logged in.
	if d.haveCred {
		opts.Auth = authorizerFor(d.cred)
	}
	if rec, save := d.app.recorder(siteURL); rec != nil {
		rec.Cassette().Source = transport.Recorded
		opts.RoundTripper = rec
		d.recorder = rec
		d.app.cleanup = append(d.app.cleanup, save)
	}

	client, err := transport.New(opts)
	if err != nil {
		d.connectErr = err
		return
	}
	d.client = client
}

// checkSite reports where a request would be sent.
func (d *doctorRun) checkSite() *render.Node {
	if d.session == nil {
		return checkSkipped("site", waitingOn("config"))
	}
	siteURL, err := d.session.resolved.RequireSite()
	if err != nil {
		return checkFailed("site", err)
	}
	path := contextPathOf(siteURL)

	// The endpoint comes from the transport's own resolution rather than from a
	// join performed here. A base and an endpoint that disagree about the
	// context path is the failure this line exists to make visible, and a
	// second opinion about it would be the bug wearing a diagnostic's clothes.
	endpoint := ""
	if d.client != nil {
		if endpoint, err = d.client.URLFor(site.ProbePath); err != nil {
			return checkFailed("site", err).Attr("url", siteURL)
		}
	}
	return checkOK("site", siteSummary(siteURL, path)).
		Attr("url", siteURL).
		Attr("context-path", path).
		AttrIf("endpoint", endpoint)
}

// contextPathOf is the path a Data Center is served under, or empty for a site
// at the server root.
func contextPathOf(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

func siteSummary(siteURL, path string) string {
	if path == "" {
		return siteURL + ", at the server root"
	}
	return siteURL + ", under context path " + path
}

// checkTransport reports what the connection is made through: the proxy nobody
// configured here, the TLS settings somebody did, and, once a response has
// arrived, what the connection that carried it actually did.
func (d *doctorRun) checkTransport() *render.Node {
	if d.session == nil {
		return checkSkipped("transport", waitingOn("config"))
	}
	r := d.session.resolved
	proxy := transport.ProxyFor(r.Site)

	// What the last response's connection looked like, when one arrived.
	// Observed rather than derived: whether the bundle was needed and which
	// version was negotiated are properties of a connection, and the
	// configured chain cannot answer either.
	var conn transport.Connection
	observed := false
	if d.client != nil {
		conn, observed = d.client.LastConnection()
	}

	// The same attributes whatever the verdict: what was configured is worth
	// reporting most when the connection could not be built, since it is the
	// list of things to go and check.
	settings := func(n *render.Node) *render.Node {
		n.AttrIf("proxy", proxy).
			AttrIf("ca-bundle", r.CABundle).
			AttrIf("client-cert", r.ClientCert).
			AttrIf("client-key", r.ClientKey)
		if r.CABundle != "" {
			n.Attr("ca-bundle-source", string(r.CABundleSource))
		}
		// Present only when a connection was observed. A site that never
		// answered and a replayed fixture both leave these absent, because
		// absent is the honest answer for both: `tls="false"` is a plain
		// http:// site that answered, never a chain nobody verified.
		if observed {
			n.Attr("tls", strconv.FormatBool(conn.TLS)).
				AttrIf("tls-version", conn.Version).
				AttrIf("verified-against", conn.VerifiedAgainst)
		}
		return n
	}

	switch {
	case d.connectErr != nil:
		return settings(checkFailed("transport", d.connectErr))
	case d.client == nil:
		return settings(checkSkipped("transport", waitingOn("site")))
	}
	return settings(checkOK("transport",
		transportSummary(proxy, r.CABundle, r.ClientCert, conn, observed)))
}

// transportSummary names what is in play, and says so when nothing is: "the
// system trust store, no proxy" is a finding, and an empty line is not.
//
// Once a response has arrived it says what the connection did rather than
// what it was configured to do, because the two differ in exactly the cases
// worth reading: a bundle the chain never needed, a version below what the
// site speaks. Before one arrives it says that too, so the configured chain is
// not mistaken for a verified one.
func transportSummary(proxy, bundle, clientCert string, conn transport.Connection, observed bool) string {
	var parts []string
	switch {
	case !observed:
		parts = append(parts, configuredTrust(bundle))
	case !conn.TLS:
		parts = append(parts, "plain HTTP, nothing to verify")
	default:
		parts = append(parts, conn.Version, verifiedSentence(conn, bundle))
	}
	if clientCert != "" {
		parts = append(parts, "presenting "+clientCert)
	}
	if proxy == "" {
		parts = append(parts, "no proxy")
	} else {
		parts = append(parts, "through "+proxy)
	}
	if !observed {
		parts = append(parts, "no connection observed")
	}
	return strings.Join(parts, ", ")
}

// configuredTrust is what a connection would verify against, before one has.
func configuredTrust(bundle string) string {
	if bundle == "" {
		return "the system trust store"
	}
	return "the system trust store plus " + bundle
}

// verifiedSentence is what the chain did verify against, and names the bundle
// that was configured for nothing when that is what happened.
func verifiedSentence(conn transport.Connection, bundle string) string {
	switch {
	case conn.VerifiedAgainst == transport.VerifiedAgainstBundle:
		return "verified against " + bundle
	case bundle != "":
		return "verified against the system trust store (" + bundle + " was not needed)"
	}
	return "verified against the system trust store"
}

// checkDeployment reports what the site is, and whether that was learned just
// now.
func (d *doctorRun) checkDeployment(ctx context.Context) *render.Node {
	if d.client == nil {
		return checkSkipped("deployment", waitingOn("transport"))
	}
	info, err := d.session.probe(ctx, d.client, d.session.resolved.Site)
	if err != nil {
		return checkFailed("deployment", err)
	}
	d.info, d.haveInfo = info, true
	if d.recorder != nil {
		d.recorder.Cassette().Deployment = recordedDeployment(info)
	}

	n := checkOK("deployment", deploymentSummary(info)).
		Attr("kind", string(info.Kind)).
		Attr("api-base", info.APIBase()).
		Attr("source", deploymentSource(info)).
		AttrIf("version", info.Version)
	if !info.ProbedAt.IsZero() {
		n.Attr("probed", info.ProbedAt.UTC().Format(time.RFC3339))
	}
	return n
}

// deploymentSource says whether this answer was probed, read from the cache, or
// declared, because "the probe says Cloud" and "a file written yesterday says
// Cloud" are different claims and only one of them is current.
func deploymentSource(info site.Info) string {
	switch {
	case info.Declared != 0:
		return "declared"
	case info.Cached:
		return "cache"
	}
	return "probe"
}

func deploymentSummary(info site.Info) string {
	out := string(info.Kind)
	if info.Version != "" {
		out += " " + info.Version
	}
	return out + ", from the " + deploymentSource(info) + ", serving " + info.APIBase()
}

// checkClockAndLimits reports the two things one serverInfo response discloses.
//
// They are one request and therefore one check function. Asking twice would be
// a second round trip for a header the first response already carried.
func (d *doctorRun) checkClockAndLimits(ctx context.Context) (*render.Node, *render.Node) {
	if !d.haveInfo {
		return checkSkipped("clock", waitingOn("deployment")),
			checkSkipped("limits", waitingOn("deployment"))
	}
	status, err := site.ReadStatus(ctx, d.client, d.info)
	if err != nil {
		return checkFailed("clock", err), checkSkipped("limits", waitingOn("clock"))
	}
	return clockNode(status.Time, time.Now().UTC()), limitsNode(status.Limits)
}

// clockNode compares the two clocks that have to agree.
//
// Skew is invisible to every other command and breaks JQL date bounds, feed
// cursors, and any `created >= -1m` in a script. The verdict is built as an
// errs.Error like any other, though no layer refuses over it, because to a
// reader a finding is a finding and it should read the same either way.
func clockNode(server, local time.Time) *render.Node {
	skew := local.Sub(server)
	return clockVerdict(skew).
		Attr("server", server.UTC().Format(time.RFC3339)).
		Attr("local", local.UTC().Format(time.RFC3339)).
		Attr("skew-seconds", strconv.Itoa(int(skew.Round(time.Second)/time.Second)))
}

// clockVerdict is the judgement alone, so the three attributes above are
// reported the same way whichever way it went.
func clockVerdict(skew time.Duration) *render.Node {
	apart := skew.Round(time.Second).Abs()
	if skew < clockSkewLimit && skew > -clockSkewLimit {
		return checkOK("clock", "this machine and the site agree to within "+apart.String())
	}
	return checkFailed("clock", errs.Runtime("CLOCK_SKEW",
		"this machine's clock is %s the site's by %s", behindOrAhead(skew), apart).
		WithDetail("no operator this tool can send bounds a query finer than a "+
			"minute, so a relative date computed here asks the site for a "+
			"different window than the one it was meant to name").
		WithRemedy("synchronise this machine's clock against NTP, or run from a "+
			"host that already is"))
}

// behindOrAhead names the direction, which decides what a wrong answer looks
// like: a client running behind the site claims to have reported through an
// instant the site has not reached.
func behindOrAhead(skew time.Duration) string {
	if skew < 0 {
		return "behind"
	}
	return "ahead of"
}

// limitsNode reports what the site says about throttling, including when it
// says nothing.
func limitsNode(l transport.Limits) *render.Node {
	if !l.Disclosed() {
		// A default Data Center sends no rate-limit headers at all, measured
		// 2026-08-17 against 10.4.0. That is an answer and not a gap.
		return checkOK("limits", "this site advertises no rate-limit policy")
	}
	return checkOK("limits", "advertised: "+firstNonEmpty(l.Policy, l.State, l.Limit)).
		AttrIf("policy", l.Policy).
		AttrIf("state", l.State).
		AttrIf("limit", l.Limit).
		AttrIf("remaining", l.Remaining)
}

// checkAccount asks the site who the credential is, which is the only check
// that proves a credential works: the deployment probe answers anonymously on
// most instances and so proves nothing about the token.
func (d *doctorRun) checkAccount(ctx context.Context) *render.Node {
	switch {
	case !d.haveInfo:
		return checkSkipped("account", waitingOn("deployment"))
	case !d.haveCred:
		return checkSkipped("account",
			"no credential was found, and this asks the site who the credential is")
	}
	account, err := site.Whoami(ctx, d.client, d.info)
	if err != nil {
		// explainScheme names the commonest way to get this wrong, which is a
		// Data Center personal access token paired with --email and therefore
		// sent as Basic. Both halves are known here, so the guess is not one.
		return checkFailed("account", explainScheme(err, d.info, d.cred))
	}

	// No email address. This document is what somebody pastes into a bug
	// report, and the address adds nothing the account id and the display name
	// do not already say.
	return checkOK("account", account.Display+" ("+account.ID+")").
		Attr("id", account.ID).
		Attr("display", account.Display).
		Attr("active", strconv.FormatBool(account.Active)).
		AttrIf("timezone", account.TimeZone)
}

// tally is the roll-up a caller reads before reading anything else.
func tally(checks []*render.Node) *render.Node {
	failed, skipped := 0, 0
	for _, c := range checks {
		switch statusOf(c) {
		case statusFailed:
			failed++
		case statusSkipped:
			skipped++
		}
	}

	status := statusOK
	switch {
	case failed > 0:
		status = statusFailed
	case skipped > 0:
		status = statusSkipped
	}

	n := render.El("doctor").
		Attr("status", status).
		Attr("checks", strconv.Itoa(len(checks))).
		Attr("failed", strconv.Itoa(failed)).
		Attr("skipped", strconv.Itoa(skipped))
	for _, c := range checks {
		n.Child(c)
	}
	return n
}

func statusOf(n *render.Node) string {
	v, _ := n.AttrValue("status")
	return v
}

// waitingOn is what a skipped check says: the check it needed, by name, so a
// reader follows the chain up to the one thing that is wrong instead of reading
// six failures for one cause.
func waitingOn(check string) string {
	return "the " + check + " check did not pass"
}

// checkOK reports a layer that is working.
func checkOK(name, summary string) *render.Node {
	return render.El(name).Attr("status", statusOK).Leaf("summary", summary)
}

// checkFailed reports a layer that is not, from the error that says so.
//
// The code, the detail, and the remedy are the ones the failing layer already
// wrote. A diagnostic that paraphrased them would be a second opinion, and it
// would go stale the first time the layer improved its own message.
func checkFailed(name string, err error) *render.Node {
	e := errs.Coerce(err)
	return render.El(name).
		Attr("status", statusFailed).
		Attr("code", e.Code).
		Leaf("summary", e.Message).
		LeafIf("detail", e.Detail).
		LeafIf("remedy", e.Remedy)
}

// checkSkipped reports a check that could not run, and why.
func checkSkipped(name, why string) *render.Node {
	return render.El(name).Attr("status", statusSkipped).Leaf("summary", why)
}

// The doctor kind is registered here rather than in schemas.go with the other
// built-ins, because eight nested shapes have to agree with the eight builders
// above them attribute by attribute, and the shortest distance between two
// things that must agree is the same file.
func init() {
	render.RegisterSchema(kindDoctor, doctorSchema())
}

// doctorSchema is the shape of one run: a roll-up, and one element per check in
// the order the layers stack.
//
// Every check element carries the same three things (a verdict, a sentence, and
// what the failing layer said) plus its own facts. Those facts are optional on
// every check, because a check that was skipped has none of them and a check
// that failed has whichever it got to before it failed.
func doctorSchema() *render.Schema {
	return &render.Schema{
		Element: "doctor",
		Attrs: []render.Field{
			// The roll-up: failed if any check failed, else skipped if any was
			// skipped, else ok. A caller that branches on one attribute branches
			// on this one.
			{Name: "status", Type: render.TypeString, Enum: checkStatuses},
			{Name: "checks", Type: render.TypeInt},
			{Name: "failed", Type: render.TypeInt},
			{Name: "skipped", Type: render.TypeInt},
		},
		Children: []render.Child{
			{Schema: configCheckSchema()},
			{Schema: credentialCheckSchema()},
			{Schema: siteCheckSchema()},
			{Schema: transportCheckSchema()},
			{Schema: deploymentCheckSchema()},
			{Schema: clockCheckSchema()},
			{Schema: accountCheckSchema()},
			{Schema: limitsCheckSchema()},
		},
	}
}

// checkStatuses is the closed set of verdicts, published so a consumer can
// branch on all three rather than on "not ok".
var checkStatuses = []string{statusOK, statusFailed, statusSkipped}

// checkSchema is what every check shares: the verdict, the sentence under it,
// and, when one failed, the code, detail, and remedy the failing layer wrote.
func checkSchema(element string, attrs ...render.Field) *render.Schema {
	return &render.Schema{
		Element: element,
		Attrs: append([]render.Field{
			{Name: "status", Type: render.TypeString, Enum: checkStatuses},
			// Present only on a failure, and it is the failing layer's own code:
			// the same string the same failure would carry out of any other
			// command, so docs/troubleshooting.md answers both.
			{Name: "code", Type: render.TypeString, Optional: true},
		}, attrs...),
		Children: []render.Child{
			// Always. A check that reported a status and no sentence would make
			// a reader open the source to find out what it had looked at.
			{Schema: render.Leaf("summary", render.TypeString)},
			{Schema: render.Leaf("detail", render.TypeString), Optional: true},
			{Schema: render.Leaf("remedy", render.TypeString), Optional: true},
		},
	}
}

func configCheckSchema() *render.Schema {
	return checkSchema("config",
		render.Field{Name: "path", Type: render.TypeString, Optional: true},
		render.Field{Name: "contexts", Type: render.TypeInt, Optional: true},
		// Empty when no context is selected, which is legitimate: --site alone
		// is a way to run a one-off command.
		render.Field{Name: "context", Type: render.TypeString, Optional: true},
		// Which of --site, JIRA_SITE, and the context supplied the site. Not an
		// enum, because "nothing did" is one of the answers.
		render.Field{Name: "site-source", Type: render.TypeString, Optional: true},
		render.Field{Name: "readonly", Type: render.TypeBool, Optional: true},
		// Which source latched read-only on, present only when it is on.
		render.Field{Name: "readonly-source", Type: render.TypeString, Optional: true},
		// Present only when --api-version or JIRA_API_VERSION forced one, which
		// is also what makes the deployment check report "declared".
		render.Field{Name: "api-version", Type: render.TypeInt, Optional: true})
}

func credentialCheckSchema() *render.Schema {
	return checkSchema("credential",
		// Where it came from, never what it is. The secret does not reach this
		// document's builder, the same way it does not reach `auth status`.
		render.Field{Name: "source", Type: render.TypeString, Optional: true},
		render.Field{
			Name: "scheme", Type: render.TypeString,
			Optional: true, Enum: auth.Schemes(),
		},
		render.Field{Name: "user", Type: render.TypeString, Optional: true},
		// False means the credential was not looked up for this site but merely
		// found: an exported token follows whatever site is pointed at.
		render.Field{Name: "site-scoped", Type: render.TypeBool, Optional: true},
		// Every provider that was consulted, in order, so a caller can see
		// which answered and which were skipped.
		render.Field{Name: "searched", Type: render.TypeString, Optional: true})
}

func siteCheckSchema() *render.Schema {
	return checkSchema("site",
		render.Field{Name: "url", Type: render.TypeString, Optional: true},
		// Empty for a site at the server root, which is every Cloud one. A Data
		// Center that lost its /jira reaches a different application and
		// answers 401 to everything, which is why this is stated rather than
		// implied by the URL above it.
		render.Field{Name: "context-path", Type: render.TypeString, Optional: true},
		// The absolute URL a request would go to, from the transport's own
		// resolution rather than from a join performed for the report.
		render.Field{Name: "endpoint", Type: render.TypeString, Optional: true})
}

func transportCheckSchema() *render.Schema {
	return checkSchema("transport",
		// The proxy the standard library would use, from HTTPS_PROXY and
		// NO_PROXY. Absent means none, and nobody configured it here either
		// way: a request going somewhere nobody chose looks like a network
		// fault.
		render.Field{Name: "proxy", Type: render.TypeString, Optional: true},
		// Paths, not contents. Nothing here is a secret, and the path is what
		// somebody has to go and check when a chain fails to verify.
		render.Field{Name: "ca-bundle", Type: render.TypeString, Optional: true},
		render.Field{Name: "ca-bundle-source", Type: render.TypeString, Optional: true},
		render.Field{Name: "client-cert", Type: render.TypeString, Optional: true},
		render.Field{Name: "client-key", Type: render.TypeString, Optional: true},
		// The connection a response arrived on, present only once one has.
		// Absent on a site that never answered and on a replayed fixture,
		// which are the two cases where "not verified" would be a claim about
		// a chain nobody looked at. tls is false for a plain http:// site.
		render.Field{Name: "tls", Type: render.TypeBool, Optional: true},
		// As crypto/tls names it, "TLS 1.3". A 1.2 against a site that speaks
		// 1.3 is a middlebox nobody mentioned.
		render.Field{Name: "tls-version", Type: render.TypeString, Optional: true},
		// system means the chain verified through the trust store, bundle or
		// no bundle; with a bundle configured that is the setting doing
		// nothing. bundle means no chain verified without it.
		render.Field{
			Name: "verified-against", Type: render.TypeString,
			Optional: true, Enum: transport.VerifiedAgainstValues,
		})
}

func deploymentCheckSchema() *render.Schema {
	return checkSchema("deployment",
		render.Field{
			Name: "kind", Type: render.TypeString, Optional: true,
			Enum: []string{string(site.Cloud), string(site.DataCenter)},
		},
		render.Field{Name: "version", Type: render.TypeString, Optional: true},
		render.Field{Name: "api-base", Type: render.TypeString, Optional: true},
		// Where the answer came from. "the probe says Cloud" and "a file
		// written yesterday says Cloud" are different claims, and a consumer
		// deciding whether to trust it needs to know which one it has.
		render.Field{
			Name: "source", Type: render.TypeString, Optional: true,
			Enum: []string{"probe", "cache", "declared"},
		},
		render.Field{Name: "probed", Type: render.TypeTimestamp, Optional: true})
}

func clockCheckSchema() *render.Schema {
	return checkSchema("clock",
		render.Field{Name: "server", Type: render.TypeTimestamp, Optional: true},
		render.Field{Name: "local", Type: render.TypeTimestamp, Optional: true},
		// Local minus server, so a positive number means this machine is ahead.
		// Seconds rather than a duration string, because the consumer of this
		// one is arithmetic.
		render.Field{Name: "skew-seconds", Type: render.TypeInt, Optional: true})
}

func accountCheckSchema() *render.Schema {
	return checkSchema("account",
		// An accountId on Cloud and a username on Data Center. The two are not
		// interchangeable, which is why the attribute is named for neither.
		render.Field{Name: "id", Type: render.TypeString, Optional: true},
		render.Field{Name: "display", Type: render.TypeString, Optional: true},
		render.Field{Name: "active", Type: render.TypeBool, Optional: true},
		// Jira evaluates every relative date and every startOf function in this
		// zone rather than in UTC or in the caller's, and nothing else in this
		// tool says which clock a query was answered on.
		render.Field{Name: "timezone", Type: render.TypeString, Optional: true})
}

func limitsCheckSchema() *render.Schema {
	return checkSchema("limits",
		// Verbatim, all four, because they are the server's own sentences about
		// its own policy. A default Data Center sends none of them and the
		// summary says so.
		render.Field{Name: "policy", Type: render.TypeString, Optional: true},
		render.Field{Name: "state", Type: render.TypeString, Optional: true},
		render.Field{Name: "limit", Type: render.TypeString, Optional: true},
		render.Field{Name: "remaining", Type: render.TypeString, Optional: true})
}
