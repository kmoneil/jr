package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kmoneil/jr/internal/errs"
)

// Deployment names which kind of Jira a fixture was recorded against. Behavior
// differs enough between them — API versions, ADF versus wiki markup, cursor
// versus offset pagination — that a fixture recorded on one proves nothing
// about the other.
type Deployment string

// The deployments this project supports.
const (
	Cloud      Deployment = "cloud"
	DataCenter Deployment = "datacenter"
)

// Deployments returns every deployment a resource must ship fixtures for.
func Deployments() []Deployment { return []Deployment{Cloud, DataCenter} }

// Interaction is one recorded request/response pair.
type Interaction struct {
	Request  RecordedRequest  `json:"request"`
	Response RecordedResponse `json:"response"`
}

// RecordedRequest is the part of a request a fixture matches on.
type RecordedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query,omitempty"`
	Body   string `json:"body,omitempty"`
}

// RecordedResponse is what the fixture replays.
type RecordedResponse struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   string              `json:"body,omitempty"`
}

// Cassette is a recorded conversation with one deployment.
type Cassette struct {
	Deployment Deployment `json:"deployment"`
	// Source says whether this conversation happened or was imagined. It is
	// the difference between a fixture that proves a request was right and one
	// that only proves it has not changed, and for a long time this repository
	// had nothing but the second kind while reading like it had the first.
	Source       Source        `json:"source,omitempty"`
	Interactions []Interaction `json:"interactions"`
}

// Source distinguishes evidence from assumption.
type Source string

const (
	// Recorded means a real server produced this, scrubbed on the way out. It
	// is evidence: the path, the parameters, and the response fields are what
	// that deployment actually does.
	Recorded Source = "recorded"

	// Constructed means somebody wrote it. Still useful — it exercises logic no
	// sandbox will produce on demand, an empty page, a truncation, a malformed
	// body — but it asserts nothing about the API, and a request shape it
	// encodes is a belief rather than a finding.
	Constructed Source = "constructed"
)

// Evidence reports whether a cassette records something that happened.
//
// Absent means constructed. Every fixture predates the recorder, so silence has
// to mean the weaker claim: a file that says nothing about where it came from
// is not evidence of anything.
func (c *Cassette) Evidence() bool { return c != nil && c.Source == Recorded }

// LoadCassette reads a fixture from disk.
func LoadCassette(path string) (*Cassette, error) {
	data, err := os.ReadFile(path) //nolint:gosec // fixture paths come from the test tree.
	if err != nil {
		return nil, errs.Runtime("FIXTURE_MISSING", "cannot read fixture %s", path).Wrap(err)
	}
	var c Cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, errs.Runtime("FIXTURE_INVALID", "cannot parse fixture %s", path).Wrap(err)
	}
	return &c, nil
}

// Save writes a fixture, creating the directory if needed.
func (c *Cassette) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Runtime("FIXTURE_WRITE", "cannot create %s", filepath.Dir(path)).Wrap(err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return errs.Runtime("FIXTURE_WRITE", "cannot encode fixture").Wrap(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // fixtures are not secret.
		return errs.Runtime("FIXTURE_WRITE", "cannot write %s", path).Wrap(err)
	}
	return nil
}

// Replayer is an http.RoundTripper that answers from a cassette and never
// touches the network.
//
// A replayed test that silently fell through to a live call would be the worst
// of both worlds: green in CI where there are no credentials, and quietly
// exercising nothing. An unmatched request is an error, always.
type Replayer struct {
	mu        sync.Mutex
	cassette  *Cassette
	played    []bool
	unmatched []string
}

// NewReplayer returns a RoundTripper serving c.
func NewReplayer(c *Cassette) *Replayer {
	return &Replayer{cassette: c, played: make([]bool, len(c.Interactions))}
}

// RoundTrip implements http.RoundTripper.
func (r *Replayer) RoundTrip(req *http.Request) (*http.Response, error) {
	key, err := matchKey(req)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.cassette.Interactions {
		if r.played[i] {
			continue
		}
		if interactionKey(r.cassette.Interactions[i].Request) != key {
			continue
		}
		r.played[i] = true
		return buildResponse(req, r.cassette.Interactions[i].Response), nil
	}

	// Fall back to a played interaction, so a retry test can replay the same
	// call. Recording order still decides which response comes first.
	for i := range r.cassette.Interactions {
		if interactionKey(r.cassette.Interactions[i].Request) == key {
			return buildResponse(req, r.cassette.Interactions[i].Response), nil
		}
	}

	r.unmatched = append(r.unmatched, key)
	return nil, errs.Runtime("FIXTURE_MISS",
		"no recorded interaction for %s", key).
		WithRemedy("re-record the fixture, or add the interaction by hand")
}

// Unplayed returns the interactions the test never triggered. A fixture that
// carries responses nothing asked for is usually a test that stopped covering
// what it claims to.
func (r *Replayer) Unplayed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string
	for i, played := range r.played {
		if !played {
			out = append(out, interactionKey(r.cassette.Interactions[i].Request))
		}
	}
	return out
}

// Unmatched returns the requests the cassette could not answer.
func (r *Replayer) Unmatched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.unmatched...)
}

// Client returns an http.Client that replays this cassette.
func (r *Replayer) Client() *http.Client { return &http.Client{Transport: r} }

// Recorder wraps a RoundTripper and captures what passes through it, so a
// fixture can be regenerated against a real instance.
//
// Credentials are redacted as the interaction is recorded, not as it is
// written, so a token cannot reach the file even if recording is interrupted.
type Recorder struct {
	mu    sync.Mutex
	next  http.RoundTripper
	tape  *Cassette
	limit int
}

// NewRecorder returns a Recorder writing into a cassette for the given
// deployment.
func NewRecorder(next http.RoundTripper, deployment Deployment) *Recorder {
	if next == nil {
		next = http.DefaultTransport
	}
	return &Recorder{
		next:  next,
		tape:  &Cassette{Deployment: deployment},
		limit: maxResponseBytes,
	}
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// One byte past the limit, and refused if it arrives. A LimitReader at the
	// limit returns exactly the limit with no error, so a body beyond it would
	// be written into the cassette silently short and replayed that way by
	// every test that ever loads it. A fixture that quietly lost its tail is
	// not evidence, and the recorder is the last place that can still say so.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(r.limit)+1))
	if err != nil {
		return nil, err
	}
	if len(respBody) > r.limit {
		return nil, errs.Runtime("RECORDING_TOO_LARGE",
			"%s %s answered with more than the %d bytes a cassette will hold",
			req.Method, req.URL.Path, r.limit).
			WithRemedy("record a narrower request: fewer fields, a smaller page")
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tape.Interactions = append(r.tape.Interactions, Interaction{
		Request: RecordedRequest{
			Method: req.Method,
			Path:   req.URL.Path,
			Query:  req.URL.RawQuery,
			Body:   string(reqBody),
		},
		Response: RecordedResponse{
			Status: resp.StatusCode,
			// Redacted and then trimmed to what something reads. A header that
			// is never captured cannot leak, which is the only defence that
			// works against a header shape nobody thought to look for.
			Header: recordableHeader(resp.Header),
			Body:   string(respBody),
		},
	})
	return resp, nil
}

// Cassette returns what has been recorded so far.
func (r *Recorder) Cassette() *Cassette {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tape
}

// matchKey builds the identity of an outgoing request. It deliberately ignores
// headers: a fixture that matched on User-Agent or X-Request-Id would break on
// every release and on every run.
func matchKey(req *http.Request) (string, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return "", errs.Runtime("FIXTURE_READ", "cannot read the request body").Wrap(err)
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return interactionKey(RecordedRequest{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  canonicalQuery(req.URL.RawQuery),
		Body:   string(body),
	}), nil
}

func interactionKey(r RecordedRequest) string {
	key := fmt.Sprintf("%s %s", strings.ToUpper(r.Method), r.Path)
	if q := canonicalQuery(r.Query); q != "" {
		key += "?" + q
	}
	if r.Body != "" {
		key += " " + canonicalBody(r.Body)
	}
	return key
}

// canonicalQuery sorts parameters so a fixture does not depend on the order a
// caller happened to build them in.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	v, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	return v.Encode()
}

// canonicalBody re-encodes a JSON body with sorted keys, so a fixture survives
// a change in field order that changes nothing about the request.
func canonicalBody(body string) string {
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return strings.Join(strings.Fields(body), " ")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}

func buildResponse(req *http.Request, rec RecordedResponse) *http.Response {
	header := make(http.Header, len(rec.Header))
	for name, values := range rec.Header {
		header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode:    rec.Status,
		Status:        http.StatusText(rec.Status),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(rec.Body)),
		ContentLength: int64(len(rec.Body)),
		Request:       req,
	}
}
