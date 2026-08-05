package auth

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/kmoneil/jira-cli/internal/errs"
)

// storePerm is the only mode the credential file may have. A credential
// readable by other users on the machine is not stored, it is published.
const storePerm fs.FileMode = 0o600

// storeHeader explains the file to whoever opens it.
const storeHeader = `# jr credentials. Keep this file private.
#
# Written by ` + "`jr auth login`" + `. Mode 0600 is enforced on read: this
# file is refused if it is readable by anyone else.
#
# This is NOT the config file. Do not commit it.

`

// storedCredential is the on-disk shape. Secret is a plain string here because
// TOML has to encode it; it becomes a Secret the moment it is read.
type storedCredential struct {
	Scheme string `toml:"scheme"`
	User   string `toml:"user,omitempty"`
	Token  string `toml:"token"`
}

type storeFile struct {
	Credentials map[string]storedCredential `toml:"credentials,omitempty"`
}

// FileStore is a credential store on disk, keyed by host.
//
// It sits under the state directory rather than beside the config, because the
// config is meant to be hand-edited, shared, and committed to a dotfiles
// repository, and a credential must not be swept along with it.
type FileStore struct{ Path string }

// Name implements Provider.
func (s FileStore) Name() string {
	if s.Path == "" {
		return "credential store"
	}
	return s.Path
}

// Lookup implements Provider.
func (s FileStore) Lookup(site string) (Credential, bool, error) {
	file, err := s.load()
	if err != nil {
		return Credential{}, false, err
	}
	stored, ok := file.Credentials[hostOf(site)]
	if !ok {
		return Credential{}, false, nil
	}
	scheme, err := ParseScheme(stored.Scheme)
	if err != nil {
		return Credential{}, false, errs.Auth("STORE_INVALID",
			"the stored credential for %s has an unknown scheme %q", site, stored.Scheme).
			WithRemedy("run `jr auth login --site %s` to replace it", site)
	}
	return Credential{
		Scheme: scheme,
		User:   stored.User,
		Secret: Secret(stored.Token),
		Source: s.Path,
	}, true, nil
}

// Save writes a credential for a site, replacing any existing one.
func (s FileStore) Save(site string, cred Credential) error {
	if s.Path == "" {
		return errs.Runtime("NO_STORE_PATH", "the credential store has no path")
	}
	if err := cred.Validate(); err != nil {
		return err
	}

	file, err := s.load()
	if err != nil {
		return err
	}
	if file.Credentials == nil {
		file.Credentials = map[string]storedCredential{}
	}
	file.Credentials[hostOf(site)] = storedCredential{
		Scheme: string(cred.Scheme),
		User:   cred.User,
		Token:  cred.Secret.Reveal(),
	}
	return s.write(file)
}

// Delete removes the credential for a site. Removing one that is not there is
// not an error: the caller asked for it to be gone, and it is.
func (s FileStore) Delete(site string) (bool, error) {
	file, err := s.load()
	if err != nil {
		return false, err
	}
	host := hostOf(site)
	if _, ok := file.Credentials[host]; !ok {
		return false, nil
	}
	delete(file.Credentials, host)
	return true, s.write(file)
}

// Hosts returns every site with a stored credential, sorted.
func (s FileStore) Hosts() ([]string, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	return sortedKeys(file.Credentials), nil
}

func (s FileStore) load() (*storeFile, error) {
	file := &storeFile{Credentials: map[string]storedCredential{}}
	if s.Path == "" {
		return file, nil
	}

	info, err := os.Stat(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return file, nil
		}
		return nil, errs.Auth("STORE_UNREADABLE", "cannot read %s", s.Path).Wrap(err)
	}
	// Refuse a file others can read. Reading it anyway and warning would mean
	// the credential is used, and stays exposed, every time.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, errs.Auth("STORE_PERMISSIONS",
			"%s is readable by other users", s.Path).
			WithDetail("mode is %04o, want %04o", perm, storePerm).
			WithRemedy("run: chmod 600 %s", s.Path)
	}

	data, err := os.ReadFile(s.Path) //nolint:gosec // the path comes from XDG resolution.
	if err != nil {
		return nil, errs.Auth("STORE_UNREADABLE", "cannot read %s", s.Path).Wrap(err)
	}
	if err := toml.Unmarshal(data, file); err != nil {
		return nil, errs.Auth("STORE_INVALID", "cannot parse %s", s.Path).
			WithDetail("%s", err.Error()).
			WithRemedy("move the file aside and run `jr auth login` again")
	}
	if file.Credentials == nil {
		file.Credentials = map[string]storedCredential{}
	}
	return file, nil
}

func (s FileStore) write(file *storeFile) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return errs.Auth("STORE_UNWRITABLE",
			"cannot create %s", filepath.Dir(s.Path)).Wrap(err)
	}

	var buf bytes.Buffer
	buf.WriteString(storeHeader)
	if err := toml.NewEncoder(&buf).Encode(file); err != nil {
		return errs.Auth("STORE_UNWRITABLE", "cannot encode the credential store").Wrap(err)
	}
	return writeSecretFile(s.Path, buf.Bytes())
}

// writeSecretFile writes atomically at mode 0600.
//
// The temporary file is created with the final mode rather than chmod'ed
// afterwards, so there is no instant during which the credential exists on disk
// world-readable.
func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return errs.Auth("STORE_UNWRITABLE", "cannot create a temporary file in %s", dir).Wrap(err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(storePerm); err != nil {
		_ = tmp.Close()
		return errs.Auth("STORE_UNWRITABLE", "cannot restrict %s", tmpName).Wrap(err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.Auth("STORE_UNWRITABLE", "cannot write %s", tmpName).Wrap(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errs.Auth("STORE_UNWRITABLE", "cannot flush %s", tmpName).Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return errs.Auth("STORE_UNWRITABLE", "cannot close %s", tmpName).Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errs.Auth("STORE_UNWRITABLE", "cannot replace %s", path).Wrap(err)
	}
	return nil
}

// DefaultChain is the provider order every command uses.
//
// Environment first so a CI job can override what is on disk without editing
// it; the file store next because it is this tool's own and the most specific;
// netrc last because it is shared with everything else on the machine.
func DefaultChain(getenv Getenv, storePath string) Chain {
	return Chain{
		EnvProvider{Getenv: getenv},
		FileStore{Path: storePath},
		NetrcProvider{Getenv: getenv},
	}
}
