package sprint_test

// The fixtures and the fake session live here rather than in sprint_test.go,
// because `sprint close` is behind two build tags and its test needs the same
// scaffolding. A helpers file with no constraint of its own compiles into every
// build, so neither test carries a copy.

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/site"
	"github.com/kmoneil/jira-cli/internal/transport"
)

var deployments = []site.Kind{site.Cloud, site.DataCenter}

// runList runs `sprint list` against the named cassette for one deployment.
func runList(
	t *testing.T, kind site.Kind, fixture string, states []string, limit registry.Limit,
) (string, registry.StreamResult, *transport.Replayer) {
	t.Helper()

	cmd, ok := registry.Lookup("sprint.list")
	if !ok {
		t.Fatal("sprint list is not registered")
	}

	conn, replayer := replayConn(t, fixture+"."+string(kind)+".json")
	flags := registry.NewFlags()
	for _, s := range states {
		flags.SetString("state", s)
	}
	inv := &registry.Invocation{
		Jira:  &stubSession{conn: conn, kind: kind, board: "99"},
		Flags: flags, Limit: limit,
		Stderr: io.Discard, Progress: registry.NoProgress,
	}

	var buf strings.Builder
	stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
		Kind: cmd.Kind(), Version: cmd.KindVersion(),
		Name: cmd.CollectionName, Columns: cmd.Columns,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	result, err := cmd.Stream(t.Context(), inv, stream)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.String(), result, replayer
}

// replayConn builds a transport backed by a recorded conversation.
func replayConn(t *testing.T, fixture string) (*transport.Client, *transport.Replayer) {
	t.Helper()
	cassette, err := transport.LoadCassette(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	replayer := transport.NewReplayer(cassette)
	conn, err := transport.New(transport.Options{
		BaseURL: "https://recorded.invalid", HTTPClient: replayer.Client(), Retries: -1,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return conn, replayer
}

// stubSession is a registry.Session over a recorded conversation, so a command
// runs with no auth, no config, and no network.
type stubSession struct {
	conn  *transport.Client
	kind  site.Kind
	board string
}

func (s *stubSession) Connect(context.Context) (*transport.Client, site.Info, error) {
	return s.conn, site.Info{Kind: s.kind}, nil
}

func (s *stubSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Info: site.Info{Kind: s.kind}}, nil
}

func (s *stubSession) Idempotency() *idem.Ledger  { return nil }
func (s *stubSession) Project() string            { return "" }
func (s *stubSession) Board() string              { return s.board }
func (s *stubSession) CheckWritable(string) error { return nil }

func (s *stubSession) RequireProject() (string, error) {
	return "", errs.Usage("NO_PROJECT", "this command needs a project and none is set")
}

func (s *stubSession) RequireBoard() (string, error) {
	if s.board == "" {
		return "", errs.Usage("NO_BOARD", "this command needs a board and none is set")
	}
	return s.board, nil
}
