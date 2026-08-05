//go:build mcp

package mcp

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// FormatArgument is the name every tool exposes for choosing an output shape.
const FormatArgument = "format"

// Tool is one entry in a tools/list response.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Tools converts every registered command into a tool.
//
// The list is the command list of this build. A reader binary advertises no
// mutating tools because it contains none — an agent introspecting the server
// sees the truth rather than a set of tools that will refuse.
func Tools(reg *registry.Registry) []Tool {
	commands := reg.All()
	out := make([]Tool, 0, len(commands))
	for _, cmd := range commands {
		out = append(out, Tool{
			Name:        commandToTool(cmd.Name()),
			Description: toolDescription(cmd),
			InputSchema: inputSchema(cmd),
		})
	}
	return out
}

// commandToTool converts issue.list to issue_list.
//
// MCP tool names are conventionally identifier-shaped, and several clients
// reject a dot. The mapping is total and reversible, so nothing is lost.
func commandToTool(name string) string { return strings.ReplaceAll(name, ".", "_") }

// toolToCommand is the inverse.
func toolToCommand(name string) string { return strings.ReplaceAll(name, "_", ".") }

// toolDescription gives the model what it needs to choose and use the tool:
// what it does, whether it changes anything, and what it returns.
func toolDescription(cmd *registry.Command) string {
	var b strings.Builder
	b.WriteString(cmd.Summary)
	b.WriteString(".\n\n")
	if cmd.Description != "" {
		b.WriteString(cmd.Description)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "Returns output of kind %s (schema v%d).\n",
		cmd.Kind(), cmd.KindVersion())
	switch {
	case cmd.Mutating:
		b.WriteString("This CHANGES Jira.\n")
	case cmd.LocalState:
		b.WriteString("This changes local configuration, not Jira.\n")
	default:
		b.WriteString("This only reads.\n")
	}
	if cmd.Destructive {
		b.WriteString("This is DESTRUCTIVE and requires yes=true.\n")
	}
	if cmd.Paginated {
		b.WriteString("A result cut short by limit is reported as incomplete " +
			"and carries a page token to resume from; it is never silently truncated.\n")
	}
	return strings.TrimSpace(b.String())
}

// inputSchema builds the JSON Schema for a tool's arguments.
func inputSchema(cmd *registry.Command) map[string]any {
	properties := map[string]any{}
	var required []string

	for _, arg := range cmd.Args {
		properties[arg.Name] = map[string]any{
			"type":        "string",
			"description": arg.Usage,
		}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}

	for _, flag := range cmd.Flags {
		properties[flag.Name] = flagSchema(flag)
		if flag.Required {
			required = append(required, flag.Name)
		}
	}

	if cmd.Paginated {
		properties["limit"] = map[string]any{
			"type": "string",
			"description": fmt.Sprintf(
				"maximum results, or \"all\" to exhaust the result set (default %d)",
				registry.DefaultLimit,
			),
		}
	}

	properties[FormatArgument] = map[string]any{
		"type":        "string",
		"enum":        render.FormatNames(),
		"description": "output format; defaults to tsv for lists and xml for records",
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
		// Refusing an unknown argument is the same rule as the CLI's: a
		// misspelled option that is silently ignored is a request that did
		// something other than what was asked.
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func flagSchema(flag registry.Flag) map[string]any {
	schema := map[string]any{"description": flag.Usage}

	switch {
	case flag.Repeatable:
		schema["type"] = "array"
		schema["items"] = map[string]any{"type": "string"}
	case flag.Type == registry.TypeBool:
		schema["type"] = "boolean"
	case flag.Type == registry.TypeInt:
		schema["type"] = "integer"
	default:
		schema["type"] = "string"
	}
	if len(flag.Enum) > 0 {
		schema["enum"] = flag.Enum
	}
	if flag.Default != "" {
		schema["default"] = flag.Default
	}
	return schema
}

// invocation turns a tool call's arguments into what a command expects.
//
// An argument the tool did not advertise is refused rather than ignored. The
// schema says additionalProperties is false, and a server that then accepted
// them would be describing itself inaccurately — which is the drift this whole
// design exists to prevent.
func (s *Server) invocation(
	ctx context.Context, cmd *registry.Command, args map[string]any,
) (*registry.Invocation, render.Format, error) {
	known := map[string]bool{FormatArgument: true, "limit": cmd.Paginated}
	for _, arg := range cmd.Args {
		known[arg.Name] = true
	}
	for _, flag := range cmd.Flags {
		known[flag.Name] = true
	}
	for name := range args {
		if !known[name] {
			return nil, "", errs.Usage("UNKNOWN_ARGUMENT",
				"%s has no argument %q", commandToTool(cmd.Name()), name).
				WithRemedy("call tools/list for the arguments this tool accepts")
		}
	}

	flags := registry.NewFlags()
	for _, flag := range cmd.Flags {
		raw, ok := args[flag.Name]
		if !ok {
			continue
		}
		if err := setFlag(flags, flag, raw); err != nil {
			return nil, "", err
		}
	}

	positional := make([]string, 0, len(cmd.Args))
	for _, arg := range cmd.Args {
		raw, ok := args[arg.Name]
		if !ok {
			if arg.Required {
				return nil, "", errs.Usage("MISSING_ARGUMENT",
					"%s requires %q", commandToTool(cmd.Name()), arg.Name)
			}
			continue
		}
		text, err := asString(arg.Name, raw)
		if err != nil {
			return nil, "", err
		}
		positional = append(positional, text)
	}

	limit := registry.Limit{N: registry.DefaultLimit}
	if raw, ok := args["limit"]; ok {
		text, err := asString("limit", raw)
		if err != nil {
			return nil, "", err
		}
		if limit, err = registry.ParseLimit(text); err != nil {
			return nil, "", err
		}
	}

	inv := &registry.Invocation{
		Args:  positional,
		Flags: flags,
		Limit: limit,
		// A tool call has no terminal and no stderr worth writing to, so
		// nothing reports progress and nothing writes a diagnostic anywhere
		// but into the reply.
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	if cmd.NeedsJira {
		if s.opt.Session == nil {
			return nil, "", errs.Runtime("NO_SESSION",
				"this server has no way to reach Jira")
		}
		session, err := s.opt.Session()
		if err != nil {
			return nil, "", err
		}
		inv.Jira = session
	}

	if cmd.Validate != nil {
		if err := cmd.Validate(ctx, inv); err != nil {
			return nil, "", err
		}
	}

	format, err := resolveFormat(cmd, args)
	if err != nil {
		return nil, "", err
	}
	return inv, format, nil
}

// resolveFormat picks the output shape, defaulting the same way the CLI does:
// TSV for a collection, XML for a record.
func resolveFormat(cmd *registry.Command, args map[string]any) (render.Format, error) {
	if raw, ok := args[FormatArgument]; ok {
		text, err := asString(FormatArgument, raw)
		if err != nil {
			return "", err
		}
		return render.ParseFormat(text)
	}
	if cmd.Streams() {
		return render.TSV, nil
	}
	return render.XML, nil
}

func setFlag(flags registry.Flags, flag registry.Flag, raw any) error {
	if flag.Repeatable {
		values, ok := raw.([]any)
		if !ok {
			// A single value where a list was advertised is a common and
			// harmless mistake; accept it rather than refusing over shape.
			text, err := asString(flag.Name, raw)
			if err != nil {
				return err
			}
			flags.SetString(flag.Name, text)
			return nil
		}
		for _, v := range values {
			text, err := asString(flag.Name, v)
			if err != nil {
				return err
			}
			flags.SetString(flag.Name, text)
		}
		return nil
	}

	switch flag.Type {
	case registry.TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return errs.Usage("INVALID_ARGUMENT", "%q expects true or false", flag.Name).
				WithDetail("got %T", raw)
		}
		flags.SetBool(flag.Name, b)
	case registry.TypeInt:
		f, ok := raw.(float64)
		if !ok {
			return errs.Usage("INVALID_ARGUMENT", "%q expects a number", flag.Name).
				WithDetail("got %T", raw)
		}
		flags.SetInt(flag.Name, int(f))
	default:
		text, err := asString(flag.Name, raw)
		if err != nil {
			return err
		}
		if len(flag.Enum) > 0 && !contains(flag.Enum, text) {
			return errs.Usage("INVALID_ARGUMENT",
				"%q does not accept %q", flag.Name, text).
				WithDetail("valid values: %s", strings.Join(flag.Enum, ", "))
		}
		flags.SetString(flag.Name, text)
	}
	return nil
}

// asString accepts the JSON scalars a model is likely to produce for something
// the schema calls a string, rather than refusing over a type it got nearly
// right.
func asString(name string, raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return "", errs.Usage("INVALID_ARGUMENT", "%q expects a string", name).
			WithDetail("got %T", raw)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
