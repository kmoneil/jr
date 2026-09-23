//go:build write

package issue

import (
	"io"
	"os"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
)

// descriptionFileFlag is --raw-field's write-side partner: the read hands a
// caller the stored bytes, and this puts bytes back without a shell in
// between. `--description "$(cat f)"` eats a trailing newline before the
// value is even seen; a file read here is exact.
const descriptionFileFlag = "description-file"

// editDescriptionKey is where validation leaves the file's content.
const editDescriptionKey = "issue.edit.description"

// validateDescriptionFile settles the description-file flag: its conflict
// with --description, the stdin case, and the read itself, which happens
// here so a missing file is a refusal before any request is spent.
func validateDescriptionFile(inv *registry.Invocation) error {
	path := inv.Flags.String(descriptionFileFlag)
	if path == "" {
		return nil
	}
	if inv.Flags.String("description") != "" {
		return errs.Usage("DESCRIPTION_AND_FILE",
			"--description and --"+descriptionFileFlag+" both name the new "+
				"description, and honoring both would mean ignoring one").
			WithRemedy("pass one of them")
	}

	var text []byte
	var err error
	if path == "-" {
		if inv.Stdin == nil {
			// Inside `mcp serve` stdin carries JSON-RPC frames; there is
			// nothing here to read a description from.
			return errs.Usage("NO_STDIN",
				"this caller has no stdin to read a description from").
				WithRemedy("pass a file path instead of -")
		}
		text, err = io.ReadAll(inv.Stdin)
	} else {
		text, err = os.ReadFile(path)
	}
	if err != nil {
		return errs.Usage("DESCRIPTION_FILE_UNREADABLE",
			"--"+descriptionFileFlag+" %s cannot be read", path).Wrap(err)
	}
	if len(text) == 0 {
		// jr cannot clear a description today, and an edit that silently
		// sent nothing would be the quiet approximation of one.
		return errs.Usage("EMPTY_DESCRIPTION_FILE",
			"%s holds no bytes, and an empty description was probably not "+
				"the edit meant here", descriptionSource(path)).
			WithRemedy("write the description into the file, or drop the flag")
	}
	inv.SetValue(editDescriptionKey, string(text))
	return nil
}

// descriptionSource names where the bytes came from, for a refusal.
func descriptionSource(path string) string {
	if path == "-" {
		return "stdin"
	}
	return path
}

// editDescription is the description an edit sends: the file's exact bytes
// when --description-file was given, the flag's value otherwise.
func editDescription(inv *registry.Invocation) string {
	if text, ok := inv.Value(editDescriptionKey).(string); ok {
		return text
	}
	return inv.Flags.String("description")
}
