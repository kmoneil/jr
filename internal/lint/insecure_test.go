package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
)

// TestNothingCanDisableCertificateVerification is a refusal held by a test
// rather than by a comment, which is the difference between a decision and an
// omission.
//
// A private CA and a client certificate are the legitimate needs behind
// corporate TLS, and `--ca-bundle` answers both. Skipping verification is not
// one of them. Every tool that has shipped an `--insecure` has it in a wiki page
// somewhere as the standard fix for a certificate problem, and following that
// page sends a credential to whichever host asked for it — which is the failure
// this tool's whole transport exists to make impossible, since none of the
// redaction, the off-site URL refusal, or the relative-path rule survives a
// connection nobody verified.
//
// It is checked three ways because there are three ways to reintroduce it: a
// flag on a command, an option on the transport, and a field on a TLS
// configuration. The first two are surfaces somebody would add deliberately;
// the third is one line in a struct literal.
func TestNothingCanDisableCertificateVerification(t *testing.T) {
	t.Run("no command declares such a flag", func(t *testing.T) {
		// Named for TLS specifically. `auth login --no-verify` exists and means
		// something else entirely — store the credential without checking it
		// against the site — and a list that caught it would be a list somebody
		// deletes rather than reads.
		banned := []string{
			"insecure", "skip-verify", "tls-skip-verify",
			"insecure-skip-verify", "no-tls-verify", "skip-tls-verify",
		}
		for _, c := range cli.Registry().All() {
			for _, f := range c.AllFlags() {
				for _, bad := range banned {
					if f.Name != bad {
						continue
					}
					t.Errorf("%s declares --%s. Certificate verification is not "+
						"optional in this tool: --ca-bundle is how a private "+
						"root is trusted and a client certificate is how mTLS "+
						"is answered, and neither needs verification turned "+
						"off. See docs/invariants.md.", c.Name(), f.Name)
				}
			}
		}
	})

	t.Run("no source sets InsecureSkipVerify", func(t *testing.T) {
		for path, line := range grepTree(t, regexp.MustCompile(`InsecureSkipVerify`)) {
			t.Errorf("%s sets InsecureSkipVerify: %s\n"+
				"There is no supported way to reach this, and a line that "+
				"reaches it is the whole of the refusal gone.", path, line)
		}
	})

	t.Run("no environment variable is read for it", func(t *testing.T) {
		// The env var half matters as much as the flag: an operator following a
		// wiki page will export something before they edit a config file, and a
		// variable read here would be a flag with no help text and no docs.
		pattern := regexp.MustCompile(`(?i)"[A-Z_]*(INSECURE|SKIP_VERIFY|NO_VERIFY)[A-Z_]*"`)
		for path, line := range grepTree(t, pattern) {
			t.Errorf("%s reads %s from the environment", path, line)
		}
	})
}

// grepTree returns the first matching line of every shipped Go file, keyed by
// path. Test files are excluded: a test that asserts the absence has to be able
// to name the thing it is asserting the absence of.
func grepTree(t *testing.T, pattern *regexp.Regexp) map[string]string {
	t.Helper()

	out := map[string]string{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "bin" || strings.HasPrefix(name, "_") ||
				name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // walking this module's own tree.
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(body), "\n") {
			if pattern.MatchString(line) {
				rel, _ := filepath.Rel(repoRoot, path)
				out[rel] = strings.TrimSpace(line)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}
