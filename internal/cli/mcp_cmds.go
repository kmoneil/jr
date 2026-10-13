//go:build mcp

package cli

import (
	"context"
	"strings"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/mcp"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

func init() {
	taggedBuiltins = append(taggedBuiltins, (*app).mcpServeCommand)
}

func (a *app) mcpServeCommand() *registry.Command {
	return &registry.Command{
		Path:    []string{"mcp", "serve"},
		Summary: "Serve this build's commands as MCP tools over stdio",
		Description: strings.TrimSpace(`
Speaks the Model Context Protocol on stdin and stdout, exposing this build's
commands as tools.

The tool list is generated from the same registry that builds the command tree
and ` + "`" + buildinfo.App + ` schema` + "`" + `, so a tool cannot drift from the command behind it,
and adding a command adds a tool for free. It is also the truth about this
binary: a reader build advertises no mutating tools because it contains none.

This command is not among them. It writes the stream the server is speaking on,
so calling it as a tool would start a second server on the same stdin and
stdout — and the call that did it would never return.

A tool call returns the same output the command would print, in the same
formats, with the same defaults — tsv for a collection, xml for a record. A
failure returns the same structured error, so an agent sees one error contract
whichever way it called: a machine-stable code, a remedy, and whether retrying
can help.

There is no exit code in a tool reply, so a truncated result carries its
warning in the content instead. It is never reported as complete.

A single frame may not exceed 8 MiB. One larger is refused, naming the limit,
and the session carries on — a request that is too big is one call failing, not
the end of the conversation.`),
		Example:      buildinfo.App + " mcp serve",
		RequiresTags: []string{"mcp"},
		Flags: []registry.Flag{{
			// This command's own. It writes no timestamp, so it has no
			// derived --tz, and the zone it takes is for the tools it serves.
			Name: registry.ZoneFlagName, Type: registry.TypeString,
			Usage: "timezone tool calls write timestamps in unless a call " +
				"names its own: " + registry.ZoneUTC + "|" + registry.ZoneAccount +
				"|" + registry.ZoneLocal + "|<IANA name> (default: " +
				registry.ZoneUTC + "). " + registry.EnvTZ + " and the context set it too",
		}},
		// The output is the JSON-RPC stream, not a result document. Nothing
		// else may reach stdout for the life of the process.
		OwnsStdout: true,
		ExitCodes:  []exitcode.Code{exitcode.Remote},
		Run:        a.runMCPServe,
	}
}

func (a *app) runMCPServe(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
	zone, err := a.serverZone(inv)
	if err != nil {
		return nil, err
	}
	err = mcp.Serve(ctx, mcp.Options{
		TZ:       zone,
		Registry: a.reg,
		In:       a.stdin,
		Out:      a.stdout,
		// Never a.stdout: a stray byte there is a malformed JSON-RPC frame, and
		// the peer sees a broken session rather than a message.
		Log:     a.stderr,
		Name:    buildinfo.App,
		Version: buildinfo.Release,
		Session: func() (registry.Session, error) { return a.jiraSession() },
	})
	// The stream ending is how a stdio server stops, and there is nothing to
	// report: everything this command had to say has already gone out as
	// frames.
	return nil, err
}

// serverZone is the zone setting tool calls fall back to: this command's own
// --tz, then JIRA_TZ, then the context's, and checked before the server
// starts so a name that is no zone stops it here and not on the first call
// that prints a timestamp.
//
// It is read once. A session is rebuilt for every call so that a credential
// changed underneath a running server is picked up, and a zone is not that
// kind of setting: a call that wants another one names it.
func (a *app) serverZone(inv *registry.Invocation) (string, error) {
	if typed := strings.TrimSpace(inv.Flags.String(registry.ZoneFlagName)); typed != "" {
		return typed, registry.CheckZone("--"+registry.ZoneFlagName, typed)
	}
	def := a.configuredZone()
	return def.Setting, registry.CheckZone(def.From, def.Setting)
}

// configuredZone is the zone the environment or the context asks for, or none
// when no context can be resolved to ask.
//
// A context that cannot be resolved is not this function's to report, which is
// why it has no error to return. A server with no context is still a server,
// for the tools that need none, and each tool call that does need one fails
// with the reason itself. Every tool that writes a timestamp is one of those,
// so nothing is ever written in a zone this guessed.
func (a *app) configuredZone() registry.ZoneDefault {
	cfg, err := a.config()
	if err != nil {
		return registry.ZoneDefault{}
	}
	resolved, err := a.resolve(cfg)
	if err != nil {
		return registry.ZoneDefault{}
	}
	return zoneDefault(resolved)
}
