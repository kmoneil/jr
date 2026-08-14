package issue_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/site"
)

// TestAWorklogDateWithATimeOfDayIsRefusedOnDataCenterOnly is a rule about a
// field and a deployment together, which is why it is asserted on both.
//
// Data Center 10.4 refuses a time of day on `worklogDate` and accepts one on
// `updated`. Cloud accepts it on both. Measured 2026-08-14 against the rig and
// the Cloud sandbox, after a transcript showed the 400 and the first fix
// proposed for it was a blanket refusal that would have broken Cloud.
func TestAWorklogDateWithATimeOfDayIsRefusedOnDataCenterOnly(t *testing.T) {
	cmd, ok := registry.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}

	validate := func(kind site.Kind, flag, value string) error {
		flags := registry.NewFlags()
		flags.SetString(flag, value)
		return cmd.Validate(t.Context(), &registry.Invocation{
			Jira:  &stubSession{project: "ENG", kind: kind},
			Flags: flags, Limit: registry.Limit{N: 50},
			Progress: registry.NoProgress,
		})
	}

	for _, flag := range []string{"worklog-after", "worklog-before"} {
		err := validate(site.DataCenter, flag, "2026-08-10 00:00")
		if err == nil {
			t.Errorf("--%s took a time of day on Data Center", flag)
		} else if code := errs.Coerce(err).Code; code != "INVALID_DATE" {
			t.Errorf("--%s refused as %q, want INVALID_DATE", flag, code)
		}

		// The same value on Cloud, where the server takes it. A refusal here
		// would be this tool inventing a limit its server does not have.
		if err := validate(site.Cloud, flag, "2026-08-10 00:00"); err != nil {
			t.Errorf("--%s was refused on Cloud, which accepts it: %v", flag, err)
		}

		// A bare date and an offset are what the remedy offers, so they have to
		// work on the deployment that refused.
		for _, good := range []string{"2026-08-10", "-7d"} {
			if err := validate(site.DataCenter, flag, good); err != nil {
				t.Errorf("--%s %s was refused on Data Center: %v", flag, good, err)
			}
		}
	}

	// The neighbouring field, which takes a minute on both. Refusing here would
	// be the over-broad fix wearing a narrower name.
	if err := validate(site.DataCenter, "updated-after", "2026-08-10 00:00"); err != nil {
		t.Errorf("--updated-after was refused on Data Center, which accepts it: %v", err)
	}
}
