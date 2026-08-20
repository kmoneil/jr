//go:build mcp

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/mcp"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// testRegistry holds one record command and one streaming command, which is
// enough to cover both output paths.
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()

	r.Register(&registry.Command{
		Path:    []string{"thing", "get"},
		Summary: "Fetch one thing",
		Example: "jr thing get K-1",
		Args: []registry.Arg{{
			Name: "key", Usage: "thing key", Required: true,
		}},
		Flags: []registry.Flag{
			{Name: "verbose", Type: registry.TypeBool, Usage: "say more"},
			{
				Name: "order", Type: registry.TypeEnum,
				Enum: []string{"asc", "desc"}, Usage: "direction",
			},
			{Name: "depth", Type: registry.TypeInt, Usage: "how deep"},
			{
				Name: "tag", Type: registry.TypeString, Repeatable: true,
				Usage: "tag to match",
			},
		},
		Outputs: []registry.Output{{Kind: "thing.get", Version: 1}},
		Run: func(_ context.Context, inv *registry.Invocation) (*render.Doc, error) {
			n := render.El("thing").
				Attr("key", inv.Args[0]).
				Attr("verbose", boolText(inv.Flags.Bool("verbose"))).
				Attr("order", inv.Flags.String("order")).
				Leaf("tags", strings.Join(inv.Flags.StringSlice("tag"), ","))
			return render.Record("thing.get", 1, n), nil
		},
	})

	r.Register(&registry.Command{
		Path:           []string{"thing", "list"},
		Summary:        "List things",
		Example:        "jr thing list",
		Paginated:      true,
		CollectionName: "things",
		Columns:        []render.Column{{Header: "key", Path: "@key"}},
		Outputs:        []registry.Output{{Kind: "thing.list", Version: 1}},
		ExitCodes:      []exitcode.Code{exitcode.Partial},
		Stream: func(
			_ context.Context, inv *registry.Invocation, out *render.Stream,
		) (registry.StreamResult, error) {
			rows := 3
			complete := true
			if !inv.Limit.All && inv.Limit.N < rows {
				rows, complete = inv.Limit.N, false
			}
			for i := range rows {
				if err := out.Write(render.El("thing").
					Attr("key", "K-"+string(rune('A'+i)))); err != nil {
					return registry.StreamResult{}, err
				}
			}
			if complete {
				return registry.StreamResult{Complete: true}, nil
			}
			return registry.StreamResult{NextPageToken: "MORE"}, nil
		},
	})

	r.Register(&registry.Command{
		Path:      []string{"thing", "fail"},
		Summary:   "Always fail",
		Example:   "jr thing fail",
		Outputs:   []registry.Output{{Kind: "thing.fail", Version: 1}},
		ExitCodes: []exitcode.Code{exitcode.NotFound},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return nil, errs.NotFound("NO_THING", "no such thing").
				WithRemedy("try another key")
		},
	})
	return r
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// session drives a server over an in-memory pipe and returns the replies.
func session(t *testing.T, reg *registry.Registry, requests ...string) []map[string]any {
	t.Helper()

	var out strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: reg,
		In:       strings.NewReader(strings.Join(requests, "\n") + "\n"),
		Out:      &out,
		Log:      io.Discard,
		Name:     "jr",
		Version:  "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	var replies []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, reply)
	}
	return replies
}

// TestInitializeHandshake pins the wire format, which is the part a hand-rolled
// server has to get exactly right for a client to connect at all.
func TestInitializeHandshake(t *testing.T) {
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+
			`{"protocolVersion":"`+mcp.ProtocolVersion+`","capabilities":{}}}`)

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	reply := replies[0]
	if reply["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", reply["jsonrpc"])
	}
	if reply["id"] != float64(1) {
		t.Errorf("id = %v, want the request's", reply["id"])
	}

	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", reply)
	}
	if result["protocolVersion"] != mcp.ProtocolVersion {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "jr" || info["version"] != "0.0.0-test" {
		t.Errorf("serverInfo = %v", info)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, has := caps["tools"]; !has {
		t.Errorf("the server does not advertise tools: %v", caps)
	}
}

// TestNotificationsAreNotAnswered is a protocol rule worth a test: replying to
// a notification desynchronizes some clients fatally.
func TestNotificationsAreNotAnswered(t *testing.T) {
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`)

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want only the ping's: %v", len(replies), replies)
	}
	if replies[0]["id"] != float64(7) {
		t.Errorf("the wrong request was answered: %v", replies[0])
	}
}

// TestToolsListMirrorsTheRegistry is the property the whole design rests on:
// the tools are the commands, so they cannot drift.
func TestToolsListMirrorsTheRegistry(t *testing.T) {
	reg := testRegistry(t)
	replies := session(t, reg, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != len(reg.All()) {
		t.Fatalf("got %d tools for %d commands", len(tools), len(reg.All()))
	}

	byName := map[string]map[string]any{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		byName[name] = tool
	}
	// A dot is not an identifier, and several clients reject it.
	if _, ok := byName["thing_get"]; !ok {
		t.Fatalf("thing.get was not exposed as thing_get: %v", byName)
	}

	schema, _ := byName["thing_get"]["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)

	// A positional argument becomes a named property, required.
	if _, ok := props["key"]; !ok {
		t.Errorf("the positional argument is missing: %v", props)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "key" {
		t.Errorf("required = %v, want [key]", required)
	}

	// Flag types survive into the schema.
	for name, want := range map[string]string{
		"verbose": "boolean", "depth": "integer", "tag": "array",
	} {
		prop, _ := props[name].(map[string]any)
		if prop["type"] != want {
			t.Errorf("flag %q is typed %v, want %v", name, prop["type"], want)
		}
	}
	order, _ := props["order"].(map[string]any)
	if _, ok := order["enum"]; !ok {
		t.Errorf("an enum flag carries no values: %v", order)
	}
	// An unadvertised argument must not be quietly accepted.
	if schema["additionalProperties"] != false {
		t.Errorf("the schema permits unknown arguments: %v", schema)
	}
}

// TestToolDescriptionsSayWhatTheyDo is what a model reads to choose a tool.
func TestToolDescriptionsSayWhatTheyDo(t *testing.T) {
	tools := mcp.Tools(testRegistry(t))
	byName := map[string]mcp.Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	get := byName["thing_get"].Description
	if !strings.Contains(get, "thing.get") {
		t.Errorf("the description does not name the output kind: %q", get)
	}
	if !strings.Contains(get, "only reads") {
		t.Errorf("a read-only tool does not say so: %q", get)
	}

	list := byName["thing_list"].Description
	if !strings.Contains(list, "never silently truncated") {
		t.Errorf("a paginated tool does not warn about truncation: %q", list)
	}
}

func callTool(t *testing.T, reg *registry.Registry, name string, args any) map[string]any {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	replies := session(t, reg,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":`+string(params)+`}`)
	if len(replies) != 1 {
		t.Fatalf("got %d replies", len(replies))
	}
	if rpcErr, has := replies[0]["error"]; has {
		t.Fatalf("protocol error where a tool result was expected: %v", rpcErr)
	}
	result, _ := replies[0]["result"].(map[string]any)
	return result
}

func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content: %v", result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func TestCallToolRunsTheCommand(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_get", map[string]any{
		"key":     "K-1",
		"verbose": true,
		"order":   "desc",
		"tag":     []any{"a", "b"},
	})
	if result["isError"] != false {
		t.Errorf("a successful call reported an error: %v", result)
	}

	text := toolText(t, result)
	// A record defaults to XML, the same as on the command line.
	if !strings.HasPrefix(text, "<?xml") {
		t.Errorf("a record did not default to XML:\n%s", text)
	}
	for _, want := range []string{`key="K-1"`, `verbose="true"`, `order="desc"`, "a,b"} {
		if !strings.Contains(text, want) {
			t.Errorf("the arguments did not reach the command (%q):\n%s", want, text)
		}
	}
}

func TestCallToolFormatArgument(t *testing.T) {
	reg := testRegistry(t)
	for format, marker := range map[string]string{
		"json": `"kind": "thing.get"`,
		"yaml": "kind: thing.get",
		"tsv":  "field\tvalue",
	} {
		result := callTool(t, reg, "thing_get", map[string]any{
			"key": "K-1", "format": format,
		})
		if text := toolText(t, result); !strings.Contains(text, marker) {
			t.Errorf("format %q did not take effect:\n%s", format, text)
		}
	}
}

// TestStreamedToolDefaultsToTSV keeps the per-content rule the CLI uses.
func TestStreamedToolDefaultsToTSV(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_list", map[string]any{})
	text := toolText(t, result)
	if !strings.HasPrefix(text, "key\n") {
		t.Errorf("a collection did not default to TSV:\n%s", text)
	}
	if strings.Count(strings.TrimSpace(text), "\n") != 3 {
		t.Errorf("got the wrong number of rows:\n%s", text)
	}
}

// TestTruncationSurvivesIntoTheReply is the contract's most important property
// crossing into a protocol with no exit codes. The warning has to be in the
// content, or a truncated result looks complete.
func TestTruncationSurvivesIntoTheReply(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_list", map[string]any{"limit": "2"})
	text := toolText(t, result)

	if !strings.Contains(text, "RESULT_TRUNCATED") {
		t.Errorf("a truncated result carries no warning:\n%s", text)
	}
	if !strings.Contains(text, "MORE") {
		t.Errorf("the resume token was dropped:\n%s", text)
	}
	// The rows are still there: the caller gets the data and the caveat.
	if !strings.Contains(text, "K-A") {
		t.Errorf("the fetched rows were discarded:\n%s", text)
	}
}

// TestFailedToolReturnsTheStructuredError is what makes one error contract hold
// across both surfaces: an agent gets the code, the remedy, and whether
// retrying can help, exactly as on stderr.
func TestFailedToolReturnsTheStructuredError(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_fail", map[string]any{})
	if result["isError"] != true {
		t.Errorf("a failed tool did not report isError: %v", result)
	}

	text := toolText(t, result)
	for _, want := range []string{
		"<code>NO_THING</code>",
		"<retryable>false</retryable>",
		"try another key",
		"<exit-name>NOT_FOUND</exit-name>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the structured error lost %q:\n%s", want, text)
		}
	}
}

// TestUnknownArgumentIsRefused matches the CLI: an option that is silently
// ignored is a request that did something other than what was asked.
func TestUnknownArgumentIsRefused(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_get", map[string]any{
		"key": "K-1", "nope": "x",
	})
	if result["isError"] != true {
		t.Errorf("an unknown argument was accepted: %v", result)
	}
	if text := toolText(t, result); !strings.Contains(text, "UNKNOWN_ARGUMENT") {
		t.Errorf("the error does not name the problem:\n%s", text)
	}
}

func TestMissingRequiredArgumentIsRefused(t *testing.T) {
	result := callTool(t, testRegistry(t), "thing_get", map[string]any{})
	if result["isError"] != true {
		t.Errorf("a missing required argument was accepted: %v", result)
	}
	if text := toolText(t, result); !strings.Contains(text, "MISSING_ARGUMENT") {
		t.Errorf("the error does not name the problem:\n%s", text)
	}
}

func TestUnknownToolIsAProtocolError(t *testing.T) {
	replies := session(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+
			`{"name":"nope","arguments":{}}}`)
	rpcErr, has := replies[0]["error"].(map[string]any)
	if !has {
		t.Fatalf("an unknown tool was not a protocol error: %v", replies[0])
	}
	message, _ := rpcErr["message"].(string)
	if !strings.Contains(message, "nope") {
		t.Errorf("the error does not name the tool: %v", rpcErr)
	}
}

func TestMalformedInput(t *testing.T) {
	cases := map[string]struct {
		line string
		code float64
	}{
		"not json":      {`{not json`, -32700},
		"wrong version": {`{"jsonrpc":"1.0","id":1,"method":"ping"}`, -32600},
		"unknown method": {
			`{"jsonrpc":"2.0","id":1,"method":"nope/nope"}`, -32601,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			replies := session(t, testRegistry(t), tc.line)
			if len(replies) != 1 {
				t.Fatalf("got %d replies", len(replies))
			}
			rpcErr, has := replies[0]["error"].(map[string]any)
			if !has {
				t.Fatalf("no error reported: %v", replies[0])
			}
			if rpcErr["code"] != tc.code {
				t.Errorf("code = %v, want %v", rpcErr["code"], tc.code)
			}
		})
	}
}

// TestBlankLinesAreIgnored covers a client that pads the stream, which is
// harmless and should not end the session.
func TestBlankLinesAreIgnored(t *testing.T) {
	replies := session(t, testRegistry(t),
		"", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(replies), replies)
	}
}

// TestNothingButFramesReachOut is the rule that keeps a session alive. A stray
// byte on the output stream is a malformed frame, and the client sees a broken
// server rather than a message.
func TestNothingButFramesReachOut(t *testing.T) {
	var out, log strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: testRegistry(t),
		In: strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
				`{"name":"thing_fail","arguments":{}}}` + "\n",
		),
		Out: &out, Log: &log, Name: "jr", Version: "test",
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("a non-JSON line reached the output stream: %q", line)
		}
	}
}

// TestAPanickingToolDoesNotEndTheSession is why dispatch recovers.
//
// The same command code runs from the CLI, which is exiting anyway, and from
// here, where it holds a session. Without a recover the process dies mid-call:
// the peer does not get an error, it sees stdio close underneath it, which is
// indistinguishable from a crash it caused.
//
// The second call is the assertion that matters. Returning an error for the
// call that panicked would also pass against a server that then died, so this
// asserts the session outlives it.
func TestAPanickingToolDoesNotEndTheSession(t *testing.T) {
	reg := testRegistry(t)
	reg.Register(&registry.Command{
		Path:    []string{"thing", "explode"},
		Summary: "Panic, to prove the server survives it",
		Example: "jr thing explode",
		Outputs: []registry.Output{{Kind: "thing", Version: 1}},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			// Explicit, because the realistic sources — a nil map write, an
			// unchecked type assertion, a slice bound from a response — are
			// exactly what staticcheck refuses to let past in this repo, which
			// is the right answer for production code and makes a fixture out
			// of one awkward. What is under test is that dispatch survives a
			// panic, not which statement produced it.
			panic("a command that panics: internals the peer must never see")
		},
	})

	var log strings.Builder
	var out strings.Builder
	err := mcp.Serve(context.Background(), mcp.Options{
		Registry: reg,
		In: strings.NewReader(strings.Join([]string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
				`"params":{"name":"thing_explode","arguments":{}}}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call",` +
				`"params":{"name":"thing_get","arguments":{"key":"K-1"}}}`,
		}, "\n") + "\n"),
		Out: &out, Log: &log, Name: "jr", Version: "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("serve returned an error rather than surviving: %v", err)
	}

	var replies []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, reply)
	}
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2 — the session ended early:\n%s", len(replies), out.String())
	}

	// The panicking call is answered, as a protocol error rather than a tool
	// error: the tool did not refuse, the server broke.
	rpcErr, ok := replies[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("the panicking call did not return a protocol error: %+v", replies[0])
	}
	if code, _ := rpcErr["code"].(float64); int(code) != -32603 {
		t.Errorf("code = %v, want -32603 (internal error)", rpcErr["code"])
	}

	// The reply carries a verdict, not the panic value: it can hold whatever
	// was in scope, and the peer is owed neither the message nor the stack.
	// Asserted against the whole frame, not just the message field, so moving
	// the detail into Data would still fail.
	const secret = "internals the peer must never see"
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "goroutine") {
		t.Errorf("the panic value reached the peer:\n%s", out.String())
	}
	// It does reach the log, which is the only place it belongs — with the
	// stack, because that is what makes it diagnosable.
	if !strings.Contains(log.String(), "panicked") ||
		!strings.Contains(log.String(), secret) ||
		!strings.Contains(log.String(), "goroutine") {
		t.Errorf("the panic was not logged with its stack:\n%s", log.String())
	}

	// And the session still works, which is the whole point.
	if _, failed := replies[1]["error"]; failed {
		t.Fatalf("the call after the panic failed: %+v", replies[1])
	}
	if replies[1]["result"] == nil {
		t.Fatalf("the call after the panic returned no result: %+v", replies[1])
	}
}
