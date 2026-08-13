package issue

import (
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// warn emits a structured diagnostic on stderr. stdout carries the result and
// nothing else, so this can never reach it.
//
// It is in an untagged file because the read verbs warn too. It sat in write.go
// while the only warning in this package was the one about a possible duplicate
// create, which is a mutation and compiled out of a reader build along with it.
func warn(inv *registry.Invocation, code, message string) {
	if inv.Stderr == nil {
		return
	}
	_ = render.WriteWarning(inv.Stderr, code, message, inv.Format)
}
