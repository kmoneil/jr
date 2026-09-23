package lint_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stubTool stands in for gofumpt, staticcheck and golangci-lint, which the
// test job does not install. It records how it was called, and reports a
// finding when the environment asks it to: gofumpt the way gofumpt -l does,
// by printing a file and exiting 0, and the other two by exiting 1.
const stubTool = `#!/bin/sh
name=$(basename "$0")
printf '%s %s\n' "$name" "$*" >> "$STUB_LOG"
case $name in
gofumpt) [ -n "$STUB_GOFUMPT" ] && printf '%s\n' "$STUB_GOFUMPT"; exit 0 ;;
staticcheck) [ -n "$STUB_STATICCHECK" ] && { printf '%s\n' "$STUB_STATICCHECK"; exit 1; }; exit 0 ;;
golangci-lint) [ -n "$STUB_LINT" ] && { printf '%s\n' "$STUB_LINT"; exit 1; }; exit 0 ;;
esac
exit 0
`

// preflightModule is a repository with two packages and a lint package,
// committed on main with a feature branch checked out, which is the shape
// preflight measures from.
func preflightModule(t *testing.T) string {
	t.Helper()
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).FindString(readFile(t, filepath.Join(repoRoot, "go.mod")))
	if goLine == "" {
		t.Fatal("the repository's go.mod has no go line")
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod": "module example.invalid/pf\n\n" + goLine + "\n",
		// The four tag sets preflight reads through print-%, the way it
		// reads the real Makefile's.
		"Makefile": "TAGS_FULL := pfa,pfb\nTAGS_AGENT := pfa\nTAGS_READER := pfb\nTAGS_CI :=\n" +
			"print-%:\n\t@echo '$($*)'\n",
		"a/a.go":                     "package a\n\n// A is untouched by every case.\nfunc A() int { return 1 }\n",
		"b/b.go":                     "package b\n\n// B is what the cases change.\nfunc B() int { return 2 }\n",
		"b/b_test.go":                "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n\tif B() != 2 {\n\t\tt.Fatal(\"B\")\n\t}\n}\n",
		"internal/lint/lint_test.go": "package lint_test\n\nimport \"testing\"\n\nfunc TestDrift(t *testing.T) {}\n",
		"docs/guide.md":              "A guide.\n",
	})
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "switch", "-q", "-c", "feat/change")
	return dir
}

// runPreflight runs scripts/preflight in dir with the stubs on PATH, or with
// only those named in tools when a case is about one being missing.
func runPreflight(t *testing.T, dir string, stub map[string]string, tools []string, args ...string) (int, string, string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go on PATH: %v", err)
	}
	stubs := t.TempDir()
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(stubTool), 0o755); err != nil { //nolint:gosec // a stub that must be executable.
			t.Fatalf("writing the %s stub: %v", name, err)
		}
	}
	log := filepath.Join(stubs, "calls.log")
	script, err := filepath.Abs(filepath.Join(repoRoot, "scripts/preflight"))
	if err != nil {
		t.Fatalf("locating preflight: %v", err)
	}

	cmd := exec.Command("bash", append([]string{script}, args...)...) //nolint:gosec // a script in this repository.
	cmd.Dir = dir
	// PATH is built rather than extended, so a real gofumpt installed
	// beside go cannot stand in for a stub this case left out.
	// The parent make's MAKEFLAGS would reach preflight's own make calls,
	// and through them the tag sets it reads.
	cmd.Env = append(withoutParentMake(gitEnv(dir)),
		"PATH="+stubs+":"+filepath.Dir(goBin)+":/usr/bin:/bin",
		"GOWORK=off", "GOFLAGS=", "STUB_LOG="+log,
		"STUB_GOFUMPT="+stub["gofumpt"], "STUB_STATICCHECK="+stub["staticcheck"],
		"STUB_LINT="+stub["golangci-lint"],
	)
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	code := 0
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running preflight: %v", err)
		}
		code = exit.ExitCode()
	}
	calls, _ := os.ReadFile(log) //nolint:gosec // the stubs' own log, in a temporary directory.
	return code, out.String() + errs.String(), string(calls)
}

var allTools = []string{"gofumpt", "staticcheck", "golangci-lint"}

// TestPreflightChecksThePackagesThatChanged is the selection, which is the
// part that can go quietly wrong: a list that comes back empty checks
// nothing, and every step reports ok over it.
func TestPreflightChecksThePackagesThatChanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		want    []string // package lines, in order
		wantAll bool
	}{
		{
			name: "a source file, a fixture, and an untracked package",
			files: map[string]string{
				"b/testdata/case.json": "{}\n",
				"c/c.go":               "package c\n",
				"docs/guide.md":        "A guide, edited.\n",
			},
			want: []string{"./b", "./c"},
		},
		{
			name:  "only documentation",
			files: map[string]string{"docs/guide.md": "A guide, edited.\n"},
			want:  nil,
		},
		{
			name:    "the module file",
			files:   map[string]string{"go.mod": ""},
			wantAll: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := preflightModule(t)
			if tc.wantAll {
				tc.files["go.mod"] = readFileAt(t, dir, "go.mod") + "\n// edited\n"
			}
			writeTree(t, dir, tc.files)

			code, out, _ := runPreflight(t, dir, nil, allTools, "--list")
			if code != 0 {
				t.Fatalf("--list exited %d:\n%s", code, out)
			}
			var got []string
			for line := range strings.SplitSeq(out, "\n") {
				if p, ok := strings.CutPrefix(line, "package  "); ok {
					got = append(got, p)
				}
			}
			want := tc.want
			if tc.wantAll {
				want = []string{"./..."}
			}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("packages = %v, want %v:\n%s", got, want, out)
			}
			// The lint package runs whatever changed: its drift checks
			// read the whole tree.
			if !tc.wantAll && !strings.Contains(out, "test     ./internal/lint") {
				t.Errorf("internal/lint is not among the tests:\n%s", out)
			}
		})
	}
}

// TestEveryPreflightStepCanFail stages a red for each step and requires that
// step, and only that step, to report it. A preflight whose step cannot fail
// reads exactly like one whose tree is clean, which is the failure this whole
// tool is meant to prevent arriving one layer up.
func TestEveryPreflightStepCanFail(t *testing.T) {
	// A change that every step passes: the same behaviour, a different
	// comment, so the package differs from the base and gets checked.
	const clean = "package b\n\n// B is what the cases change, and this one did.\nfunc B() int { return 2 }\n"
	for _, tc := range []struct {
		step  string // the step that must fail, or empty for none
		files map[string]string
		stub  map[string]string
		tools []string
	}{
		{step: "", files: map[string]string{"b/b.go": clean}},
		{
			step:  "gofumpt",
			files: map[string]string{"b/b.go": clean},
			stub:  map[string]string{"gofumpt": "b/b.go"},
		},
		// A malformed struct tag, because structtag is not among the vet
		// checks go test runs for itself: printf is, and a printf mistake
		// fails the test step as well, which is two reds for one cause.
		{
			step: "vet",
			files: map[string]string{"b/b.go": "package b\n\n// T is tagged badly.\n" +
				"type T struct {\n\tX int `json:\"x\",omitempty`\n}\n\n// B is unchanged.\nfunc B() int { return 2 }\n"},
		},
		{
			step: "fix",
			files: map[string]string{"b/b.go": "package b\n\nimport \"strings\"\n\n// B counts.\n" +
				"func B() int {\n\tn := 0\n\tfor _, s := range strings.Split(\"x,y\", \",\") {\n" +
				"\t\tn += len(s)\n\t}\n\treturn n\n}\n"},
		},
		{
			step:  "unused",
			files: map[string]string{"b/b.go": clean},
			stub:  map[string]string{"staticcheck": "b/b.go:4:6: func B is unused (U1000)"},
		},
		{
			step:  "lint",
			files: map[string]string{"b/b.go": clean},
			stub:  map[string]string{"golangci-lint": "b/b.go:1:1: a finding (godot)"},
		},
		{
			step: "test",
			files: map[string]string{
				"b/b.go":      clean,
				"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { t.Fatal(\"red\") }\n",
			},
		},
		// A tool that is not installed fails its step rather than skipping
		// it: a check that did not run is not a check that passed.
		{
			step:  "gofumpt",
			files: map[string]string{"b/b.go": clean},
			tools: []string{"staticcheck", "golangci-lint"},
		},
	} {
		name := tc.step
		if name == "" {
			name = "nothing"
		}
		if tc.tools != nil {
			name += " missing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := preflightModule(t)
			writeTree(t, dir, tc.files)
			tools := tc.tools
			if tools == nil {
				tools = allTools
			}

			code, out, calls := runPreflight(t, dir, tc.stub, tools)
			var failing []string
			for _, m := range regexp.MustCompile(`(?m)^  FAIL  (\S+)`).FindAllStringSubmatch(out, -1) {
				failing = append(failing, m[1])
			}
			want := []string{}
			if tc.step != "" {
				want = []string{tc.step}
			}
			if strings.Join(failing, " ") != strings.Join(want, " ") {
				t.Errorf("failing steps = %v, want %v:\n%s", failing, want, out)
			}
			if (code == 0) != (tc.step == "") {
				t.Errorf("exit %d with failing steps %v:\n%s", code, failing, out)
			}
			// Every step reports, so a red does not hide the next one.
			if n := strings.Count(out, "\n  ok    ") + strings.Count(out, "\n  FAIL  "); n != 6 {
				t.Errorf("%d steps reported, want all 6:\n%s", n, out)
			}
			// The changed package reached the tools, and the untouched one
			// did not.
			if tc.tools == nil {
				for _, want := range []string{"gofumpt -l b/b.go", "staticcheck -checks=U1000 ./b", "golangci-lint run ./b"} {
					if !strings.Contains(calls, want) {
						t.Errorf("no call %q; the stubs saw:\n%s", want, calls)
					}
				}
				if strings.Contains(calls, "./a") {
					t.Errorf("an unchanged package was checked:\n%s", calls)
				}
			}
		})
	}
}

// readFileAt reads a file from a directory the test made.
func readFileAt(t *testing.T, dir, name string) string {
	t.Helper()
	return readFile(t, filepath.Join(dir, name))
}
