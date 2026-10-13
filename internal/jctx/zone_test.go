package jctx_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/jctx"
)

// TestTheZoneComesFromTheEnvironmentThenTheContext is the two sources this
// package knows about. --tz is not among them: it is not a global, it exists
// only on a command that writes a timestamp, and it is read there, over
// whatever this resolves to.
//
// The source is kept beside the value because a zone that names nothing is
// refused, and the refusal has to say which of the two to go and change.
func TestTheZoneComesFromTheEnvironmentThenTheContext(t *testing.T) {
	cfg := &jctx.Config{
		Current: "work",
		Contexts: map[string]jctx.Context{
			"work":  {Site: "https://work.atlassian.invalid", TZ: "Europe/Berlin"},
			"plain": {Site: "https://plain.atlassian.invalid"},
		},
	}
	env := func(tz string) jctx.Getenv {
		return func(key string) string {
			if key == jctx.EnvTZ {
				return tz
			}
			return ""
		}
	}

	for _, tc := range []struct {
		name    string
		context string
		env     string
		want    string
		source  jctx.Source
	}{
		{"neither", "plain", "", "", jctx.FromNowhere},
		{"the context", "work", "", "Europe/Berlin", jctx.FromContext},
		{"the environment", "plain", "Asia/Tokyo", "Asia/Tokyo", jctx.FromEnv},
		{"the environment over the context", "work", "Asia/Tokyo", "Asia/Tokyo", jctx.FromEnv},
		// Exported and unset is a shell's way of having no value.
		{"an empty variable is no variable", "work", "  ", "Europe/Berlin", jctx.FromContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := jctx.Resolve(cfg, jctx.Overrides{Context: tc.context}, env(tc.env))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if r.TZ != tc.want || r.TZSource != tc.source {
				t.Errorf("zone = %q from %q, want %q from %q",
					r.TZ, r.TZSource, tc.want, tc.source)
			}
		})
	}
}
