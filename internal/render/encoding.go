package render

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"

	"github.com/kmoneil/jr/internal/errs"
)

// writeJSONValue encodes v with two-space indentation and no HTML escaping, so
// a JQL string containing < or & survives a round trip unchanged.
func writeJSONValue(w *writer, v any) {
	if w.err != nil {
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		w.err = errs.Runtime("ENCODE_FAILED", "cannot encode result as JSON").Wrap(err)
		return
	}
	w.raw(buf.String())
}

// writeYAMLValue encodes v with two-space indentation.
func writeYAMLValue(w *writer, v any) {
	if w.err != nil {
		return
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		_ = enc.Close()
		w.err = errs.Runtime("ENCODE_FAILED", "cannot encode result as YAML").Wrap(err)
		return
	}
	if err := enc.Close(); err != nil {
		w.err = errs.Runtime("ENCODE_FAILED", "cannot encode result as YAML").Wrap(err)
		return
	}
	w.raw(buf.String())
}
