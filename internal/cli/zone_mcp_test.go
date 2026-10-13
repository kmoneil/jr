//go:build mcp

package cli_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// served runs `jr mcp serve` with the given flags in the given session and
// returns the replies, in order.
func served(t *testing.T, env map[string]string, flags []string, requests ...string) []map[string]any {
	t.Helper()

	stdin := strings.NewReader(strings.Join(requests, "\n") + "\n")
	got := runWithStdin(t, env, stdin, append([]string{"mcp", "serve"}, flags...)...)
	if got.exit != exitcode.OK {
		t.Fatalf("mcp serve exited %v\nstderr: %s", got.exit, got.stderr)
	}
	var replies []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(got.stdout), "\n") {
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

// call is one tools/call frame.
func call(id int, tool string, args map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}`, id, raw)
}

// toolAnswer is the text of a tool reply and whether it was an error.
func toolAnswer(t *testing.T, reply map[string]any) (string, bool) {
	t.Helper()
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", reply)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content: %v", result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

// TestAToolCallIsWrittenInTheServersZoneUnlessItNamesOne is issue 218 for the
// caller it was raised by: an agent summarising answers for people who think
// in local time.
//
// A tool call takes tz as it takes limit, because the argument is derived from
// the command and the tool list is built from that. A call that names none
// falls back to the server's own setting, which is the zone `mcp serve` was
// started with, or JIRA_TZ, or its context's. That is one more level than the
// command line has, and the order is the same: the nearest one wins.
func TestAToolCallIsWrittenInTheServersZoneUnlessItNamesOne(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")
	get := func(id int, args map[string]any) string {
		with := map[string]any{"key": "ENG-1"}
		maps.Copy(with, args)
		return call(id, "issue_get", with)
	}

	for _, tc := range []struct {
		name    string
		context []string
		flags   []string
		args    map[string]any
		want    string
	}{
		{"nothing asked is UTC", nil, nil, nil, updatedUTC},
		{"the server's own flag", nil, []string{"--tz", "Europe/Berlin"}, nil, updatedBerlin},
		{"the server's context", []string{"--tz", "Europe/Berlin"}, nil, nil, updatedBerlin},
		{"the call's argument", nil, nil, map[string]any{"tz": "Asia/Tokyo"}, updatedTokyo},
		{
			"the call over the server", nil,
			[]string{"--tz", "Europe/Berlin"},
			map[string]any{"tz": "Asia/Tokyo"},
			updatedTokyo,
		},
		{
			"utc asked over a server in a zone", nil,
			[]string{"--tz", "Europe/Berlin"},
			map[string]any{"tz": "utc"},
			updatedUTC,
		},
		{"the account's zone", nil, nil, map[string]any{"tz": "account"}, updatedChicago},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replies := served(t, site.in(t, tc.context...), tc.flags, get(1, tc.args))
			text, isError := toolAnswer(t, replies[0])
			if isError {
				t.Fatalf("the call failed:\n%s", text)
			}
			if !strings.Contains(text, "<updated>"+tc.want+"</updated>") {
				t.Errorf("updated is not %s:\n%s", tc.want, text)
			}
		})
	}

	// A collection takes the other route through the server, row by row.
	t.Run("a collection", func(t *testing.T) {
		replies := served(t, site.in(t), nil,
			call(1, "issue_list", map[string]any{"tz": "Asia/Tokyo"}))
		text, isError := toolAnswer(t, replies[0])
		if isError || !strings.Contains(text, updatedTokyo) || strings.Contains(text, updatedUTC) {
			t.Errorf("the rows are not in the zone (error=%v):\n%s", isError, text)
		}
	})

	t.Run("a zone that names nothing is that call's failure", func(t *testing.T) {
		replies := served(t, site.in(t), nil,
			get(1, map[string]any{"tz": "Mars/Olympus"}), get(2, nil))
		if text, isError := toolAnswer(t, replies[0]); !isError || !strings.Contains(text, "INVALID_TZ") {
			t.Errorf("want INVALID_TZ, got (error=%v):\n%s", isError, text)
		}
		// And the session carries on.
		if text, isError := toolAnswer(t, replies[1]); isError || !strings.Contains(text, updatedUTC) {
			t.Errorf("the call after it did not answer (error=%v):\n%s", isError, text)
		}
	})
}

// TestOnlyAToolThatWritesATimestampTakesAZone is the tool list telling the
// truth about the argument. A tool whose command writes no timestamp does not
// advertise tz, and refuses it like any other argument it did not advertise,
// where accepting it would be an option that did nothing.
func TestOnlyAToolThatWritesATimestampTakesAZone(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")
	replies := served(t, site.in(t), nil,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		call(2, "version", map[string]any{"tz": "Europe/Berlin"}))

	result, _ := replies[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	takes := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		_, takes[name] = properties["tz"]
	}
	for _, name := range []string{"issue_get", "issue_list"} {
		if !takes[name] {
			t.Errorf("%s writes timestamps and advertises no tz", name)
		}
	}
	for _, name := range []string{"version", "doctor", "context_list"} {
		if takes[name] {
			t.Errorf("%s advertises a tz it would not use", name)
		}
	}

	if text, isError := toolAnswer(t, replies[1]); !isError || !strings.Contains(text, "UNKNOWN_ARGUMENT") {
		t.Errorf("version took a tz argument (error=%v):\n%s", isError, text)
	}
}

// TestAServerIsNotStartedInAZoneThatNamesNothing stops the mistake where it
// is made. Started anyway, the server would fail the first call that printed a
// timestamp and every one after it, with a reason that points at a flag the
// caller of the tool never typed.
func TestAServerIsNotStartedInAZoneThatNamesNothing(t *testing.T) {
	site := newZoneSite(t, "America/Chicago")
	got := runWithStdin(t, site.in(t), strings.NewReader(""),
		"mcp", "serve", "--tz", "Mars/Olympus")
	if got.exit != exitcode.Usage || !strings.Contains(got.stderr, "INVALID_TZ") {
		t.Errorf("exit = %v, want INVALID_TZ at %v\nstderr: %s",
			got.exit, exitcode.Usage, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a server that did not start wrote to the stream:\n%s", got.stdout)
	}
}
