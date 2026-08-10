//go:build mcp

// Package mcp serves the command registry over the Model Context Protocol.
//
// Every command becomes a tool whose schema is derived from the same metadata
// that builds the command tree and `jr schema`, so adding a command adds a tool
// for free and the two cannot drift. The tool list of a build is the command
// list of that build: a reader binary advertises no mutating tools because it
// contains none.
//
// With one exception, and it is a command rather than a category: a command
// that declares OwnsStdout writes this server's own byte stream, so it cannot
// be a tool of it. `mcp serve` is the only one. See servableAsTool.
//
// The protocol is spoken directly rather than through an SDK. What is needed is
// JSON-RPC 2.0 over stdio with three methods, which is small and precisely
// specified, and the profiles this tag ships in — agent and reader — are the
// ones meant to carry the least. The wire format is asserted by test rather
// than trusted.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// ProtocolVersion is the revision this server implements. A client asking for a
// different one is answered with this, which is what the specification says to
// do when the requested revision is not supported.
const ProtocolVersion = "2025-06-18"

// Options configures a server.
type Options struct {
	// Registry supplies the tools. It is the same registry the command tree is
	// built from, so the two cannot disagree about what exists.
	Registry *registry.Registry
	// In and Out carry the JSON-RPC stream. For a stdio server these are the
	// process's stdin and stdout.
	In  io.Reader
	Out io.Writer
	// Log receives protocol-level complaints. It must never be Out: a stray
	// byte there is a malformed frame, and the client sees a broken session
	// rather than a message.
	Log io.Writer

	Name    string
	Version string

	// Session builds the Jira connection a tool call needs. It is called per
	// call rather than once, so a credential change between calls is picked up
	// and a failure to connect fails one tool rather than the server.
	Session func() (registry.Session, error)
}

// request is an incoming JSON-RPC message. ID is absent for a notification,
// which is why it is a RawMessage: distinguishing "no id" from "id 0" decides
// whether a reply is owed.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Server answers MCP requests from the registry.
type Server struct {
	opt Options
	mu  sync.Mutex
	enc *json.Encoder
}

// Serve runs the stdio loop until the input ends or the context is cancelled.
func Serve(ctx context.Context, opt Options) error {
	if opt.Registry == nil {
		return errs.Runtime("NO_REGISTRY", "the MCP server was given no commands")
	}
	s := &Server{opt: opt, enc: json.NewEncoder(opt.Out)}

	scanner := bufio.NewScanner(opt.In)
	// A tool call can carry a sizable argument, so the default 64KiB line cap
	// is raised. Beyond this a frame is refused rather than silently truncated.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		// A cancelled context ends the session cleanly: the client asked for
		// nothing more, so there is nothing to report as a failure.
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancellation is a clean stop, not an error.
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleLine(ctx, []byte(line))
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return errs.Runtime("MCP_READ", "cannot read the MCP stream").Wrap(err)
	}
	return nil
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.fail(nil, codeParse, "cannot parse the request", nil)
		return
	}
	if req.JSONRPC != "2.0" {
		s.fail(req.ID, codeInvalidRequest, "jsonrpc must be \"2.0\"", nil)
		return
	}

	// A notification carries no id and is owed no reply. Answering one is a
	// protocol violation, and some clients treat it as a fatal desync.
	notification := len(req.ID) == 0

	result, rpcErr := s.dispatch(ctx, req)
	if notification {
		return
	}
	if rpcErr != nil {
		s.fail(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		return
	}
	s.send(response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// dispatch routes one request, and survives a command that panics.
//
// The same command code runs from two callers with very different stakes. The
// CLI runs one and exits, so a panic there loses an invocation that was ending
// anyway and the traceback is the diagnosis. This server holds a session: a
// panic in one tool call would end every later one, and the peer would not even
// get an error — it would see the stdio stream close underneath it, which is
// indistinguishable from a crash it caused and gives an agent nothing to report.
//
// net/http recovers per connection for this reason. Nothing else in this tree
// recovers from anything, deliberately; this is the one place where the process
// outliving the failure is the whole point.
//
// It cannot cover a goroutine a command started. `multipartSource` in
// internal/resource/issue/attachment_write.go spawns one per upload attempt, and
// a panic there ends the process whatever happens here — that is Go's rule, not
// a gap in this function. Said out loud because "the handler recovers" reads as
// covering more than it does.
func (s *Server) dispatch(ctx context.Context, req request) (result any, rpcErr *rpcError) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		// The reply says the call failed and nothing else. A panic value can
		// carry whatever was in scope — a path, a URL, a field from a response
		// — and the peer is owed a verdict, not this process's internals. The
		// detail goes to the log, which is never Out: a traceback on stdout is
		// a malformed frame, and that has shipped here once already.
		if s.opt.Log != nil {
			_, _ = fmt.Fprintf(s.opt.Log, "[mcp] %s panicked: %v\n%s\n",
				req.Method, recovered, debug.Stack())
		}
		result = nil
		rpcErr = &rpcError{
			Code:    codeInternal,
			Message: fmt.Sprintf("%s failed inside the server", req.Method),
		}
	}()

	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": Tools(s.opt.Registry)}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	default:
		return nil, &rpcError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		}
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	// Echo the client's revision when it is one this server speaks, and
	// otherwise state the one it does. Silently agreeing to a revision this
	// server does not implement is how a session half-works.
	version := ProtocolVersion
	var got struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &got); err == nil && got.ProtocolVersion == ProtocolVersion {
		version = got.ProtocolVersion
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			// Tools are fixed for the life of the process, because they come
			// from a registry decided at compile time. So no listChanged.
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.opt.Name,
			"version": s.opt.Version,
		},
	}
}

// callParams is a tools/call request.
type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var call callParams
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "cannot parse the tool call"}
	}

	cmd, ok := s.opt.Registry.Lookup(toolToCommand(call.Name))
	if !ok {
		return nil, &rpcError{
			Code:    codeInvalidParams,
			Message: fmt.Sprintf("no tool named %q in this build", call.Name),
		}
	}
	// Checked here as well as in Tools, because a peer can call a name that was
	// never advertised. Leaving it out of the list is a description of this
	// server; refusing it here is the control. It says why rather than
	// pretending the command does not exist, because `mcp serve` plainly does
	// and a caller reading "no such tool" about a command in `jr --help` learns
	// nothing.
	if !servableAsTool(cmd) {
		return nil, &rpcError{
			Code: codeInvalidParams,
			Message: fmt.Sprintf(
				"%q cannot be called as a tool: it writes the stream this server "+
					"speaks on", call.Name,
			),
		}
	}

	text, err := s.run(ctx, cmd, call.Arguments)
	if err != nil {
		// A failed command is a tool error, not a protocol error: the client
		// asked a well-formed question and got a well-formed refusal. The
		// structured error travels intact — code, remedy, and retryable are
		// exactly what an agent needs to decide whether to try again.
		return toolResult(renderError(err), true), nil
	}
	return toolResult(text, false), nil
}

// run executes a command and renders its output.
func (s *Server) run(
	ctx context.Context, cmd *registry.Command, args map[string]any,
) (string, error) {
	inv, format, err := s.invocation(ctx, cmd, args)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	if cmd.Streams() {
		if err := streamInto(ctx, &out, cmd, inv, format); err != nil {
			return "", err
		}
		return out.String(), nil
	}

	doc, err := cmd.Run(ctx, inv)
	if err != nil {
		return "", err
	}
	if err := render.Write(&out, doc, format); err != nil {
		return "", err
	}
	return out.String(), nil
}

// streamInto runs a collection command and writes its rows, plus the truncation
// warning if it was cut short.
func streamInto(ctx context.Context, out *strings.Builder, cmd *registry.Command,
	inv *registry.Invocation, format render.Format,
) error {
	stream, err := render.NewStream(out, format, render.StreamSpec{
		Kind:    cmd.Kind(),
		Version: cmd.KindVersion(),
		Name:    cmd.CollectionName,
		Columns: columnsFor(cmd, inv),
	})
	if err != nil {
		return err
	}
	result, err := cmd.Stream(ctx, inv, stream)
	if err != nil {
		return err
	}
	if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
		return err
	}
	if result.Complete {
		return nil
	}

	// There is no exit code here to carry the signal, so the warning goes in the
	// content. A truncated result that looked complete is the one failure this
	// whole format exists to prevent.
	var warning strings.Builder
	if err := render.WriteStreamTruncation(&warning, cmd.Kind(),
		stream.Count(), result.NextPageToken, format); err != nil {
		return err
	}
	out.WriteString("\n")
	out.WriteString(warning.String())
	return nil
}

func columnsFor(cmd *registry.Command, inv *registry.Invocation) []render.Column {
	if cmd.ColumnsFor != nil {
		return cmd.ColumnsFor(inv)
	}
	return cmd.Columns
}

// renderError turns a failure into the same structured error the CLI writes to
// stderr, so an agent sees one error contract however it called.
func renderError(err error) string {
	var out strings.Builder
	if werr := render.WriteError(&out, errs.Coerce(err), render.XML); werr != nil {
		return err.Error()
	}
	return out.String()
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) send(resp response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(resp); err != nil && s.opt.Log != nil {
		_, _ = fmt.Fprintf(s.opt.Log, "[mcp] cannot write a response: %v\n", err)
	}
}

func (s *Server) fail(id json.RawMessage, code int, message string, data any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.send(response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}
