//go:build write

package cli_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/idem"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestAWriteJiraMayHaveAppliedIsNotSentTwice holds every mutating command to
// §6.3 at the retry budget a caller actually runs with.
//
// On 2026-10-01 `issue create --idempotency-key` made two issues and reported
// one, exit 0: Jira made the first, a proxy answered 503, and the transport
// sent the POST again because a ledger key marked it replayable. The ledger
// guards a second invocation and is never consulted by a retry inside one.
// Every test that drove a keyed write built its transport with Retries: -1, so
// the retry that did the damage could not run in any of them.
//
// So this drives every mutating command the flag sweep can drive, with
// --idempotency-key wherever it is declared and without, against both
// deployments, at the default budget of three retries, through a fake that
// applies each write and answers the first one with 503. A request that is not
// idempotent by its method must reach the server at most once. A PUT or a
// DELETE may be sent again, which HTTP allows. An apply needs a real plan, so
// a command declaring --apply names the test that drives one.
func TestAWriteJiraMayHaveAppliedIsNotSentTwice(t *testing.T) {
	requireNothingIsWritten(t)
	var driven int
	for _, c := range cli.Registry().All() {
		if !c.Mutating {
			continue
		}
		if _, plans := c.Flag("apply"); plans && replayApplyCoveredBy[c.Name()] == "" {
			t.Errorf("%s declares --apply and names no test that applies a row "+
				"Jira answered 503 after doing; add it to replayApplyCoveredBy", c.Name())
		}
		if why := commandNotDriven(c); why != "" {
			if replayNotSwept[c.Name()] == "" {
				t.Errorf("%s mutates and cannot be driven here (%s); add it to "+
					"replayNotSwept with the test that holds it to one send", c.Name(), why)
			}
			continue
		}
		driven++
		_, keyed := c.Flag("idempotency-key")
		t.Run(c.Name(), func(t *testing.T) {
			for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
				for _, withKey := range []bool{false, keyed} {
					applied, err := driveFaulted(t, c, kind, withKey)
					requireNoRepeatedWrite(t, kind, withKey, applied, err)
				}
			}
		})
	}
	if driven == 0 {
		t.Fatal("no mutating command was driven, so this proves nothing about retries")
	}
}

// TestTheReplaySweepCanFail is the negative control: a command that sends its
// POST again after a 503 has to be caught.
func TestTheReplaySweepCanFail(t *testing.T) {
	resend := &registry.Command{
		Path:      []string{"probe", "resend"},
		Summary:   "A create that retries by hand",
		Mutating:  true,
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.resend", Version: 1}},
		Run: func(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
			conn, _, err := inv.Jira.Connect(ctx)
			if err != nil {
				return nil, err
			}
			req := transport.Request{
				Method: transport.MethodPost, Path: "/rest/api/2/issue",
				Body: []byte(`{"fields":{"summary":"probe"}}`),
			}
			for range 2 {
				resp, err := conn.Do(ctx, req)
				if err == nil && transport.Err(resp) == nil {
					break
				}
			}
			return render.Record("probe.resend", 1, render.El("probe")), nil
		},
	}
	applied, err := driveFaulted(t, resend, site.DataCenter, false)
	caught := false
	for request, n := range applied {
		if n > 1 && strings.HasPrefix(request, http.MethodPost+" ") {
			caught = true
		}
	}
	if !caught {
		t.Errorf("a command that sent its POST twice was not seen doing it, so "+
			"TestAWriteJiraMayHaveAppliedIsNotSentTwice cannot fail: %v, %v", applied, err)
	}
}

// replayApplyCoveredBy names, for every command that declares --apply, the
// test that applies a row Jira answered 503 after doing.
var replayApplyCoveredBy = map[string]string{
	"issue.edit":     "TestAnApplyRowJiraMayHaveAppliedIsNotSentTwice",
	"issue.move":     "TestAnApplyRowJiraMayHaveAppliedIsNotSentTwice",
	"issue.assign":   "TestAnApplyRowJiraMayHaveAppliedIsNotSentTwice",
	"issue.sequence": "TestAStepJiraMayHaveAppliedIsNotSentTwice",
}

// replayNotSwept names every mutating command this harness cannot drive, with
// the test that holds it to one send instead.
var replayNotSwept = map[string]string{
	"issue.attachment.upload": "TestPostIsNotReplayedAfterAnUpstreamError, " +
		"which holds every POST, and an upload is one",
}

// requireNoRepeatedWrite fails on a non-idempotent request applied twice, and
// on a command that reported success for a write whose answer was a 503 it did
// not resend.
func requireNoRepeatedWrite(
	t *testing.T, kind site.Kind, keyed bool, applied map[string]int, err error,
) {
	t.Helper()
	with := "no key"
	if keyed {
		with = "--idempotency-key"
	}
	requests := make([]string, 0, len(applied))
	for request := range applied {
		requests = append(requests, request)
	}
	sort.Strings(requests)
	for _, request := range requests {
		method, _, _ := strings.Cut(request, " ")
		if applied[request] > 1 && !idempotentByMethod(method) {
			t.Errorf("on %s with %s, Jira applied %s %d times; the first answer "+
				"was a 503 after it had done the work", kind, with, request, applied[request])
		}
	}
	if err == nil && oneWriteSentOnce(applied) {
		t.Errorf("on %s with %s, the only write was answered 503 and not resent, "+
			"and the command reported success", kind, with)
	}
}

func idempotentByMethod(method string) bool {
	switch method {
	case http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// oneWriteSentOnce reports a command whose only write was a non-idempotent
// request sent exactly once, which the fake answered 503.
func oneWriteSentOnce(applied map[string]int) bool {
	if len(applied) != 1 {
		return false
	}
	for request, n := range applied {
		method, _, _ := strings.Cut(request, " ")
		return n == 1 && !idempotentByMethod(method)
	}
	return false
}

// driveFaulted runs one command for real, not as a preview, through a
// transport at the default retry budget, against faultingTransport.
func driveFaulted(
	t *testing.T, c *registry.Command, kind site.Kind, keyed bool,
) (map[string]int, error) {
	t.Helper()
	ft := &faultingTransport{kind: kind, applied: map[string]int{}}
	flags := registry.NewFlags()
	for _, f := range c.AllFlags() {
		if f.Name == "yes" {
			flags.SetBool("yes", true)
			continue
		}
		if f.Required {
			if v, _, ok := probePair(f); ok {
				setProbe(flags, f, v)
			}
		}
	}
	for name, value := range companionFlags[c.Name()] {
		flags.SetString(name, value)
	}
	if keyed {
		flags.SetString("idempotency-key", "sweep-1")
	}
	var args []string
	for _, a := range c.Args {
		if v := argProbe(c, a); v != "" {
			args = append(args, v)
		}
	}
	inv := &registry.Invocation{
		Jira: faultSession{
			sweepSession: sweepSession{kind: kind},
			rt:           ft,
			ledger:       &idem.Ledger{Path: filepath.Join(t.TempDir(), "idem.toml")},
		},
		Args:     args,
		Flags:    flags,
		Limit:    sweepLimit(t, c, flags),
		Format:   render.XML,
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}
	var out strings.Builder
	err := runFor(t.Context(), c, inv, &out)
	return ft.applied, err
}

// faultingTransport is a Jira behind a proxy that times out: it applies every
// write it is sent and answers the first one with 503.
type faultingTransport struct {
	kind    site.Kind
	failed  bool
	applied map[string]int
}

func (f *faultingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = strings.TrimSpace(string(b))
	}
	respond := func(status int, payload string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(payload)),
			Request:    r,
		}, nil
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return respond(http.StatusOK, sweepResponse(r.URL.Path, f.kind))
	}
	f.applied[r.Method+" "+r.URL.Path+" "+body]++
	if !f.failed {
		f.failed = true
		return respond(http.StatusServiceUnavailable, `{"errorMessages":["upstream timed out"]}`)
	}
	return respond(http.StatusOK, sweepResponse(r.URL.Path, f.kind))
}

// faultSession is sweepSession with a transport that retries, the way the CLI
// builds one, and a ledger, so a keyed write takes its real path.
type faultSession struct {
	sweepSession
	rt     http.RoundTripper
	ledger *idem.Ledger
}

func (s faultSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	conn, err := transport.New(transport.Options{
		BaseURL:    sweepBase,
		HTTPClient: &http.Client{Transport: s.rt},
		Retries:    3,
		Clock:      instantClock{},
		Jitter:     func() float64 { return 0 },
	})
	version := "10.4.0"
	if s.kind == site.Cloud {
		version = "1001.0.0"
	}
	return conn, site.Info{Kind: s.kind, Version: version, BaseURL: sweepBase}, err
}

func (s faultSession) Metadata(ctx context.Context) (*site.Metadata, error) {
	conn, info, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &site.Metadata{Client: conn, Info: info}, nil
}

func (s faultSession) Idempotency() *idem.Ledger { return s.ledger }

// instantClock lets a retry happen without waiting for its backoff.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }

func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
