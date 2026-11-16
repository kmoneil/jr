package cli_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// skillDocs is what `jr skill` prints for each file of the skill, keyed by its
// slash-separated path under a skill directory.
//
// The references are listed from skillassets on disk, which is the tree the
// binary embeds, so a reference added there is covered here without an edit.
func skillDocs(t *testing.T) map[string]string {
	t.Helper()
	docs := map[string]string{"SKILL.md": printed(t, "skill")}
	refs, err := os.ReadDir(filepath.Join("skillassets", "references"))
	if err != nil {
		t.Fatalf("reading the embedded references: %v", err)
	}
	for _, e := range refs {
		docs["references/"+e.Name()] = printed(t, "skill", strings.TrimSuffix(e.Name(), ".md"))
	}
	if len(docs) < 2 {
		t.Fatalf("the skill has %d documents, which cannot be the whole skill", len(docs))
	}
	return docs
}

func printed(t *testing.T, args ...string) string {
	t.Helper()
	got := run(t, nil, args...)
	if got.exit != exitcode.OK || got.stdout == "" {
		t.Fatalf("jr %v: exit %v, %d bytes\n%s", args, got.exit, len(got.stdout), got.stderr)
	}
	return got.stdout
}

// tree reads every file under dir, keyed by slash-separated path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return out
}

func assertTree(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := tree(t, dir)
	for name, body := range want {
		switch g, ok := got[name]; {
		case !ok:
			t.Errorf("%s is missing from %s", name, dir)
		case g != body:
			t.Errorf("%s holds %d bytes, want the %d `jr skill` prints", name, len(g), len(body))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s holds %s, which nothing wrote", dir, name)
		}
	}
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertRefused(t *testing.T, got result, exit exitcode.Code, code string) {
	t.Helper()
	if got.exit != exit {
		t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, exit, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a refusal wrote to stdout:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "<code>"+code+"</code>") {
		t.Errorf("stderr does not carry %s:\n%s", code, got.stderr)
	}
}

// TestSkillDirWritesWhatSkillPrints is the reason --dir exists: the files are
// the bytes `jr skill` prints, every one of them and nothing else, so an
// install that runs it needs no list of references of its own.
func TestSkillDirWritesWhatSkillPrints(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills", "jr")
	got := run(t, nil, "skill", "--dir", dir)
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	if got.stdout != "" || got.stderr != "" {
		t.Errorf("--dir printed something: stdout %q, stderr %q", got.stdout, got.stderr)
	}
	assertTree(t, dir, skillDocs(t))
}

func TestSkillDirWritesIntoAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if got := run(t, nil, "skill", "--dir", dir); got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	assertTree(t, dir, skillDocs(t))
}

// TestSkillDirRefusesToReplaceWithoutForce holds the rule `attachment download`
// set: a write that silently replaced a file would be indistinguishable from
// one that worked.
func TestSkillDirRefusesToReplaceWithoutForce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), "an older skill\n")

	assertRefused(t, run(t, nil, "skill", "--dir", dir), exitcode.Conflict, "DESTINATION_EXISTS")
	assertTree(t, dir, map[string]string{"SKILL.md": "an older skill\n"})
	if _, err := os.Stat(filepath.Join(dir, "references")); err == nil {
		t.Error("a refusal created references/")
	}
}

func TestSkillDirForceReplacesTheSkill(t *testing.T) {
	dir := t.TempDir()
	want := skillDocs(t)
	for name := range want {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), "stale\n")
	}

	got := run(t, nil, "skill", "--dir", dir, "--force")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v\nstderr: %s", got.exit, got.stderr)
	}
	assertTree(t, dir, want)
}

// TestSkillDirRefusesStrayFilesEvenWithForce covers the upgrade that would go
// wrong quietly: a reference an older build carried, left beside a skill that
// no longer mentions it.
func TestSkillDirRefusesStrayFilesEvenWithForce(t *testing.T) {
	for _, stray := range []string{"notes.txt", "references/retired.md", "references/old/x.md"} {
		t.Run(stray, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, filepath.FromSlash(stray)), "mine\n")

			got := run(t, nil, "skill", "--dir", dir, "--force")
			assertRefused(t, got, exitcode.Conflict, "STRAY_FILES")
			named := stray
			if strings.Count(stray, "/") > 1 {
				named = "references/old"
			}
			if !strings.Contains(got.stderr, named) {
				t.Errorf("the refusal does not name %s:\n%s", named, got.stderr)
			}
			assertTree(t, dir, map[string]string{stray: "mine\n"})
		})
	}
}

func TestSkillDirNamesTenStraysAndCountsTheRest(t *testing.T) {
	dir := t.TempDir()
	for i := range 25 {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("stray-%02d", i)), "")
	}

	got := run(t, nil, "skill", "--dir", dir)
	assertRefused(t, got, exitcode.Conflict, "STRAY_FILES")
	for _, want := range []string{"stray-00", "stray-09", "and 15 more"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(got.stderr, "stray-10") {
		t.Errorf("the refusal names more than ten:\n%s", got.stderr)
	}
}

func TestSkillDirRefusesAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "skill")
	writeFile(t, file, "not a directory\n")

	assertRefused(t, run(t, nil, "skill", "--dir", file, "--force"),
		exitcode.Conflict, "NOT_A_DIRECTORY")
}

// TestSkillDirRefusesALinkEvenWithForce is issue 299. A link at --dir is
// another install, Homebrew's keg or a clone's skills/jr, and --force used to
// write through it: the keg's SKILL.md replaced, exit 0, and the path still a
// link, so nothing said whose files had changed. Without --force, a link to an
// empty directory was written through the same way.
func TestSkillDirRefusesALinkEvenWithForce(t *testing.T) {
	cases := []struct {
		name   string
		target map[string]string
	}{
		{"to another install", map[string]string{"SKILL.md": "another install's skill\n"}},
		{"to an empty directory", map[string]string{}},
		{"to nothing", nil},
	}
	for _, tc := range cases {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, force %v", tc.name, force), func(t *testing.T) {
				root := t.TempDir()
				target := filepath.Join(root, "keg", "skill")
				if tc.target != nil {
					if err := os.MkdirAll(target, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				for name, body := range tc.target {
					writeFile(t, filepath.Join(target, name), body)
				}
				link := filepath.Join(root, "skills", "jr")
				symlink(t, target, link)

				args := []string{"skill", "--dir", link}
				if force {
					args = append(args, "--force")
				}
				got := run(t, nil, args...)
				assertRefused(t, got, exitcode.Conflict, "DESTINATION_IS_LINK")
				if !strings.Contains(got.stderr, target) {
					t.Errorf("the refusal does not name what the link points to:\n%s", got.stderr)
				}

				if dest, err := os.Readlink(link); err != nil || dest != target {
					t.Errorf("the link now reads %q, %v; want it untouched, pointing to %s", dest, err, target)
				}
				if tc.target != nil {
					assertTree(t, target, tc.target)
				} else if _, err := os.Lstat(target); err == nil {
					t.Errorf("a refusal created %s", target)
				}
			})
		}
	}
}

// symlink makes link point to target, and skips where the platform will not
// let this user make one, as Windows without developer mode does not.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
}

func TestSkillDirUsageRefusals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never")
	cases := []struct {
		name string
		args []string
		code string
	}{
		{"force alone", []string{"skill", "--force"}, "FORCE_WITHOUT_DIR"},
		{"an empty dir", []string{"skill", "--dir", ""}, "EMPTY_DIR"},
		{"a reference", []string{"skill", "workflows", "--dir", dir}, "DIR_AND_REFERENCE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, run(t, nil, tc.args...), exitcode.Usage, tc.code)
		})
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("a refused invocation created %s", dir)
	}
}
