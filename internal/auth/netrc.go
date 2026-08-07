package auth

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// NetrcProvider reads a credential from a .netrc file.
//
// It is last in the chain because .netrc is shared with curl, git, and
// everything else: a match there is the least specific statement that this
// credential is meant for this tool.
//
// # The mode is not checked, and that is a decision
//
// FileStore.load refuses a credential store any other user can read, and the
// argument written above that check — reading it anyway would mean the
// credential is used, and stays exposed, every time — applies word for word to
// a world-readable .netrc holding the same kind of secret. This reads it
// anyway. The asymmetry is deliberate, and it is here rather than in a commit
// message because a reader comparing the two functions cannot otherwise tell
// whether it was decided or missed.
//
// The store is a file this tool creates, writes at 0600, and owns; refusing to
// read it when its mode has changed is holding jr to its own guarantee. .netrc
// is none of those things. It predates this tool on most machines that have
// one, it is read by curl and git and everything else, and curl reads a 0644
// file without complaint — verified, not assumed. Refusing would make jr the
// one tool that broke on a machine where nothing else objects, over a mode jr
// did not set and cannot fix without changing a file it does not own.
//
// docs/architecture.md says the same thing about directories, for the same
// reason: an existing 0755 is not repaired on read, because changing
// permissions nobody asked this tool to change is its own surprise.
//
// Warning on every invocation was the third option and is the weakest. It
// leaves the credential exposed exactly as ignoring does, while adding noise a
// caller may have already decided about — and .netrc is last in the chain, so
// the warning would fire for the least authoritative source there is.
//
// What this does *not* say is that 0644 is fine. `chmod 600 ~/.netrc` costs
// nothing — curl reads a 0600 file too — and is worth doing. It is not jr's to
// enforce.
//
// TestANetrcIsReadWhateverItsMode pins this, so the decision cannot be
// reversed by accident.
type NetrcProvider struct {
	// Path is the file to read. Empty means $NETRC, then ~/.netrc.
	Path   string
	Getenv Getenv
}

// Name implements Provider.
func (NetrcProvider) Name() string { return ".netrc" }

// Lookup implements Provider.
func (p NetrcProvider) Lookup(site string) (Credential, bool, error) {
	path := p.resolvePath()
	if path == "" {
		return Credential{}, false, nil
	}

	// Read at whatever mode it has. See the type's doc comment: this is the
	// decision, not an omission, and it is the opposite of what FileStore.load
	// does with the store.
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's own netrc.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Credential{}, false, nil
		}
		return Credential{}, false, errs.Auth("NETRC_UNREADABLE",
			"cannot read %s", path).WithRemedy("check the file's permissions").Wrap(err)
	}

	entry, ok := parseNetrc(string(data), hostOf(site))
	if !ok {
		return Credential{}, false, nil
	}
	if entry.password == "" {
		return Credential{}, false, nil
	}

	return Credential{
		Scheme: inferScheme(entry.login),
		User:   entry.login,
		Secret: Secret(entry.password),
		Source: path,
	}, true, nil
}

func (p NetrcProvider) resolvePath() string {
	if p.Path != "" {
		return p.Path
	}
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if override := strings.TrimSpace(getenv("NETRC")); override != "" {
		return override
	}
	home := strings.TrimSpace(getenv("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return ""
		}
	}
	return filepath.Join(home, ".netrc")
}

type netrcEntry struct {
	machine  string
	login    string
	password string
}

// parseNetrc finds the entry for a host.
//
// The format is a flat token stream, not a line-oriented one: `machine x login
// y password z` on one line and spread across four are the same file. A default
// entry, if present, matches any host not named explicitly, and is only used
// when no explicit entry matched.
func parseNetrc(content, host string) (netrcEntry, bool) {
	var (
		current    netrcEntry
		fallback   netrcEntry
		haveMatch  bool
		haveFall   bool
		inDefault  bool
		collecting bool
	)

	commit := func() {
		switch {
		case !collecting:
		case inDefault:
			if !haveFall {
				fallback, haveFall = current, true
			}
		case current.machine == host:
			if !haveMatch {
				current.machine = host
				haveMatch = true
				fallback = current
			}
		}
	}

	tokens := netrcTokens(content)
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "machine":
			commit()
			if haveMatch {
				return fallback, true
			}
			i++
			if i >= len(tokens) {
				return netrcEntry{}, false
			}
			current = netrcEntry{machine: strings.ToLower(tokens[i])}
			inDefault, collecting = false, true

		case "default":
			commit()
			if haveMatch {
				return fallback, true
			}
			current = netrcEntry{}
			inDefault, collecting = true, true

		case "login", "user":
			i++
			if i < len(tokens) && collecting {
				current.login = tokens[i]
			}

		case "password", "account":
			i++
			if i < len(tokens) && collecting && tokens[i-1] == "password" {
				current.password = tokens[i]
			}

		case "macdef":
			// A macro definition runs to a blank line. Skipping to the next
			// keyword is enough, since a macro body cannot contain one at the
			// start of a token.
			i++
		}
	}
	commit()

	if haveMatch || haveFall {
		return fallback, true
	}
	return netrcEntry{}, false
}

// netrcTokens splits on whitespace, honoring double-quoted values so a password
// containing a space survives.
func netrcTokens(content string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, splitQuoted(line)...)
	}
	return out
}

func splitQuoted(line string) []string {
	var (
		out     []string
		current strings.Builder
		quoted  bool
		started bool
	)
	flush := func() {
		if started {
			out = append(out, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			quoted = !quoted
			started = true
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}
