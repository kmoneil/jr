package lint_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const rigCommon = repoRoot + "/scripts/dc/common.sh"

// TestTheRigRefusesAProfileThatNamesAnotherAddress drives require_rig, the
// guard every recording script runs first, against a fake rig on loopback.
//
// `make dc-up` recreates the compose network and Docker hands it the first
// free subnet, so inside a dev container the bridge address depends on what
// else is running that day. `auth login` leaves an existing context alone by
// design. So for a fortnight the throwaway profile named yesterday's
// container, and the first command against it was a TIMEOUT that read as a
// network failure, after `auth status` had reported authenticated, which it
// does without contacting Jira. Every answer along the way was correct and
// none of them said the address was stale.
//
// The guard says so, with both values. It is driven here rather than read,
// because a guard that compares the wrong field of the profile, or compares
// it against the wrong address, passes a stale profile in exactly the way the
// old check did, and the only place that shows is against a rig.
func TestTheRigRefusesAProfileThatNamesAnotherAddress(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("no %s on PATH, and scripts/dc/common.sh is written in it: %v", tool, err)
		}
	}
	bin := buildProfile(t, t.TempDir(), fullProfile(t))

	// What the rig answers: /status, which is how jira_base finds it, and
	// serverInfo over the profile's token, which is where the version line
	// comes from. The instance is set up in private mode, so an anonymous
	// serverInfo is an empty error and the guard has to send the token.
	rig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_, _ = io.WriteString(w, `{"state":"RUNNING"}`)
		case "/rest/api/2/serverInfo":
			if r.Header.Get("Authorization") != "Bearer recorded-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"version":"10.4.0","deploymentType":"Server"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rig.Close()
	port := rig.URL[strings.LastIndex(rig.URL, ":")+1:]

	for _, tc := range []struct {
		name string
		// stored is the site the profile's current context names, or empty
		// for no profile at all.
		stored string
		// jr is the binary the guard is handed, or empty for the built one.
		jr   string
		want int
		says []string
	}{
		{
			name:   "the profile names the rig",
			stored: rig.URL,
			want:   0,
			says:   []string{"rig: Jira 10.4.0 at " + rig.URL},
		},
		// The incident. Same host, a different address, and a credential
		// stored for it, so nothing short of asking the rig can tell.
		{
			name:   "the profile names yesterday's container",
			stored: "http://127.0.0.1:1",
			want:   2,
			says:   []string{"refusing", "http://127.0.0.1:1", rig.URL, "make dc-up"},
		},
		{
			name: "there is no profile",
			want: 2,
			says: []string{"no throwaway profile", "make dc-up"},
		},
		// A file that is executable by mode and not by this machine, which is
		// what bin/jr is in a dev container after a build on the host.
		{
			name:   "the binary cannot run here",
			stored: rig.URL,
			jr:     "not-for-this-machine",
			want:   2,
			says:   []string{"does not run on this machine", "set JR"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			// common.sh derives every path from its own location and sources
			// the .env beside it, so a copy in an empty directory is a rig
			// whose only configuration is the port the fake one listens on.
			if err := os.WriteFile(filepath.Join(dir, "common.sh"), []byte(readFile(t, rigCommon)), 0o644); err != nil {
				t.Fatalf("copying common.sh: %v", err)
			}
			env := "JIRA_PORT=" + port + "\nCONTEXT_PATH=\n"
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
				t.Fatalf("writing .env: %v", err)
			}

			profile := filepath.Join(dir, "profile")
			if err := os.MkdirAll(profile, 0o755); err != nil {
				t.Fatalf("making the profile: %v", err)
			}
			if err := os.WriteFile(filepath.Join(profile, "token"), []byte("recorded-token"), 0o600); err != nil {
				t.Fatalf("writing the token: %v", err)
			}
			if tc.stored != "" {
				// Through the real binary, so the profile is whatever jr
				// writes for that site and not this test's idea of it.
				create := exec.Command(bin, "context", "create", "default", "--site", tc.stored) //nolint:gosec // a binary this test just built.
				create.Env = append(os.Environ(),
					"XDG_CONFIG_HOME="+filepath.Join(profile, "config"),
					"XDG_STATE_HOME="+filepath.Join(profile, "state"),
					"XDG_CACHE_HOME="+filepath.Join(profile, "cache"),
				)
				if out, err := create.CombinedOutput(); err != nil {
					t.Fatalf("creating the context: %v\n%s", err, out)
				}
			}

			jr := bin
			if tc.jr != "" {
				jr = filepath.Join(dir, tc.jr)
				// An ELF header and nothing behind it: exec refuses it, and
				// bash, offered it as a script instead, refuses it as binary.
				if err := os.WriteFile(jr, []byte("\x7fELF\x00\x00\x00\x00"), 0o755); err != nil {
					t.Fatalf("writing the stub: %v", err)
				}
			}

			cmd := exec.Command("bash", "-c", `. "$1/common.sh" && require_rig`, "bash", dir)
			cmd.Env = append(os.Environ(), "JR="+jr)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()

			got := 0
			if err != nil {
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok {
					t.Fatalf("running require_rig: %v", err)
				}
				got = exit.ExitCode()
			}
			if got != tc.want {
				t.Errorf("require_rig exited %d, want %d.\nstdout:\n%s\nstderr:\n%s",
					got, tc.want, stdout.String(), stderr.String())
			}

			// The address it verified is the one it prints, and only then:
			// a caller does `base=$(require_rig) || exit 2` and a refusal
			// that printed an address would hand it something to record
			// against.
			wantOut := ""
			if tc.want == 0 {
				wantOut = rig.URL + "\n"
			}
			if stdout.String() != wantOut {
				t.Errorf("require_rig printed %q on stdout, want %q", stdout.String(), wantOut)
			}

			for _, s := range tc.says {
				if !strings.Contains(stderr.String(), s) {
					t.Errorf("require_rig did not say %q.\nstderr:\n%s", s, stderr.String())
				}
			}
		})
	}
}

// TestEveryRigScriptRunsTheGuardFirst holds the recording scripts to the guard.
//
// The check used to be four lines copied into six files, each testing that a
// config file existed and a binary was executable by mode, and the copy is
// how the address check was missing from all of them at once. One function
// in common.sh, and this is the list of who has to call it.
func TestEveryRigScriptRunsTheGuardFirst(t *testing.T) {
	dir := repoRoot + "/scripts/dc"
	for _, script := range []string{
		"record.sh", "record-transport.sh", "record-writes.sh",
		"record-refusals.sh", "smoke.sh",
	} {
		body := readFile(t, filepath.Join(dir, script))
		if !strings.Contains(body, "require_rig") {
			t.Errorf("%s never calls require_rig, so it records against whatever "+
				"the profile names, which for a fortnight was yesterday's container",
				script)
		}
		if strings.Contains(body, "config.toml") {
			t.Errorf("%s checks the profile itself. The check belongs in "+
				"require_rig, where the address is compared too", script)
		}
	}

	// up.sh is the one script that may write the profile, and it ends by
	// running the same guard, so what it leaves is what every other script
	// will accept.
	up := readFile(t, filepath.Join(dir, "up.sh"))
	login := strings.Index(up, `"$jr" auth login`)
	guard := strings.LastIndex(up, "require_rig")
	if login < 0 || guard < login {
		t.Errorf("up.sh does not run require_rig after logging the profile in, " +
			"so dc-up can report ready and leave a profile no recording script " +
			"will accept")
	}
	if !strings.Contains(up[:login], `rm -f "$profile/config/jr/config.toml"`) {
		t.Errorf("up.sh reuses the profile's context, and `auth login` leaves an " +
			"existing one alone: the address it names is the container's from " +
			"the last time the compose network was created")
	}
}

// fullProfile is the shipped full build, which is the one the rig records with.
func fullProfile(t *testing.T) profile {
	t.Helper()

	profiles := profilesFromMakefile(t)
	i := slices.IndexFunc(profiles, func(p profile) bool { return p.name == "full" })
	if i < 0 {
		t.Fatalf("the Makefile ships %v; this test needs the full profile", profiles)
	}
	return profiles[i]
}
