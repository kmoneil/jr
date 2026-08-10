//go:build mcp

package cli

import (
	"context"
	"strings"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/mcp"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
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
		// The output is the JSON-RPC stream, not a result document. Nothing
		// else may reach stdout for the life of the process.
		OwnsStdout: true,
		ExitCodes:  []exitcode.Code{exitcode.Remote},
		Run:        a.runMCPServe,
	}
}

func (a *app) runMCPServe(ctx context.Context, _ *registry.Invocation) (*render.Doc, error) {
	err := mcp.Serve(ctx, mcp.Options{
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
