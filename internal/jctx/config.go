package jctx

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kmoneil/jr/internal/errs"
)

// Config is the contents of config.toml.
type Config struct {
	// Current is the context `jr` uses when none is named.
	Current string `toml:"current,omitempty"`
	// Contexts is keyed by context name.
	Contexts map[string]Context `toml:"contexts,omitempty"`

	// path is where this config was loaded from, so Save writes it back to the
	// same place without the caller having to remember.
	path string
}

// configHeader is written above every generated file, because the file is
// meant to be hand-edited and a reader deserves to know what rewrites it.
const configHeader = `# jr configuration.
#
# Hand-editable. Rewritten by ` + "`jr context`" + ` commands, which do not
# preserve comments outside this header.
#
# Credentials are NOT stored here. This file is safe to keep in a dotfiles
# repository; the credential store is separate and is not.

`

// Load reads the config file. A missing file is not an error: it is a caller
// who has not created a context yet, and every command that needs one says so
// itself with a remedy.
func Load(path string) (*Config, error) {
	cfg := &Config{Contexts: map[string]Context{}, path: path}

	data, err := os.ReadFile(path) //nolint:gosec // the path comes from XDG resolution or an explicit flag.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, errs.Runtime("CONFIG_UNREADABLE", "cannot read %s", path).
			WithRemedy("check the file's permissions").Wrap(err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, errs.Usage("CONFIG_INVALID", "cannot parse %s", path).
			WithDetail("%s", err.Error()).
			WithRemedy("fix the syntax, or move the file aside and run `jr context create`")
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	cfg.path = path

	if err := cfg.validate(path); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate rejects a config that would make a later command behave
// surprisingly, at load time rather than at use time.
func (c *Config) validate(path string) error {
	for name, ctx := range c.Contexts {
		if err := ValidateName(name); err != nil {
			return errs.Usage("CONFIG_INVALID", "%s defines an invalid context name", path).
				WithDetail("%s", err.Error())
		}
		if err := ctx.Validate(); err != nil {
			return errs.Usage("CONFIG_INVALID", "context %q in %s is not usable", name, path).
				WithDetail("%s", err.Error())
		}
	}
	// A current context naming something that does not exist would otherwise
	// surface as a confusing failure on an unrelated command.
	if c.Current != "" {
		if _, ok := c.Contexts[c.Current]; !ok {
			return errs.Usage("CONFIG_INVALID",
				"%s selects context %q, which it does not define", path, c.Current).
				WithDetail("defined: %s", strings.Join(c.Names(), ", ")).
				WithRemedy("run `jr context use <name>`, or fix the file by hand")
		}
	}
	return nil
}

// Path returns where this config was loaded from.
func (c *Config) Path() string { return c.path }

// Names returns every context name, sorted.
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Contexts))
	for name := range c.Contexts {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Get returns a context by name.
func (c *Config) Get(name string) (Context, bool) {
	ctx, ok := c.Contexts[name]
	if !ok {
		return Context{}, false
	}
	return ctx.clone(), true
}

// Set adds or replaces a context.
func (c *Config) Set(name string, ctx Context) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := ctx.Validate(); err != nil {
		return err
	}
	normalized, err := NormalizeSite(ctx.Site)
	if err != nil {
		return err
	}
	ctx.Site = normalized

	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	c.Contexts[name] = ctx.clone()
	// The first context created becomes current, so a caller who makes one
	// context never has to run `context use` to make it take effect.
	if c.Current == "" {
		c.Current = name
	}
	return nil
}

// Use selects the current context.
func (c *Config) Use(name string) error {
	if _, ok := c.Contexts[name]; !ok {
		return c.unknownContext(name)
	}
	c.Current = name
	return nil
}

// Delete removes a context.
func (c *Config) Delete(name string) error {
	if _, ok := c.Contexts[name]; !ok {
		return c.unknownContext(name)
	}
	delete(c.Contexts, name)

	if c.Current != name {
		return nil
	}
	// Deleting the current context must not leave the config pointing at
	// something that no longer exists. With exactly one left, choosing it is
	// unambiguous; with several, the caller decides.
	c.Current = ""
	if remaining := c.Names(); len(remaining) == 1 {
		c.Current = remaining[0]
	}
	return nil
}

func (c *Config) unknownContext(name string) error {
	e := errs.NotFound("UNKNOWN_CONTEXT", "no context named %q", name)
	if names := c.Names(); len(names) > 0 {
		return e.WithDetail("defined: %s", strings.Join(names, ", "))
	}
	return e.WithRemedy("create one with `jr context create <name> --site <host>`")
}

// Save writes the config atomically.
//
// Atomically because a config truncated by a crash mid-write costs a user every
// context they had, and the failure would surface much later as an unrelated
// command claiming no context is configured.
func (c *Config) Save() error {
	if c.path == "" {
		return errs.Runtime("NO_CONFIG_PATH", "this config has no path to save to")
	}
	// 0700, matching the state directory, even though the file inside it is
	// deliberately 0644.
	//
	// The two modes answer different questions and only one of them travels.
	// 0644 on config.toml says the file is not a secret and survives being
	// committed to a dotfiles repository, which is what it is for; the
	// directory mode is left behind by that copy entirely. So 0700 costs
	// nothing 0644 was buying, and it stops another user on this machine
	// reading the site hostnames, context names, and project keys.
	//
	// MkdirAll does not touch a directory that already exists, so this applies
	// to new installs and leaves an existing 0755 alone. Repairing one on read
	// would be this tool changing permissions nobody asked it to change.
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return errs.Runtime("CONFIG_UNWRITABLE",
			"cannot create %s", filepath.Dir(c.path)).Wrap(err)
	}

	var buf bytes.Buffer
	buf.WriteString(configHeader)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return errs.Runtime("CONFIG_UNWRITABLE", "cannot encode the config").Wrap(err)
	}

	return writeFileAtomic(c.path, buf.Bytes(), 0o644)
}

// writeFileAtomic writes via a temporary file in the same directory, then
// renames. Same directory because rename is only atomic within a filesystem.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return errs.Runtime("WRITE_FAILED", "cannot create a temporary file in %s", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.Runtime("WRITE_FAILED", "cannot write %s", tmpName).Wrap(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return errs.Runtime("WRITE_FAILED", "cannot set permissions on %s", tmpName).Wrap(err)
	}
	// Sync before rename, so the rename cannot expose a file whose contents
	// have not reached disk.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errs.Runtime("WRITE_FAILED", "cannot flush %s", tmpName).Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return errs.Runtime("WRITE_FAILED", "cannot close %s", tmpName).Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errs.Runtime("WRITE_FAILED", "cannot replace %s", path).Wrap(err)
	}
	return nil
}
