package registry

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kmoneil/jr/internal/buildinfo"
	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// The zone a result's timestamps are written in is asked for with --tz, with
// JIRA_TZ, or by a context, and it is one of three words or a zone's name.
const (
	// ZoneUTC is the default, and writes every timestamp as a resource built
	// it. Asking for it by name is the same bytes as asking for nothing.
	ZoneUTC = "utc"
	// ZoneAccount is the zone on the Jira account's profile, which is the zone
	// Jira reads every date this tool sends in. Output in it matches input.
	ZoneAccount = "account"
	// ZoneLocal is the zone of the machine this process runs on. Over MCP
	// that is the server's machine and not the model's reader's.
	ZoneLocal = "local"
)

// ZoneFlagName is the flag, and the tool argument, that names the zone.
const ZoneFlagName = "tz"

// EnvTZ sets the zone for every command in a shell.
//
// It is here beside EnvFormat for the reason that one is: the flag it backs is
// declared here and its usage string names it. internal/jctx reads the same
// variable under its own name, because resolution lives there.
const EnvTZ = "JIRA_TZ"

// localZone is the machine's zone. A variable so a test can name one, since
// the real one is whatever the machine running the suite happens to be in.
var localZone = func() *time.Location { return time.Local }

// ZoneFlag is the --tz flag every command that writes a timestamp carries.
//
// It is derived, as --limit is, and for the reason AllFlags gives: one
// derivation feeds the parser, `jr schema`, the generated reference and the
// MCP tool list, so none of them can describe a flag the others do not have.
//
// It is not a global, though it reads like one. A global is bound on the root
// and the parser takes it on every command, and on a command that writes no
// timestamp this one could only be accepted and ignored. Derived, it does not
// exist there, and `jr version --tz local` is an unknown flag, which is true.
func ZoneFlag() Flag {
	return Flag{
		Name: ZoneFlagName, Type: TypeString,
		Usage: "timezone timestamps are written in: " +
			strings.Join([]string{ZoneUTC, ZoneAccount, ZoneLocal}, "|") +
			"|<IANA name>. Values keep their offset, so each is still RFC " +
			"3339. Default " + ZoneUTC + ", or " + EnvTZ + ", or the context's",
	}
}

// TakesZone reports whether this command has --tz: whether it writes a
// timestamp, and writes it for a reader.
//
// It is derived from the kinds the command declares, so a command gains the
// flag by declaring a timestamp in its schema and by nothing else.
func (c *Command) TakesZone() bool {
	if c.TimestampsStayUTC {
		return false
	}
	return slices.ContainsFunc(c.Outputs, func(o Output) bool {
		return render.KindHasTimestamp(o.Kind)
	})
}

// CheckZone refuses a zone setting that names nothing, before any work is
// done on the strength of it.
//
// It does not resolve `account`, which costs a request. It answers the
// question that needs no session: is this one of the three words, or a zone
// this build can read.
//
// from names where the setting was read, `--tz`, `JIRA_TZ`, a context, so the
// refusal says which of them to change. A zone that stopped being one is far
// likelier to be a line in a file than a word typed on this invocation.
func CheckZone(from, setting string) error {
	setting = strings.TrimSpace(setting)
	_, err := namedZone(setting)
	if e, ok := errs.AsError(err); ok {
		e.Message = from + " names " + strconv.Quote(setting) + ", which is not a timezone"
	}
	return err
}

// checkZoneTyped is Gate's part: the --tz on this invocation, held to the
// same question before anything is validated or sent.
//
// An empty one is refused and not read as UTC. It would fall back to the
// environment and then to the context, so `--tz ""` typed to mean "no zone"
// would be answered in whichever zone one of those names, which is
// EMPTY_SCOPE's defect over again.
func checkZoneTyped(cmd *Command, inv *Invocation) error {
	if !cmd.TakesZone() || !inv.Flags.WasSet(ZoneFlagName) {
		return nil
	}
	typed := inv.Flags.String(ZoneFlagName)
	if strings.TrimSpace(typed) == "" {
		return errs.Usage("INVALID_TZ", "--%s was given an empty value", ZoneFlagName).
			WithDetail("an empty --%s does not mean UTC; it falls back to %s "+
				"and then to the context's", ZoneFlagName, EnvTZ).
			WithRemedy("pass --%s %s to mean UTC, or drop the flag", ZoneFlagName, ZoneUTC)
	}
	return CheckZone("--"+ZoneFlagName, typed)
}

// namedZone is the location a setting names without asking anybody: nil for
// the three words, each of which is settled elsewhere, and the zone itself
// for a name.
func namedZone(setting string) (*time.Location, error) {
	switch setting {
	case "", ZoneUTC, ZoneAccount, ZoneLocal:
		return nil, nil
	}
	loc, err := time.LoadLocation(setting)
	if err != nil {
		return nil, errs.Usage("INVALID_TZ", "%q is not a timezone", setting).
			WithDetail("the zone database this build carries has no such name, "+
				"and names are case-sensitive").
			WithRemedy("use %s, %s, %s, or an IANA name like Europe/Berlin",
				ZoneUTC, ZoneAccount, ZoneLocal).
			Wrap(err)
	}
	return loc, nil
}

// ZoneDefault is the zone an invocation falls back to when it names none, and
// where that came from: the environment or the context on the command line,
// and the server's own setting for a tool call.
type ZoneDefault struct {
	Setting string
	// From names the source, for a refusal of the setting.
	From string
}

// SettleZone resolves the zone this invocation's timestamps are written in and
// leaves it on the invocation, before the command runs.
//
// Before, and not when the document is written, because resolving can fail:
// `account` is a request to the site, and an account may have no zone the site
// will name. A command that had already changed Jira and then could not write
// its result would report a failure about a write that happened, with nothing
// on stdout to say what it did. Settled here, the same failure refuses the
// command while it has done nothing.
//
// A command that takes no zone settles none, so `account` costs a request only
// where there is a timestamp to write.
func SettleZone(ctx context.Context, cmd *Command, inv *Invocation, def ZoneDefault) error {
	if !cmd.TakesZone() {
		return nil
	}
	setting, from := def.Setting, def.From
	if typed := strings.TrimSpace(inv.Flags.String(ZoneFlagName)); typed != "" {
		setting, from = typed, "--"+ZoneFlagName
	}
	if err := CheckZone(from, setting); err != nil {
		return err
	}
	loc, err := zoneFor(ctx, inv.Jira, strings.TrimSpace(setting))
	if err != nil {
		return err
	}
	inv.Zone = loc
	return nil
}

// zoneFor turns a setting into a location, or into nil for UTC, which is how
// every timestamp already reads and so needs nothing done to it.
func zoneFor(ctx context.Context, session Session, setting string) (*time.Location, error) {
	switch setting {
	case "", ZoneUTC:
		return nil, nil
	case ZoneLocal:
		return localZone(), nil
	case ZoneAccount:
		return accountZone(ctx, session)
	}
	return namedZone(setting)
}

// accountZone asks the site which zone the account keeps.
//
// One GET to /myself. The refusals are site.Account.Location's own, with the
// detail and the remedy rewritten: that function serves a caller reading a
// date, and tells them to use a relative offset, which is no help to somebody
// choosing how a timestamp is printed.
func accountZone(ctx context.Context, session Session) (*time.Location, error) {
	if session == nil {
		return nil, errs.Runtime("NO_SESSION",
			"--%s %s cannot be resolved without a connection to Jira",
			ZoneFlagName, ZoneAccount)
	}
	meta, err := session.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	me, err := site.Whoami(ctx, meta.Client, meta.Info)
	if err != nil {
		return nil, err
	}
	loc, err := me.Location()
	if err != nil {
		if e, ok := errs.AsError(err); ok {
			e.Detail = "the zone `" + ZoneAccount + "` is the one on the Jira " +
				"account's profile, and there is none to write timestamps in"
			e.Remedy = "name a zone, as in `" + buildinfo.App + " ... --" +
				ZoneFlagName + " Europe/Berlin`, or use " + ZoneUTC
		}
		return nil, err
	}
	return loc, nil
}
