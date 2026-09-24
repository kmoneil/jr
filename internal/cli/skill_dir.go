package cli

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
)

// The skill is instructions, not a secret. A skill loader running as another
// user, or a copy made for a team, has to be able to read it.
const (
	skillFileMode fs.FileMode = 0o644
	skillDirMode  fs.FileMode = 0o755
)

// strayNamed is how many entries a STRAY_FILES refusal names before it counts
// the rest. A directory named by mistake, a home directory, can hold
// thousands.
const strayNamed = 10

// skillLayout is every file of the skill, slash-separated and relative to the
// skill directory, SKILL.md first. It comes from the embedded tree, the same
// source the reference argument is checked against, so a reference added to
// skillassets is written without a second edit.
func skillLayout() []string {
	out := []string{skillMain}
	for _, ref := range skillReferences() {
		out = append(out, path.Join(skillRefDir, ref+".md"))
	}
	return out
}

// writeSkillDir writes the whole skill into dir, creating it if need be.
//
// Every document is rendered, and the directory inspected, before a byte is
// written, so each refusal leaves the directory exactly as it was. Each file
// is replaced through a temporary file beside it, so a reader meets the old
// document or the new one and never a prefix of either. The set is not one
// transaction: a failure part way names what was already written.
func (a *app) writeSkillDir(dir string, force bool) error {
	layout := skillLayout()
	bodies := make([]string, len(layout))
	for i, name := range layout {
		body, err := a.skillDocument(name)
		if err != nil {
			return err
		}
		bodies[i] = body
	}

	if err := checkSkillDir(dir, layout, force); err != nil {
		return err
	}

	refs := filepath.Join(dir, skillRefDir)
	if err := os.MkdirAll(refs, skillDirMode); err != nil {
		return errs.Runtime("SKILL_UNWRITABLE", "cannot create %s", refs).Wrap(err)
	}
	for i, name := range layout {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err := writeSkillFile(dest, bodies[i]); err != nil {
			e := errs.Runtime("SKILL_UNWRITABLE", "cannot write %s", dest)
			if i > 0 {
				e = e.WithDetail("already written: %s", strings.Join(layout[:i], ", "))
			}
			return e.Wrap(err)
		}
	}
	return nil
}

// checkSkillDir refuses a directory the skill cannot be written into exactly.
//
// One that does not exist yet is fine: it is created. One that exists may hold
// the skill's own files, which --force replaces, and nothing else. An entry the
// skill does not write is refused even with --force, because both ways past it
// are wrong: deleting it removes something this command did not create, and
// keeping it leaves, say, a reference an older build carried beside a skill
// that no longer mentions it, where an agent can still read it.
func checkSkillDir(dir string, layout []string, force bool) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return errs.Runtime("SKILL_UNWRITABLE", "cannot read %s", dir).Wrap(err)
	case !info.IsDir():
		return errs.New(exitcode.Conflict, "NOT_A_DIRECTORY",
			"%s exists and is not a directory", dir).
			WithRemedy("pass --dir a directory, or a path that does not exist yet")
	}

	existing, stray, err := sortSkillDir(dir, layout)
	if err != nil {
		return err
	}
	if len(stray) > 0 {
		return errs.New(exitcode.Conflict, "STRAY_FILES",
			"%s holds files the skill does not write", dir).
			WithDetail("%s", nameSome(stray)).
			WithRemedy("remove them, or pass --dir an empty directory")
	}
	if len(existing) > 0 && !force {
		return errs.New(exitcode.Conflict, "DESTINATION_EXISTS",
			"%s already holds the skill's %s", dir, strings.Join(existing, ", ")).
			WithRemedy("pass --force to replace them, or --dir a directory that holds no skill")
	}
	return nil
}

// sortSkillDir splits what dir holds into the skill's own files and everything
// else, both slash-separated and relative to dir.
func sortSkillDir(dir string, layout []string) (existing, stray []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, errs.Runtime("SKILL_UNWRITABLE", "cannot read %s", dir).Wrap(err)
	}
	for _, e := range entries {
		switch {
		case e.Name() == skillRefDir && e.IsDir():
			refs, err := os.ReadDir(filepath.Join(dir, skillRefDir))
			if err != nil {
				return nil, nil, errs.Runtime("SKILL_UNWRITABLE",
					"cannot read %s", filepath.Join(dir, skillRefDir)).Wrap(err)
			}
			for _, r := range refs {
				existing, stray = sortEntry(path.Join(skillRefDir, r.Name()), r, layout, existing, stray)
			}
		default:
			existing, stray = sortEntry(e.Name(), e, layout, existing, stray)
		}
	}
	return existing, stray, nil
}

// sortEntry files one entry as the skill's own or as a stray. A directory is
// never the skill's own, whatever it is called: the skill writes files.
func sortEntry(name string, e fs.DirEntry, layout, existing, stray []string) ([]string, []string) {
	if !e.IsDir() && slices.Contains(layout, name) {
		return append(existing, name), stray
	}
	return existing, append(stray, name)
}

// nameSome lists the first strayNamed names and counts the rest.
func nameSome(names []string) string {
	if len(names) <= strayNamed {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:strayNamed], ", ") +
		", and " + strconv.Itoa(len(names)-strayNamed) + " more"
}

// writeSkillFile replaces dest with body through a temporary file beside it.
func writeSkillFile(dest, body string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// After a successful rename there is nothing here to remove, and the
	// error saying so is not one.
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(skillFileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dest)
}
